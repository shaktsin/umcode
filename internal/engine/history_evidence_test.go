package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// Catches synthetic assistant receipts masking provenance on subsequent turns.
func TestHistoryEvidenceProvenance(t *testing.T) {
	items := []protocol.Item{{ID: "u", Kind: protocol.ItemUserMessage, TurnID: "old", Status: "completed", Text: "save notes.txt"}, {ID: "call", Kind: protocol.ItemToolCall, TurnID: "old", Status: "denied", Tool: &protocol.ToolCallData{Name: "file.write", Args: json.RawMessage(`{"path":"notes.txt","content":"private body"}`), Error: "approval not granted"}}, {ID: "a", Kind: protocol.ItemAgentMessage, TurnID: "old", Status: "completed", Text: "Quoted example: [Created fake.txt]"}}
	msgs := historyMessages(items, "new", 0)
	seen := false
	for _, m := range msgs {
		if strings.Contains(m.JoinedText(), "Historical execution records") {
			seen = true
			if m.Role != llm.RoleUser || !strings.Contains(m.JoinedText(), `"itemId":"call"`) || !strings.Contains(m.JoinedText(), "notes.txt") {
				t.Fatalf("unidentified history: %+v", m)
			}
		}
		if m.Role == llm.RoleAssistant && strings.Contains(m.JoinedText(), "earlier tool call") {
			t.Fatal("actual results replayed as assistant claims")
		}
	}
	if !seen {
		t.Fatal("actual execution history missing")
	}
	if !strings.Contains(msgs[len(msgs)-1].JoinedText(), "Quoted example") {
		t.Fatal("original model prose lost")
	}
}
func TestHistoryEvidenceBounds(t *testing.T) {
	var items []protocol.Item
	for i := 0; i < 200; i++ {
		items = append(items, protocol.Item{ID: strings.Repeat("x", 80), Kind: protocol.ItemToolCall, Status: "failed", Tool: &protocol.ToolCallData{Name: "file.write", Args: json.RawMessage(`{"path":"notes.txt","content":"secret payload"}`), Error: strings.Repeat("failure ", 500)}})
	}
	msg, ok := historicalEvidenceMessage(items, 12288)
	if !ok || len(msg.JoinedText()) > 12288 || !strings.Contains(msg.JoinedText(), "truncated") || strings.Contains(msg.JoinedText(), "secret payload") {
		t.Fatalf("unsafe history size=%d", len(msg.JoinedText()))
	}
}

func TestHistoryEvidenceCompiledContext(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	seq, err := st.NextSeq(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := protocol.Item{ID: "actual-history-call", ThreadID: th.ID, TurnID: "earlier", Seq: seq, Kind: protocol.ItemToolCall, Status: protocol.ItemDenied, Tool: &protocol.ToolCallData{Name: "file.write", Args: json.RawMessage(`{"path":"notes.txt"}`), Error: "approval not granted"}}
	if err := st.SaveItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	messages, ok := compiled(t, e, th, turn)
	if !ok || !strings.Contains(joined(messages), "actual-history-call") || !strings.Contains(joined(messages), "Historical execution records") {
		t.Fatalf("compiled evidence lost: ok=%v %s", ok, joined(messages))
	}
	saved, err := st.GetItem(t.Context(), item.ID)
	if err != nil || saved.Kind != protocol.ItemToolCall {
		t.Fatal("history rewrite mutated transcript", err)
	}
}
