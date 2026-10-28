package models

import "github.com/shaktsin/umcode/internal/protocol"

var allLevels = []string{"low", "medium", "high"}

func levelRank(l string) int {
	switch l {
	case "low":
		return 0
	case "high":
		return 2
	}
	return 1
}

// ResolveLevel maps a requested level onto the nearest level the model
// supports (ties go lower). ok is false when the model has no thinking.
// An empty request means medium.
func (m Meta) ResolveLevel(l protocol.Complexity) (string, bool) {
	if len(m.ReasoningLevels) == 0 {
		return "", false
	}
	want := levelRank(string(protocol.NormalizeComplexity(string(l))))
	best, bestDist := "", 99
	for _, s := range m.ReasoningLevels {
		d := levelRank(s) - want
		if d < 0 {
			d = -d
		}
		if d < bestDist || (d == bestDist && levelRank(s) < levelRank(best)) {
			best, bestDist = s, d
		}
	}
	return best, true
}

// withReasoningProfile fills ReasoningLevels/SelfAdapting for a reasoning
// model that does not carry them yet (bundled catalog entries).
func withReasoningProfile(m Meta) Meta {
	if m.Reasoning && len(m.ReasoningLevels) == 0 {
		m.ReasoningLevels = append([]string(nil), allLevels...)
		m.SelfAdapting = m.Provider != "openai_compatible"
	}
	return m
}
