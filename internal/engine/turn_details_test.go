package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/teststore"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type turnDetailsProvider struct{ *memoryScriptProvider }

func (*turnDetailsProvider) ID() string { return "claude" }

func TestTurnDetailsFromAdmittedTurnAndReadDoesNotCallProvider(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default(home)
	cfg.LLM.Provider, cfg.LLM.Model = "claude", "deterministic"
	st, err := teststore.Open(t.Context(), filepath.Join(home, "details.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	provider := &turnDetailsProvider{memoryScriptProvider: &memoryScriptProvider{script: memoryAnswer}}
	registry := llm.NewRegistry()
	registry.Register(provider)
	e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: registry,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Shutdown(context.Background()) })
	if _, err := e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "claude", Label: "test", Secret: "test-secret", IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "details integration"})
	if err != nil {
		t.Fatal(err)
	}
	wait := make(chan protocol.Turn, 1)
	started, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "Tell me a short greeting.", Override: protocol.ModelSelection{Provider: "claude", Model: "deterministic", Complexity: protocol.ComplexityMedium}}, wait: wait})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case completed := <-wait:
		if completed.ID != started.ID || completed.Status != protocol.TurnCompleted {
			t.Fatalf("completed turn=%+v started=%+v", completed, started)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("admitted turn did not complete")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls=%d", len(provider.requests))
	}
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	details, err := e.TurnDetails(t.Context(), protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: started.ID})
	if err != nil || details.Diagnostics == nil || !details.Diagnostics.Policy.ContextCompiler || details.Diagnostics.Policy.Source != "stored" || len(details.Diagnostics.Requests) == 0 {
		t.Fatalf("admitted details=%+v err=%v", details, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("opening turn details made another model call: %d", len(provider.requests))
	}
	approvals, err := st.ListApprovals(t.Context(), "")
	if err != nil || len(approvals) != 0 {
		t.Fatalf("ordinary optimized chat created approval events: %+v err=%v", approvals, err)
	}
}

func TestTurnDetailsAdmissionSnapshot(t *testing.T) {
	e, th, tr, st := pluginHookEngine(t)
	r := newTurnRecorder(e, tr.ID, optimization.All(true), "stored")
	ctx := withTurnRecorder(t.Context(), r)
	if _, err := e.SetTokenOptimization(ctx, protocol.TokenOptimizationParams{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	r.finish()
	got, err := st.ReadTurnDetails(ctx, protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: tr.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Diagnostics.Policy.ContextCompiler || got.Diagnostics.Policy.Source != "stored" {
		t.Fatalf("policy=%+v", got)
	}
}
func TestTurnDetailsPartialPricing(t *testing.T) {
	e, th, tr, st := pluginHookEngine(t)
	r := newTurnRecorder(e, tr.ID, optimization.All(false), "stored")
	amount := 0.12
	r.recordRequest(protocol.RequestDiagnostics{Phase: "chat", CostState: "known", CostUSD: &amount})
	r.recordRequest(protocol.RequestDiagnostics{Phase: "chat", CostState: "unavailable"})
	r.finish()
	got, err := st.ReadTurnDetails(t.Context(), protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: tr.ID})
	if err != nil {
		t.Fatal(err)
	}
	cost, state := turnDetailsCost(got.Diagnostics.Requests)
	if state != "partial" || cost == nil || *cost != 0.12 {
		t.Fatalf("cost=%v state=%s", cost, state)
	}
}
func TestTurnDetailsInterrupted(t *testing.T) {
	e, th, tr, st := pluginHookEngine(t)
	r := newTurnRecorder(e, tr.ID, optimization.All(false), "stored")
	ctx, cancel := context.WithCancel(withTurnRecorder(t.Context(), r))
	cancel()
	n := int64(17)
	recorderFrom(ctx).recordRequest(protocol.RequestDiagnostics{Phase: "chat", InputTokens: &n, MeasurementOrigin: "provider", CostState: "unavailable"})
	r.finish()
	got, err := st.ReadTurnDetails(t.Context(), protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: tr.ID})
	if err != nil || got.Diagnostics == nil || *got.Diagnostics.Requests[0].InputTokens != 17 {
		t.Fatalf("partial=%+v err=%v", got, err)
	}
}
func TestTurnDetailsSensitiveBodies(t *testing.T) {
	e, th, tr, st := pluginHookEngine(t)
	r := newTurnRecorder(e, tr.ID, optimization.All(false), "stored")
	r.recordRequest(protocol.RequestDiagnostics{Phase: "chat", Model: "known-model", ToolNames: []string{"file_read"}})
	r.finish()
	got, err := st.ReadTurnDetails(t.Context(), protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: tr.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got.Diagnostics)
	for _, forbidden := range []string{"prompt", "toolOutput", "beforeBytes", "afterBytes"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("body field %s in %s", forbidden, b)
		}
	}
}
func TestTurnDetailsCaptureFailure(t *testing.T) {
	e, _, tr, st := pluginHookEngine(t)
	r := newTurnRecorder(e, tr.ID, optimization.All(false), "stored")
	st.Close()
	r.recordRequest(protocol.RequestDiagnostics{Phase: "chat"})
	r.finish() // capture failure is diagnostic-only, never a task panic/error.
}
