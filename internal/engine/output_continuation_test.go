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
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/teststore"
)

type contStep struct {
	text      string
	tool      bool
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
	ch := make(chan llm.Event, 3)
	ch <- llm.Event{Type: llm.EventTextDelta, Text: step.text}
	if step.tool {
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "partial", Name: "file__write", Args: json.RawMessage(`{"path":"never.txt","content":"unfinished`)}}
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
	p, turn, items := runContinuation(t, []contStep{
		{text: "first half ", reason: "max_tokens", output: 100},
		{text: "second half", reason: "end_turn", output: 50},
	})
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn did not complete: %+v", turn)
	}
	if len(p.reqs) != 2 {
		t.Fatalf("calls = %d, want 2", len(p.reqs))
	}
	msgs := p.reqs[1].Messages
	last, prev := msgs[len(msgs)-1], msgs[len(msgs)-2]
	if last.Role != llm.RoleUser || last.JoinedText() != "Continue from where you stopped." {
		t.Fatalf("missing nudge: %+v", last)
	}
	if prev.Role != llm.RoleAssistant || prev.JoinedText() != "first half " {
		t.Fatalf("partial text not kept: %+v", prev)
	}
	var saved []string
	for _, it := range items {
		if it.Kind == protocol.ItemAgentMessage {
			saved = append(saved, it.Text)
		}
	}
	if strings.Join(saved, "|") != "first half |second half" {
		t.Fatalf("saved items = %q", saved)
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
		{text: "finished", reason: "end_turn", output: 50},
	})
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn did not complete: %+v", turn)
	}
	for _, m := range p.reqs[1].Messages {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("incomplete tool call replayed: %+v", m)
		}
	}
	for _, it := range items {
		if it.Kind == protocol.ItemToolCall {
			t.Fatalf("incomplete tool call executed: %+v", it)
		}
	}
}

func TestContinuationGivesUpAfterTwo(t *testing.T) {
	p, turn, _ := runContinuation(t, []contStep{{text: "more ", reason: "max_tokens", output: 100}})
	if len(p.reqs) != 3 {
		t.Fatalf("calls = %d, want 3", len(p.reqs))
	}
	if turn.Status != protocol.TurnFailed || !strings.Contains(turn.Error, "after automatic continuation attempts") {
		t.Fatalf("turn = %+v", turn)
	}
	if !errors.Is(&outputInterruptionError{Kind: interruptionOutput}, errModelOutputLimit) {
		t.Fatal("interruption no longer wraps errModelOutputLimit")
	}
}

func TestLowerLevel(t *testing.T) {
	for in, want := range map[string]string{"high": "medium", "medium": "low", "low": "off", "off": "off", "": "", "xhigh": "xhigh"} {
		if got := lowerLevel(in); got != want {
			t.Errorf("lowerLevel(%q) = %q, want %q", in, got, want)
		}
	}
}
