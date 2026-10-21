package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// This script replaces only the external model; real execution, persistence,
// sandboxing and turn admission remain exercised by the runtime fixture.
type modelDrivenScript struct {
	requests []llm.Request
	failRead string
	answer   string
}

func (*modelDrivenScript) ID() string { return "claude" }
func (*modelDrivenScript) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *modelDrivenScript) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	p.requests = append(p.requests, req)
	ch := make(chan llm.Event, 3)
	if p.failRead != "" && len(p.requests) == 1 {
		args, _ := json.Marshal(map[string]string{"path": p.failRead})
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "read-missing", Name: "file__read", Args: args}}
	} else {
		ch <- llm.Event{Type: llm.EventTextDelta, Text: p.answer}
	}
	ch <- llm.Event{Type: llm.EventDone, StopReason: "end_turn", Usage: llm.Usage{Reported: true, InputTokens: 20, OutputTokens: 20}}
	close(ch)
	return ch, nil
}
func modelDrivenAnswer(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn) string {
	t.Helper()
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var answer string
	for _, it := range items {
		if it.TurnID == turn.ID && it.Kind == protocol.ItemAgentMessage {
			answer = it.Text
		}
	}
	return answer
}
func TestModelDrivenTurnSkipsIntentClassifier(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	p := &modelDrivenScript{answer: "Here is the explanation you requested."}
	e.LLMs.Register(p)
	turn := runTaskRuntimeTurn(t, e, th, "Explain the available approaches")
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn failed before ordinary model execution: %+v", turn)
	}
	if len(p.requests) != 1 {
		t.Fatalf("extra classification or correction calls: %d", len(p.requests))
	}
	if len(p.requests[0].Tools) == 1 && p.requests[0].Tools[0].Name == "task_intent" {
		t.Fatal("request reached classifier instead of coding model")
	}
}
func TestModelDrivenTurnPreservesAssistantResponse(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	const answer = "I cannot complete this because the command failed."
	p := &modelDrivenScript{answer: answer}
	e.LLMs.Register(p)
	turn := runTaskRuntimeTurn(t, e, th, "Explain why the previous command failed")
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("explanation rejected: %+v", turn)
	}
	if got := modelDrivenAnswer(t, e, th, turn); got != answer {
		t.Fatalf("model answer replaced: %q", got)
	}
}
func TestModelDrivenTurnReturnsToolFailureToModel(t *testing.T) {
	for _, path := range []string{"missing-file.txt", "../outside.txt"} {
		t.Run(path, func(t *testing.T) {
			e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
			p := &modelDrivenScript{failRead: path, answer: "The requested read failed; I have not changed any files."}
			e.LLMs.Register(p)
			turn := runTaskRuntimeTurn(t, e, th, "Read the named file and explain any failure")
			if turn.Status != protocol.TurnCompleted {
				t.Fatalf("model could not explain tool failure: %+v", turn)
			}
			if len(p.requests) != 2 {
				t.Fatalf("expected tool round and response, got %d model calls", len(p.requests))
			}
			found := false
			for _, m := range p.requests[1].Messages {
				if m.Role == llm.RoleTool && m.IsError {
					found = true
				}
			}
			if !found {
				t.Fatal("actual tool failure was not returned to the model")
			}
			items, err := e.Store.ListItems(t.Context(), th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			recorded := false
			for _, it := range items {
				if it.TurnID == turn.ID && it.Tool != nil && it.Tool.Error != "" {
					recorded = true
					if it.Status != protocol.ItemFailed && it.Status != protocol.ItemDenied {
						t.Fatalf("failure relabeled: %+v", it)
					}
				}
			}
			if !recorded {
				t.Fatal("actual tool failure disappeared from persisted history")
			}
		})
	}
}
func TestModelDrivenTurnDoesNotRequireWorkGraphCompletion(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	p := &modelDrivenScript{answer: "The plan remains open; this is a discussion of the next step."}
	e.LLMs.Register(p)
	turn := runTaskRuntimeTurn(t, e, th, "Discuss the next step without changing any files")
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("open work graph blocked discussion: %+v", turn)
	}
	if got := modelDrivenAnswer(t, e, th, turn); !strings.Contains(got, "plan remains open") {
		t.Fatalf("discussion rewritten: %q", got)
	}
}

func TestModelDrivenTurnCanDiscussPriorDeniedAction(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	p := &modelDrivenScript{failRead: "../outside.txt", answer: "Access was denied."}
	e.LLMs.Register(p)
	runTaskRuntimeTurn(t, e, th, "Read the requested file")
	p.requests = nil
	p.failRead = ""
	p.answer = "The prior action was denied; I will explain the limitation."
	turn := runTaskRuntimeTurn(t, e, th, "retry")
	if turn.Status != protocol.TurnCompleted || len(p.requests) != 1 {
		t.Fatalf("prior tool denial blocked model discussion: status=%s calls=%d error=%s", turn.Status, len(p.requests), turn.Error)
	}
}

func (*modelDrivenScript) RuntimeCapacity(context.Context, llm.Credential, string) (llm.RuntimeCapacity, error) {
	return llm.RuntimeCapacity{ContextWindow: 200000}, nil
}
