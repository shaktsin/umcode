package engine

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"testing"
)

func TestTokenOptimizationResolution(t *testing.T) {
	e, _, _, st := pluginHookEngine(t)
	r, err := e.TokenOptimization(t.Context())
	if err != nil || r.Enabled || r.Source != "default" {
		t.Fatalf("default: %+v %v", r, err)
	}
	e.Cfg.Models.ContextCompiler = true
	r, err = e.TokenOptimization(t.Context())
	if err != nil || !r.LegacyMixed || r.Source != "legacy" {
		t.Fatalf("legacy: %+v %v", r, err)
	}
	for _, on := range []bool{true, false} {
		r, err = e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: on})
		if err != nil || r.Enabled != on || r.Source != "stored" || r.LegacyMixed {
			t.Fatalf("save: %+v %v", r, err)
		}
		p, err := e.resolveOptimizationPolicy(t.Context())
		if err != nil || p.ContextCompiler != on || p.ContextRetrieval != on || p.ProgressiveTools != on || p.ToolResultReducers != on || p.DesignedWorkflow != on || p.AutoPromote != on || p.AutomaticWorkflow != on {
			t.Fatalf("policy: %+v %v", p, err)
		}
	}
	if _, err := st.DB.Exec(`UPDATE settings SET value_json='null' WHERE key='token_optimization.enabled'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.TokenOptimization(t.Context()); err == nil {
		t.Fatal("malformed persisted setting silently accepted")
	}
	if _, err := st.DB.Exec(`UPDATE settings SET value_json='"bad"' WHERE key='token_optimization.enabled'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resolveOptimizationPolicy(t.Context()); err == nil {
		t.Fatal("invalid setting silently accepted")
	}
	st.DB.Close()
	if _, err := e.TokenOptimization(t.Context()); err == nil {
		t.Fatal("store read failure hidden")
	}
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: true}); err == nil {
		t.Fatal("store write failure hidden")
	}
}
