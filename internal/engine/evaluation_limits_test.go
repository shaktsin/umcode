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
	if err != nil {
		t.Fatal("bounded wire output was rejected", err)
	}
	if limit := llm.EffectiveOutputLimit("claude", llm.Request{MaxTokens: 1000, Reasoning: llm.ReasoningMedium, ReasoningStyle: "budget"}); limit != 1000 {
		t.Fatal("wire output grew", limit)
	}
	if _, err = p.Stream(t.Context(), llm.Credential{}, llm.Request{Model: "claude-3-7-sonnet", MaxTokens: 1001}); err == nil {
		t.Fatal("output above declared maximum admitted")
	}
}
