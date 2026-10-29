package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// stepProvider answers each chat request (no output cap) from a script indexed
// by the call number. Requests with a fixed auxiliary cap (compaction, task intent) are
// recorded and answered with a short text.
type stepProvider struct {
	id   string
	mu   sync.Mutex
	reqs []llm.Request
	chat int
	step func(n int, req llm.Request) []llm.Event
}

func (p *stepProvider) ID() string { return p.id }
func (p *stepProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"qwen3-coder"}, nil
}
func (p *stepProvider) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	n := -1
	if !isAuxCap(req.MaxTokens) {
		n = p.chat
		p.chat++
	}
	p.mu.Unlock()
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	events := []llm.Event{{Type: llm.EventTextDelta, Text: "summary"}, {Type: llm.EventDone, StopReason: "stop", Usage: llm.Usage{Reported: true, InputTokens: 10, OutputTokens: 5}}}
	if n >= 0 {
		events = p.step(n, req)
	}
	ch := make(chan llm.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (p *stepProvider) chatReqs() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []llm.Request
	for _, r := range p.reqs {
		if !isAuxCap(r.MaxTokens) {
			out = append(out, r)
		}
	}
	return out
}

func (p *stepProvider) capped(max int) []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []llm.Request
	for _, r := range p.reqs {
		if r.MaxTokens == max {
			out = append(out, r)
		}
	}
	return out
}

func isAuxCap(n int) bool { return n == compactionMaxOutput || n == taskIntentMaxOutput }

func toolStep(name string) []llm.Event {
	return []llm.Event{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "c-" + name, Name: name, Args: json.RawMessage(`{"path":"."}`)}},
		{Type: llm.EventDone, StopReason: "tool_use", Usage: llm.Usage{Reported: true, InputTokens: 10, OutputTokens: 5}},
	}
}

func textStep(text string) []llm.Event {
	return []llm.Event{{Type: llm.EventTextDelta, Text: text}, {Type: llm.EventDone, StopReason: "stop", Usage: llm.Usage{Reported: true, InputTokens: 10, OutputTokens: 5}}}
}

func localStepFixture(t *testing.T, level protocol.Complexity, step func(int, llm.Request) []llm.Event) (*Engine, protocol.Thread, *stepProvider, protocol.ModelSelection) {
	t.Helper()
	e, th, _, sel := scriptedLocalFixture(t, qwenProps, false)
	p := &stepProvider{id: "openai_compatible", step: step}
	e.LLMs.Register(p)
	sel.Complexity = level
	return e, th, p, sel
}

func TestLocalPerRoundEffort(t *testing.T) {
	t.Run("high", func(t *testing.T) {
		script := [][]llm.Event{toolStep("file__list"), toolStep("no_such_tool"), toolStep("file__list"), textStep("done")}
		e, th, p, sel := localStepFixture(t, protocol.ComplexityHigh, func(n int, _ llm.Request) []llm.Event { return script[n] })
		delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "look around", Override: sel})
		reqs := p.chatReqs()
		if len(reqs) != 4 {
			t.Fatalf("chat requests = %d, want 4", len(reqs))
		}
		for i, want := range []string{"xhigh", "medium", "xhigh", "medium"} {
			kw := reqs[i].TemplateKwargs
			if kw["enable_thinking"] != true || kw["reasoning_effort"] != want {
				t.Fatalf("round %d kwargs = %+v, want effort %s", i, kw, want)
			}
		}
	})
	t.Run("low drops to off", func(t *testing.T) {
		script := [][]llm.Event{toolStep("file__list"), textStep("done")}
		e, th, p, sel := localStepFixture(t, protocol.ComplexityLow, func(n int, _ llm.Request) []llm.Event { return script[n] })
		delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "look around", Override: sel})
		reqs := p.chatReqs()
		if len(reqs) != 2 {
			t.Fatalf("chat requests = %d, want 2", len(reqs))
		}
		if kw := reqs[0].TemplateKwargs; kw["enable_thinking"] != true || kw["reasoning_effort"] != "low" {
			t.Fatalf("round 0 kwargs = %+v", kw)
		}
		kw := reqs[1].TemplateKwargs
		if kw["enable_thinking"] != false {
			t.Fatalf("round 1 kwargs = %+v, want thinking off", kw)
		}
		if _, has := kw["reasoning_effort"]; has {
			t.Fatalf("round 1 still sends reasoning_effort: %+v", kw)
		}
	})
	t.Run("self-adapting keeps the level", func(t *testing.T) {
		e, th, _, _ := delegationFixture(t)
		script := [][]llm.Event{toolStep("file__list"), toolStep("no_such_tool"), toolStep("file__list"), textStep("done")}
		p := &stepProvider{id: "claude", step: func(n int, _ llm.Request) []llm.Event { return script[n] }}
		e.LLMs.Register(p)
		sel := protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-4-6", Complexity: protocol.ComplexityHigh}
		delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "look around", Override: sel})
		reqs := p.chatReqs()
		if len(reqs) != 4 {
			t.Fatalf("chat requests = %d, want 4", len(reqs))
		}
		for i, r := range reqs {
			if r.Reasoning != "high" {
				t.Fatalf("round %d reasoning = %q, want high", i, r.Reasoning)
			}
		}
	})
}

