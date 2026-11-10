package engine

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
)

type localRecorder struct {
	mu   sync.Mutex
	reqs []llm.Request
}

func (*localRecorder) ID() string { return "openai_compatible" }
func (*localRecorder) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"qwen3-coder"}, nil
}
func (p *localRecorder) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	ch := make(chan llm.Event, 2)
	ch <- llm.Event{Type: llm.EventTextDelta, Text: "ok summary"}
	ch <- llm.Event{Type: llm.EventDone, StopReason: "stop", Usage: llm.Usage{Reported: true, InputTokens: 10, OutputTokens: 5}}
	close(ch)
	return ch, nil
}

func localReasoningFixture(t *testing.T) (*Engine, protocol.Thread, *localRecorder) {
	t.Helper()
	e, th, _, _ := delegationFixture(t)
	rec := &localRecorder{}
	e.LLMs.Register(rec)
	srv := httptest.NewServer(nil)
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
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello local", Override: sel})
	return e, th, rec
}

func assertNoReasoning(t *testing.T, label string, reqs []llm.Request) {
	t.Helper()
	if len(reqs) == 0 {
		t.Fatalf("%s: local provider received no request", label)
	}
	for _, r := range reqs {
		if r.Reasoning != "" || r.ReasoningStyle != "" || len(r.TemplateKwargs) != 0 {
			t.Fatalf("%s: undetected local model got reasoning fields: %+v", label, r)
		}
	}
}

func TestLocalTurnSendsNoReasoningWhenUndetected(t *testing.T) {
	_, _, rec := localReasoningFixture(t)
	assertNoReasoning(t, "turn", rec.reqs)
}

func TestLocalCompactionSendsNoReasoningWhenUndetected(t *testing.T) {
	e, th, rec := localReasoningFixture(t)
	before := len(rec.reqs)
	if _, err := e.SetThreadSettings(t.Context(), protocol.ThreadSetSettingsParams{ThreadID: th.ID, Settings: protocol.ModelSelection{Provider: "openai_compatible", Model: "qwen3-coder", Complexity: protocol.ComplexityHigh}}); err != nil {
		t.Fatal(err)
	}
	if err := e.CompactThread(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) <= before {
		t.Fatal("compaction did not reach the local provider")
	}
	assertNoReasoning(t, "compaction", rec.reqs)
}
