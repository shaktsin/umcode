package server_test

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// Keep private interpretation separate from existing public execution scripts.
// Acceptance tests explicitly select a mutation/inspection contract.
func serverIntentReply(req llm.Request, kind, path string) (<-chan llm.Event, bool) {
	if !strings.HasPrefix(req.System, "Privately classify") {
		return nil, false
	}
	var input struct {
		TaskID           string `json:"taskId"`
		ExpectedRevision int    `json:"expectedRevision"`
		OriginItemID     string `json:"originItemId"`
		Request          string `json:"request"`
	}
	_ = json.Unmarshal([]byte(req.Messages[0].JoinedText()), &input)
	if kind == "" {
		kind = "explanation"
	}
	predicate := "answer"
	resource := json.RawMessage(`{}`)
	if kind != "explanation" {
		predicate = "file_content"
		if kind == "inspection" {
			predicate = "exists"
		}
		resource, _ = json.Marshal(protocol.TaskResource{Type: "file", Path: path})
	}
	raw, _ := json.Marshal(protocol.TaskIntentProposal{TaskID: input.TaskID, ExpectedRevision: input.ExpectedRevision, OriginItemID: input.OriginItemID, Relation: "continue", Expectations: []protocol.TaskExpectationProposal{{Source: protocol.TaskSourceSpan{ItemID: input.OriginItemID, Start: 0, End: utf8.RuneCountInString(input.Request)}, Kind: kind, Predicate: predicate, Resource: resource}}})
	out := make(chan llm.Event, 2)
	out <- llm.Event{Type: llm.EventTextDelta, Text: string(raw)}
	out <- llm.Event{Type: llm.EventDone, StopReason: "end_turn", Usage: llm.Usage{Reported: true, InputTokens: 5, OutputTokens: 5}}
	close(out)
	return out, true
}
