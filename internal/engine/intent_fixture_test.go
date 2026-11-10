package engine

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// Legacy provider scripts describe execution, not the new private interpretation
// phase. Keep that phase explicit and outside their execution counters. File
// recovery fixtures request a real notes.txt mutation; dialogue/routing fixtures
// retain an explanation contract and their existing operation-level assertions.
func testIntentReply(req llm.Request, kind, predicate, path string) (<-chan llm.Event, bool) {
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
	resource := json.RawMessage(`{}`)
	if path != "" {
		resource, _ = json.Marshal(protocol.TaskResource{Type: "file", Path: path})
	}
	raw, _ := json.Marshal(protocol.TaskIntentProposal{TaskID: input.TaskID, ExpectedRevision: input.ExpectedRevision, OriginItemID: input.OriginItemID, Relation: "continue", Expectations: []protocol.TaskExpectationProposal{{Source: protocol.TaskSourceSpan{ItemID: input.OriginItemID, Start: 0, End: utf8.RuneCountInString(input.Request)}, Kind: kind, Predicate: predicate, Resource: resource}}})
	out := make(chan llm.Event, 2)
	out <- llm.Event{Type: llm.EventTextDelta, Text: string(raw)}
	out <- llm.Event{Type: llm.EventDone, StopReason: "end_turn", Usage: llm.Usage{Reported: true, InputTokens: 5, OutputTokens: 5}}
	close(out)
	return out, true
}
