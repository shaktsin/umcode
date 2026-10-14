package engine

import (
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/llm"
	"testing"
)

func TestEvaluationWireOutputLimit(t *testing.T) {
	p := &evaluationProvider{inner: &evalStreamFixture{}, synthetic: true, model: evaluation.Model{ID: "claude-3-7-sonnet", Window: 100000, MaxOutput: 1000}}
	ch, err := p.Stream(t.Context(), llm.Credential{}, llm.Request{Model: "claude-3-7-sonnet", MaxTokens: 1000, Reasoning: llm.ReasoningMedium, ReasoningStyle: "budget"})
	if ch != nil {
		for range ch {
		}
	}
	if err == nil {
		t.Fatal("provider-expanded output limit bypassed declared bound")
	}
}
