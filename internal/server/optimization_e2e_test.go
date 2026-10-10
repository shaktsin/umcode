package server_test

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"testing"
)

func TestTokenOptimizationRPC(t *testing.T) {
	h := newHarness(t, nil)
	var r protocol.TokenOptimizationResult
	h.call(protocol.MethodTokenOptimizationGet, struct{}{}, &r)
	if r.Enabled || r.Source != "default" {
		t.Fatalf("default %+v", r)
	}
	for _, on := range []bool{true, false} {
		h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: on}, &r)
		h.call(protocol.MethodTokenOptimizationGet, struct{}{}, &r)
		if r.Enabled != on || r.Source != "stored" || r.LegacyMixed {
			t.Fatalf("round trip %+v", r)
		}
	}
	if err := h.callErr(protocol.MethodTokenOptimizationSet, map[string]any{"enabled": "yes"}); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}
