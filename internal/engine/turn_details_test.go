package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

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
