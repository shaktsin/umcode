package models

import (
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// DefaultPresets are the built-in effort presets.
func DefaultPresets() map[protocol.Complexity]protocol.ComplexityPreset {
	return map[protocol.Complexity]protocol.ComplexityPreset{
		protocol.ComplexityLow:    {Level: protocol.ComplexityLow, Reasoning: llm.ReasoningLow},
		protocol.ComplexityMedium: {Level: protocol.ComplexityMedium, Reasoning: llm.ReasoningMedium},
		protocol.ComplexityHigh:   {Level: protocol.ComplexityHigh, Reasoning: llm.ReasoningHigh},
	}
}

// Presets merges config overrides (models.complexity in config.yaml) over the defaults.
func Presets(cfg config.ModelsConfig) map[protocol.Complexity]protocol.ComplexityPreset {
	out := DefaultPresets()
	for name, o := range cfg.Complexity {
		lvl := protocol.NormalizeComplexity(name)
		p, ok := out[lvl]
		if !ok {
			continue
		}
		if o.Reasoning != "" {
			p.Reasoning = o.Reasoning
		}
		out[lvl] = p
	}
	return out
}