// Reasoning tokens are often unreported; streamed reasoning text must still
// make a continuation step the level down.
func TestContinuationLowersLevelFromStreamedReasoning(t *testing.T) {
	e, th, p, sel := localStepFixture(t, protocol.ComplexityHigh, func(n int, _ llm.Request) []llm.Event {
		if n == 0 {
			return []llm.Event{
				{Type: llm.EventReasoningDelta, Text: strings.Repeat("thinking ", 600)},
				{Type: llm.EventTextDelta, Text: "partial"},
				{Type: llm.EventDone, StopReason: "length", Usage: llm.Usage{Reported: true, InputTokens: 100, OutputTokens: 100}},
			}
		}
		return textStep("the rest")
	})
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "explain", Override: sel})
	reqs := p.chatReqs()
	if len(reqs) != 2 {
		t.Fatalf("chat requests = %d, want 2", len(reqs))
	}
	if got := reqs[0].TemplateKwargs["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("first effort = %v", got)
	}
	if got := reqs[1].TemplateKwargs["reasoning_effort"]; got != "medium" {
		t.Fatalf("continuation effort = %v, want medium", got)
	}
}

func TestLocalAuxiliaryCallsDoNotThink(t *testing.T) {
	e, th, p, sel := localStepFixture(t, protocol.ComplexityHigh, func(int, llm.Request) []llm.Event { return textStep("ok") })
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello local", Override: sel})
	if _, err := e.SetThreadSettings(t.Context(), protocol.ThreadSetSettingsParams{ThreadID: th.ID, Settings: sel}); err != nil {
		t.Fatal(err)
	}
	if err := e.CompactThread(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	turn := protocol.Turn{ID: "aux-turn", ThreadID: th.ID, Status: "running"}
	if err := e.Store.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	res, err := e.resolveSelection(t.Context(), th, sel, "classify this", 0)
	if err != nil {
		t.Fatal(err)
	}
	intent := llm.Request{Model: sel.Model, MaxTokens: taskIntentMaxOutput, System: "Classify.", Messages: []llm.Message{llm.Text(llm.RoleUser, "{}")}}
	if _, err = e.callModel(t.Context(), t.Context(), turn, &res, intent, "task_intent", newTurnBudget(res.limits)); err != nil {
		t.Fatal(err)
	}
	for name, max := range map[string]int{"task intent": taskIntentMaxOutput, "compaction": compactionMaxOutput} {
		reqs := p.capped(max)
		if len(reqs) == 0 {
			t.Fatalf("%s call never reached the provider", name)
		}
		for _, r := range reqs {
			if r.TemplateKwargs["enable_thinking"] != false {
				t.Fatalf("%s kwargs = %+v, want thinking off", name, r.TemplateKwargs)
			}
			if _, has := r.TemplateKwargs["reasoning_effort"]; has {
				t.Fatalf("%s sends reasoning_effort: %+v", name, r.TemplateKwargs)
			}
		}
	}
}

func TestCompactionRetriesWithoutRejectedKwargs(t *testing.T) {
	e, th, p, sel := localStepFixture(t, protocol.ComplexityHigh, func(int, llm.Request) []llm.Event { return textStep("ok") })
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello local", Override: sel})
	if _, err := e.SetThreadSettings(t.Context(), protocol.ThreadSetSettingsParams{ThreadID: th.ID, Settings: sel}); err != nil {
		t.Fatal(err)
	}
	rejecting := &rejectKwargsProvider{stepProvider: p}
	e.LLMs.Register(rejecting)
	if err := e.CompactThread(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	reqs := p.capped(compactionMaxOutput)
	if len(reqs) != 2 || len(reqs[0].TemplateKwargs) == 0 || len(reqs[1].TemplateKwargs) != 0 {
		t.Fatalf("compaction requests = %+v", reqs)
	}
}

// rejectKwargsProvider fails the first request that carries template kwargs.
type rejectKwargsProvider struct {
	*stepProvider
	rejected bool
}

func (r *rejectKwargsProvider) Stream(ctx context.Context, c llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if !r.rejected && req.MaxTokens == compactionMaxOutput && len(req.TemplateKwargs) > 0 {
		r.rejected = true
		r.stepProvider.mu.Lock()
		r.stepProvider.reqs = append(r.stepProvider.reqs, req)
		r.stepProvider.mu.Unlock()
		return nil, &llm.Error{Provider: "openai_compatible", Status: 400, Body: `{"error":"unknown field chat_template_kwargs"}`}
	}
	return r.stepProvider.Stream(ctx, c, req)
}

func TestSetComplexityDefaultsAppliesNewMaxOutputImmediately(t *testing.T) {
	e, _, _, sel := scriptedLocalFixture(t, "", false)
	cred, err := e.Creds.Resolve(t.Context(), "openai_compatible", sel.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	res := resolved{sel: sel, cred: cred}
	res.sel.Model = "unlisted-model"
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.MaxOutput != 32768 {
		t.Fatalf("initial capacity = %+v, %v", got, err)
	}
	d := e.ComplexityDefaults()
	d.DefaultMaxOutputTokens = 4096
	if _, err = e.SetComplexityDefaults(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	got, err = e.resolveCapacity(t.Context(), res)
	if err != nil || got.MaxOutput != 4096 {
		t.Fatalf("capacity after change = %+v, %v; want 4096", got, err)
	}
}

func TestSetComplexityDefaultsAcceptsAndPersistsNoLimit(t *testing.T) {
	e, _, _, _ := scriptedLocalFixture(t, "", false)
	d := e.ComplexityDefaults()
	d.Limits.MaxDurationMinutes = protocol.NoLimit
	d.Limits.MaxTokens = protocol.NoLimit
	got, err := e.SetComplexityDefaults(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits.MaxDurationMinutes != protocol.NoLimit || got.Limits.MaxTokens != protocol.NoLimit {
		t.Fatalf("limits = %+v", got.Limits)
	}
	var saved protocol.ComplexityDefaults
	if err := e.Store.GetSetting(t.Context(), complexitySettingKey, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Limits.MaxDurationMinutes != protocol.NoLimit || saved.Limits.MaxTokens != protocol.NoLimit {
		t.Fatalf("persisted = %+v", saved.Limits)
	}
	if err := e.loadComplexity(t.Context()); err != nil {
		t.Fatal(err)
	}
	if l := e.ComplexityDefaults().Limits; l.MaxDurationMinutes != protocol.NoLimit || l.MaxTokens != protocol.NoLimit {
		t.Fatalf("reloaded = %+v", l)
	}
	d.Limits.MaxDurationMinutes = -2
	if _, err := e.SetComplexityDefaults(t.Context(), d); err == nil {
		t.Fatal("-2 should be rejected")
	}
}
