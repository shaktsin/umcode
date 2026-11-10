package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
)

const qwenProps = `{"chat_template":"{% if enable_thinking %}<think>{% endif %}{{ reasoning_effort }}{{ preserve_thinking }}"}`

// scriptedLocal records every chat request; failFirst makes the first one a
// 400 that names chat_template_kwargs.
type scriptedLocal struct {
	mu        sync.Mutex
	reqs      []llm.Request
	failFirst bool
	calls     int
}

func (*scriptedLocal) ID() string { return "openai_compatible" }
func (*scriptedLocal) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"qwen3-coder"}, nil
}
func (p *scriptedLocal) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.calls++
	fail := p.failFirst && p.calls == 1
	p.mu.Unlock()
	if fail {
		return nil, &llm.Error{Provider: "openai_compatible", Status: 400, Body: `{"error":"unknown field chat_template_kwargs"}`}
	}
	ch := make(chan llm.Event, 2)
	ch <- llm.Event{Type: llm.EventTextDelta, Text: "ok summary"}
	ch <- llm.Event{Type: llm.EventDone, StopReason: "stop", Usage: llm.Usage{Reported: true, InputTokens: 10, OutputTokens: 5}}
	close(ch)
	return ch, nil
}

func (p *scriptedLocal) snapshot() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.reqs...)
}

func scriptedLocalFixture(t *testing.T, props string, failFirst bool) (*Engine, protocol.Thread, *scriptedLocal, protocol.ModelSelection) {
	t.Helper()
	e, th, _, _ := delegationFixture(t)
	rec := &scriptedLocal{failFirst: failFirst}
	e.LLMs.Register(rec)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" && props != "" {
			fmt.Fprint(w, props)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	record, err := e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "openai_compatible", Secret: "local-token", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := models.EndpointIdentity("openai_compatible", record.BaseURL)
	window := 65536
	if err = e.Store.SetModelCapacityProfile(t.Context(), protocol.ModelCapacityProfile{Provider: "openai_compatible", EndpointID: id, Model: "qwen3-coder", Backend: "generic", ContextWindow: &window}); err != nil {
		t.Fatal(err)
	}
	sel := protocol.ModelSelection{Provider: "openai_compatible", Model: "qwen3-coder", CredentialID: record.ID, Complexity: protocol.ComplexityHigh}
	return e, th, rec, sel
}

func TestLocalTurnSendsTemplateKwargs(t *testing.T) {
	e, th, rec, sel := scriptedLocalFixture(t, qwenProps, false)
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello local", Override: sel})
	reqs := rec.snapshot()
	if len(reqs) == 0 {
		t.Fatal("no request reached the local provider")
	}
	for _, r := range reqs {
		if r.TemplateKwargs["enable_thinking"] != true || r.TemplateKwargs["reasoning_effort"] != "xhigh" || r.TemplateKwargs["preserve_thinking"] != false {
			t.Fatalf("template kwargs = %+v", r.TemplateKwargs)
		}
		if r.Reasoning != "" {
			t.Fatalf("legacy reasoning set for a template server: %q", r.Reasoning)
		}
	}
}

func TestLocalNoDetectionSendsNothing(t *testing.T) {
	e, th, rec, sel := scriptedLocalFixture(t, "", false)
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello local", Override: sel})
	assertNoReasoning(t, "turn", rec.snapshot())
}

func TestTemplateKwargs400RetriesOnce(t *testing.T) {
	e, th, rec, sel := scriptedLocalFixture(t, qwenProps, true)
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello local", Override: sel})
	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("want a failed call and one retry, got %d requests", len(reqs))
	}
	if len(reqs[0].TemplateKwargs) == 0 {
		t.Fatal("first call should carry the kwargs")
	}
	if len(reqs[1].TemplateKwargs) != 0 {
		t.Fatalf("retry still sent kwargs: %+v", reqs[1].TemplateKwargs)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "again", Override: sel})
	reqs = rec.snapshot()
	if len(reqs) < 3 {
		t.Fatalf("third turn sent no request: %d", len(reqs))
	}
	for _, r := range reqs[2:] {
		if len(r.TemplateKwargs) != 0 {
			t.Fatalf("later turn resent kwargs: %+v", r.TemplateKwargs)
		}
	}
}
