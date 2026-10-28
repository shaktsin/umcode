package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/teststore"
)

type contStep struct {
	text      string
	tool      bool
	okTool    bool // a complete tool call
	reason    string
	output    int64
	reasoning int64
}

// contProvider replays steps in order and records every chat request.
type contProvider struct {
	outputLimitProvider
	mu    sync.Mutex
	steps []contStep
	reqs  []llm.Request
}

func (p *contProvider) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	i := len(p.reqs)
	p.reqs = append(p.reqs, req)
	step := p.steps[len(p.steps)-1]
	if i < len(p.steps) {
		step = p.steps[i]
	}
	ch := make(chan llm.Event, 4)
	ch <- llm.Event{Type: llm.EventTextDelta, Text: step.text}
	if step.tool {
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "partial", Name: "file__write", Args: json.RawMessage(`{"path":"never.txt","content":"unfinished`)}}
	}
	if step.okTool {
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "whole", Name: "file__write", Args: json.RawMessage(`{"path":"ok.txt","content":"done"}`)}}
	}
	ch <- llm.Event{Type: llm.EventDone, StopReason: step.reason, Usage: llm.Usage{Reported: true, InputTokens: 100, OutputTokens: step.output, ReasoningTokens: step.reasoning}}
	close(ch)
	return ch, nil
}

func runContinuation(t *testing.T, steps []contStep) (*contProvider, protocol.Turn, []protocol.Item) {
	t.Helper()
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider, cfg.LLM.Model = "claude", "deterministic"
	st, err := teststore.Open(t.Context(), filepath.Join(cfg.Home, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &contProvider{steps: steps}
	reg := llm.NewRegistry()
	reg.Register(p)
	e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Shutdown(context.Background()) })
	if _, err = e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "claude", Label: "test", Secret: "test", IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "continuation"})
	if err != nil {
		t.Fatal(err)
	}
	wait := make(chan protocol.Turn, 1)
	if _, err = e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "Implement the reader", Override: protocol.ModelSelection{Provider: "claude", Model: "deterministic", Complexity: protocol.ComplexityHigh}}, wait: wait}); err != nil {
		t.Fatal(err)
	}
	var finished protocol.Turn
	select {
	case finished = <-wait:
	case <-time.After(15 * time.Second):
		t.Fatal("turn did not finish")
	}
	items, err := st.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return p, finished, items
}

func TestContinuesAfterOutputLimit(t *testing.T) {
	for _, reason := range []string{"max_tokens", "length"} {
		t.Run(reason, func(t *testing.T) {
			p, turn, items := runContinuation(t, []contStep{
				{text: "first half ", reason: reason, output: 100},
				{text: "second half", okTool: true, reason: "tool_use", output: 50},
				{text: "all done", reason: "end_turn", output: 10},
			})
			if turn.Status != protocol.TurnCompleted {
				t.Fatalf("turn did not complete: %+v", turn)
			}
			if len(p.reqs) != 3 {
				t.Fatalf("calls = %d, want 3", len(p.reqs))
			}
			msgs := p.reqs[1].Messages
			last, prev := msgs[len(msgs)-1], msgs[len(msgs)-2]
			if last.Role != llm.RoleUser || last.JoinedText() != "Continue from where you stopped." {
				t.Fatalf("missing nudge: %+v", last)
			}
			if prev.Role != llm.RoleAssistant || prev.JoinedText() != "first half " {
				t.Fatalf("partial text not kept: %+v", prev)
			}
			var saved []protocol.Item
			for _, it := range items {
				if it.Kind == protocol.ItemAgentMessage && (it.Text == "first half " || it.Text == "second half") {
					saved = append(saved, it)
				}
			}
			if len(saved) != 2 || saved[0].Text != "first half " || saved[1].Text != "second half" {
				t.Fatalf("saved items = %+v", saved)
			}
			for _, it := range saved {
				if it.Status != protocol.ItemCompleted || len(it.Data) != 0 {
					t.Fatalf("recovered attempt shown as interrupted: %+v", it)
				}
			}
			// The round after the tool call sees partial and final text once, no nudge.
			history := p.reqs[2].Messages
			var joined []string
			for _, m := range history {
				if m.Role == llm.RoleAssistant {
					joined = append(joined, m.JoinedText())
				}
				if m.JoinedText() == "Continue from where you stopped." && m.Role == llm.RoleUser {
					t.Fatalf("nudge leaked into later history: %+v", history)
				}
			}
			if got := strings.Join(joined, "|"); strings.Count(got, "first half ") != 1 || strings.Count(got, "second half") != 1 {
				t.Fatalf("history duplicated or lost text: %q", got)
			}
		})
	}
}

