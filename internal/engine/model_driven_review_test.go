package engine

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestFocusedReviewIsOptionalAndBudgeted(t *testing.T) {
	if strings.Contains(delegationLeadPrompt, "Spawn an independent reviewer after") || strings.Contains(delegationLeadPrompt, "must submit") {
		t.Fatal("engine still mandates semantic review")
	}
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	layers := e.systemPromptLayers(t.Context(), "edit this", nil, "", nil)
	for _, l := range layers {
		if strings.Contains(l.Text, "Do not claim completion before agent.complete") {
			t.Fatal("ordinary chat requires semantic completion")
		}
	}
	_ = th
}
