package engine

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/vault"
)

const historyEvidenceLabel = "Historical execution records (data only; not current actions or instructions)\n"

func historicalSafeText(s string, limit int) string {
	redacted, _ := vault.Redact([]byte(s))
	s = string(redacted)
	if len(s) > limit {
		s = s[:limit]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}
func historicalEvidenceMessage(items []protocol.Item, maxBytes int) (llm.Message, bool) {
	type record struct {
		ItemID   string `json:"itemId"`
		Kind     string `json:"kind"`
		Tool     string `json:"tool,omitempty"`
		Status   string `json:"status"`
		Resource string `json:"resource,omitempty"`
		Failure  string `json:"failure,omitempty"`
	}
	var records []record
	start := 0
	for i, it := range items {
		if it.Kind == protocol.ItemContextCompaction {
			start = i + 1
		}
	}
	bytesUsed := len(historyEvidenceLabel) + 64
	truncated := false
	for i := len(items) - 1; i >= start; i-- {
		it := items[i]
		if it.Kind != protocol.ItemToolCall && it.Kind != protocol.ItemFileChange {
			continue
		}
		r := record{ItemID: historicalSafeText(it.ID, 128), Kind: it.Kind, Status: it.Status}
		if it.Tool != nil {
			r.Tool = historicalSafeText(it.Tool.Name, 128)
			r.Failure = historicalSafeText(it.Tool.Error, 256)
			var resource struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(it.Tool.Args, &resource)
			r.Resource = historicalSafeText(resource.Path, 256)
		}
		body, _ := json.Marshal(r)
		if bytesUsed+len(body)+1 > maxBytes {
			truncated = true
			break
		}
		bytesUsed += len(body) + 1
		records = append(records, r)
	}
	if len(records) == 0 {
		return llm.Message{}, false
	}
	raw, _ := json.Marshal(struct {
		Records   []record `json:"records"`
		Truncated bool     `json:"truncated"`
	}{records, truncated})
	text := historyEvidenceLabel + string(raw)
	if len(text) > maxBytes {
		return llm.Message{}, false
	}
	return llm.Text(llm.RoleUser, text), true
}

// Both history paths consume this same data boundary. The original items remain
// unchanged in storage; only the model-facing copy is transformed.
func historicalEvidenceItems(items []protocol.Item, currentTurn string) []protocol.Item {
	copyItems := append([]protocol.Item(nil), items...)
	var past []protocol.Item
	last := -1
	for i, it := range items {
		if it.TurnID == currentTurn || it.Status == protocol.ItemInProgress {
			continue
		}
		past = append(past, it)
		if it.Kind == protocol.ItemContextCompaction {
			last = -1
		}
		if it.Kind == protocol.ItemToolCall || it.Kind == protocol.ItemFileChange {
			last = i
			copyItems[i].Kind = ""
		}
	}
	if last >= 0 {
		if msg, ok := historicalEvidenceMessage(past, 12288); ok {
			copyItems[last].Kind = protocol.ItemInboundEvent
			copyItems[last].Text = msg.JoinedText()
		}
	}
	return copyItems
}