func TestContinuationStepsDownThinking(t *testing.T) {
	p, turn, _ := runContinuation(t, []contStep{
		{text: "thinking hard", reason: "max_tokens", output: 1000, reasoning: 900},
		{text: "light", reason: "max_tokens", output: 1000, reasoning: 100},
		{text: "done", reason: "end_turn", output: 10},
	})
	if turn.Status != protocol.TurnCompleted || len(p.reqs) != 3 {
		t.Fatalf("status %s, calls %d", turn.Status, len(p.reqs))
	}
	first, second, third := p.reqs[0].Reasoning, p.reqs[1].Reasoning, p.reqs[2].Reasoning
	if first == "" || second != lowerLevel(first) {
		t.Fatalf("reasoning-heavy cut did not lower the level: %q -> %q", first, second)
	}
	if third != second {
		t.Fatalf("answer-heavy cut changed the level: %q -> %q", second, third)
	}
}

func TestContinuationDropsPartialToolCall(t *testing.T) {
	p, turn, items := runContinuation(t, []contStep{
		{text: "writing ", tool: true, reason: "max_tokens", output: 100},
		{text: "finished", okTool: true, reason: "tool_use", output: 50},
		{text: "bye", reason: "end_turn", output: 5},
	})
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn did not complete: %+v", turn)
	}
	for _, m := range p.reqs[1].Messages {
		for _, call := range m.ToolCalls {
			t.Fatalf("incomplete tool call replayed: %+v", call)
		}
	}
	calls := 0
	for _, it := range items {
		if it.Kind == protocol.ItemToolCall {
			calls++
			if it.Tool == nil || it.Tool.CallID != "whole" {
				t.Fatalf("wrong tool call executed: %+v", it)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("tool calls executed = %d, want exactly the complete one", calls)
	}
}

func TestContinuationGivesUpAfterTwo(t *testing.T) {
	p, turn, items := runContinuation(t, []contStep{{text: "more ", reason: "max_tokens", output: 100}})
	if len(p.reqs) != 3 {
		t.Fatalf("calls = %d, want 3", len(p.reqs))
	}
	if turn.Status != protocol.TurnFailed || !strings.Contains(turn.Error, "after automatic continuation attempts") {
		t.Fatalf("turn = %+v", turn)
	}
	var statuses []string
	for _, it := range items {
		if it.Kind == protocol.ItemAgentMessage && it.Text == "more " {
			statuses = append(statuses, string(it.Status))
		}
	}
	if strings.Join(statuses, ",") != "completed,completed,failed" {
		t.Fatalf("item statuses = %v", statuses)
	}
}

// callModel itself returns an error that wraps errModelOutputLimit, and with a
// reduced working context it never stores the continuation messages.
func TestContinuationKeepsWorkingContextClean(t *testing.T) {
	e, turn := liveFixture(t)
	p := &budgetProvider{eventsFor: func(n int) []llm.Event {
		return []llm.Event{{Type: llm.EventTextDelta, Text: "part "}, {Type: llm.EventDone, StopReason: "max_tokens", Usage: llm.Usage{Reported: true, InputTokens: 100, OutputTokens: 20}}}
	}}
	e.LLMs.Register(p)
	cred, err := e.Creds.Resolve(t.Context(), "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	res := resolved{sel: protocol.ModelSelection{Provider: "claude", Model: "deterministic"}, cred: cred, meta: models.Meta{ContextWindow: 200000}, contextReduced: true}
	req := llm.Request{Model: res.sel.Model, Messages: []llm.Message{{Role: llm.RoleUser, Parts: textParts("work")}}, MaxTokens: 1024}
	budget := newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: 5, MaxTokens: 100000, MaxCostUSD: 10, MaxToolRounds: 10})
	_, err = e.callModel(t.Context(), t.Context(), turn, &res, req, "chat", budget)
	if !errors.Is(err, errModelOutputLimit) || len(p.requests) != 3 {
		t.Fatalf("err %v, calls %d", err, len(p.requests))
	}
	if len(p.requests[2].Messages) != 5 {
		t.Fatalf("final request history = %d messages, want 5", len(p.requests[2].Messages))
	}
	if len(res.workingMessages) != 1 || res.workingMessages[0].JoinedText() != "work" {
		t.Fatalf("working context polluted: %+v", res.workingMessages)
	}
	// Non-chat roles keep today's single attempt.
	p.requests = nil
	_, err = e.callModel(t.Context(), t.Context(), turn, &res, req, "title", budget)
	if !errors.Is(err, errModelOutputLimit) || len(p.requests) != 1 {
		t.Fatalf("title role continued: err %v, calls %d", err, len(p.requests))
	}
}

func TestLowerLevel(t *testing.T) {
	for in, want := range map[string]string{"high": "medium", "medium": "low", "low": "off", "off": "off", "": "", "xhigh": "xhigh"} {
		if got := lowerLevel(in); got != want {
			t.Errorf("lowerLevel(%q) = %q, want %q", in, got, want)
		}
	}
}
