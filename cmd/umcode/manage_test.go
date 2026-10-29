package main

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPresetsTableHasNoCapColumn(t *testing.T) {
	d := protocol.ComplexityDefaults{
		Default:                protocol.ComplexityMedium,
		DefaultMaxOutputTokens: 32768,
		Presets: []protocol.ComplexityPreset{
			{Level: protocol.ComplexityLow, Reasoning: "low"},
			{Level: protocol.ComplexityMedium, Reasoning: "medium"},
			{Level: protocol.ComplexityHigh, Reasoning: "high"},
		},
	}
	out := renderComplexityDefaults(d)
	for _, bad := range []string{"MAX", "OUTPUT TOKENS", "REASONING", "auto"} {
		if strings.Contains(out, bad) {
			t.Fatalf("table mentions %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{"default: medium", "default max output tokens: 32768", "LEVEL", "low", "medium", "high"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table lacks %q:\n%s", want, out)
		}
	}
}

func TestComplexityRenderShowsNoLimit(t *testing.T) {
	d := protocol.ComplexityDefaults{Default: protocol.ComplexityMedium, DefaultMaxOutputTokens: 32768}
	d.Limits.MaxDurationMinutes, d.Limits.MaxTokens = protocol.NoLimit, protocol.NoLimit
	out := renderComplexityDefaults(d)
	if !strings.Contains(out, "time limit: no limit") || !strings.Contains(out, "token budget: no limit") {
		t.Fatalf("output = %q", out)
	}
	d.Limits.MaxDurationMinutes, d.Limits.MaxTokens = 120, 1_000_000
	out = renderComplexityDefaults(d)
	if !strings.Contains(out, "time limit: 120 minutes") || !strings.Contains(out, "token budget: 1000000") {
		t.Fatalf("output = %q", out)
	}
}
