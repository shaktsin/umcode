package engine

import (
	"context"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/work"
	"testing"
)

func TestOptimizationTurnSnapshot(t *testing.T) {
	e, th, _, st := pluginHookEngine(t)
	e.Work = &work.Service{Store: st, Log: e.Log}
	e.Tools.Add(tools.NewWorkUpdate(e.Work))
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	p, err := e.resolveOptimizationPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a := optimization.WithPolicy(t.Context(), p)
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	q, err := e.resolveOptimizationPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b := optimization.WithPolicy(t.Context(), q)
	for _, tc := range []struct {
		ctx  context.Context
		want int
	}{{a, 1}, {b, 0}} {
		actual, _, err := e.permittedTurnCatalog(tc.ctx, nil, nil)
		if err != nil || len(actual) != tc.want {
			t.Fatalf("turn catalog=%d want=%d err=%v", len(actual), tc.want, err)
		}
	}
	if err := e.Work.Begin(a, th, "design a new architecture"); err != nil {
		t.Fatal(err)
	}
	w, ok, err := st.OpenWorkForThread(t.Context(), th.ID)
	if err != nil || !ok || w.WorkflowDepth != "designed" {
		t.Fatalf("Work missed turn policy: %+v %v", w, err)
	}
	if !e.optimizationPolicy(context.WithoutCancel(a)).AutoPromote || e.optimizationPolicy(b).AutoPromote {
		t.Fatal("policy lost across persistence boundary")
	}
}
