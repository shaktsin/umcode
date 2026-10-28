package models

import (
	"reflect"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestResolveLevelNearest(t *testing.T) {
	m := Meta{Reasoning: true, ReasoningLevels: []string{"low", "high"}}
	if got, ok := m.ResolveLevel(protocol.ComplexityMedium); !ok || got != "low" {
		t.Errorf("medium -> %q,%v, want low,true", got, ok)
	}
	if got, ok := m.ResolveLevel(protocol.ComplexityHigh); !ok || got != "high" {
		t.Errorf("high -> %q,%v", got, ok)
	}
	one := Meta{ReasoningLevels: []string{"high"}}
	if got, ok := one.ResolveLevel(protocol.ComplexityLow); !ok || got != "high" {
		t.Errorf("low on [high] -> %q,%v", got, ok)
	}
	if got, ok := (Meta{ReasoningLevels: []string{"low", "medium", "high"}}).ResolveLevel(""); !ok || got != "medium" {
		t.Errorf("empty -> %q,%v, want medium", got, ok)
	}
}

func TestResolveLevelNoThinking(t *testing.T) {
	if got, ok := (Meta{}).ResolveLevel(protocol.ComplexityHigh); ok || got != "" {
		t.Errorf("got %q,%v, want \"\",false", got, ok)
	}
}

func TestInferSelfAdapting(t *testing.T) {
	all := []string{"low", "medium", "high"}
	for _, c := range []struct{ p, m string }{{"claude", "claude-sonnet-4"}, {"openai", "gpt-5"}, {"openai", "o3"}, {"gemini", "gemini-2.5-pro"}} {
		m := infer(c.p, c.m)
		if !m.SelfAdapting || !reflect.DeepEqual(m.ReasoningLevels, all) {
			t.Errorf("%s/%s: SelfAdapting=%v levels=%v", c.p, c.m, m.SelfAdapting, m.ReasoningLevels)
		}
	}
	if m := infer("openai", "gpt-4o"); m.SelfAdapting || len(m.ReasoningLevels) != 0 {
		t.Errorf("gpt-4o should not reason: %+v", m)
	}
	m := infer("openai_compatible", "qwen")
	if m.SelfAdapting || !m.Reasoning || m.ReasoningStyle != "template" || !reflect.DeepEqual(m.ReasoningLevels, all) {
		t.Errorf("openai_compatible: %+v", m)
	}
}
