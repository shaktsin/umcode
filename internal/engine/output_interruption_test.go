package engine

import (
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestLengthStopDoesNotInventContextCause(t *testing.T) {
	r := requestBudgetReport{Window: 32768, ReservedOutput: 4096}
	if got := classifyOutputInterruption("length", llm.Usage{Reported: true, InputTokens: 1000, OutputTokens: 20}, r, false); got != interruptionOutput {
		t.Fatal("length with headroom must be the output cap:", got)
	}
	if got := classifyOutputInterruption("LENGTH", llm.Usage{}, r, false); got != interruptionOutput {
		t.Fatal("length with unreported usage must be the output cap:", got)
	}
	if got := classifyOutputInterruption("length", llm.Usage{Reported: true, InputTokens: 30000, OutputTokens: 2768}, r, false); got != interruptionContext {
		t.Fatal("length with the window consumed must be context:", got)
	}
	if got := classifyOutputInterruption("length", llm.Usage{Reported: true, InputTokens: 30952, OutputTokens: 1816}, r, false); got != interruptionContext {
		t.Fatal(got)
	}
	if got := classifyOutputInterruption("length", llm.Usage{Reported: true, InputTokens: 1000, OutputTokens: 4096}, r, false); got != interruptionOutput {
		t.Fatal(got)
	}
}
func TestContextOverflowHasOneReductionRetry(t *testing.T) {
	for _, alwaysFail := range []bool{false, true} {
		e, turn := liveFixture(t)
		p := &budgetProvider{eventsFor: func(n int) []llm.Event {
			if n == 1 || alwaysFail {
				return []llm.Event{{Type: llm.EventError, Err: &llm.Error{Status: 400, Body: "maximum context length exceeded"}}}
			}
			return []llm.Event{{Type: llm.EventDone, StopReason: "stop", Usage: llm.Usage{Reported: true, InputTokens: 100, OutputTokens: 20}}}
		}}
		e.LLMs.Register(p)
		cred, err := e.Creds.Resolve(t.Context(), "claude", "")
		if err != nil {
			t.Fatal(err)
		}
		res := resolved{sel: protocol.ModelSelection{Provider: "claude", Model: "deterministic"}, cred: cred, meta: models.Meta{ContextWindow: 200000}}
		req := llm.Request{Model: res.sel.Model, Messages: []llm.Message{{Role: llm.RoleTool, ToolCallID: "already_done", Result: strings.Repeat("result ", 2000)}}, MaxTokens: 1024}
		_, err = e.callModel(t.Context(), t.Context(), turn, &res, req, "chat", newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: 5, MaxTokens: 100000, MaxCostUSD: 10, MaxToolRounds: 10}))
		if (err != nil) != alwaysFail || len(p.requests) != 2 {
			t.Fatalf("bounded retry failed: %d %v", len(p.requests), err)
		}
		if len(p.requests[1].Messages[0].Result) >= len(req.Messages[0].Result) || p.requests[1].Messages[0].ToolCallID != "already_done" {
			t.Fatal("lost completed result or reduction")
		}
	}
}
func TestIncompleteCallsNeverExecute(t *testing.T) {
	TestOutputLimitDoesNotCompleteOrExecutePartialTools(t)
}
func TestInterruptedRetryDoesNotReplayCompletedCalls(t *testing.T) {
	TestContextOverflowHasOneReductionRetry(t)
}

func TestInterruptedTextRetainedAsPartialHistory(t *testing.T) {
	msgs := historyMessages([]protocol.Item{{TurnID: "old", Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "edit"}, {TurnID: "old", Kind: protocol.ItemAgentMessage, Status: protocol.ItemFailed, Text: "I was about to edit", Data: []byte(`{"interrupted":true}`)}}, "new", 0)
	if len(msgs) != 2 || !strings.Contains(msgs[1].JoinedText(), "Interrupted partial") {
		t.Fatal("partial output lost")
	}
}
