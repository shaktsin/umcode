package engine

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestFocusedReviewIsOptionalAndBudgeted(t *testing.T) {
	if strings.Contains(delegationWorkflowPrompt, "Spawn an independent reviewer after") || strings.Contains(delegationWorkflowPrompt, "must submit") {
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
func TestReviewCannotWidenParentPermissions(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	ctx := withChildExecution(t.Context(), &childExecution{child: protocol.AgentChild{Role: protocol.AgentRoleReviewer}, workspace: ChildWorkspace{Root: "/owned/review", Revision: "revision"}})
	layers := e.systemPromptLayers(ctx, "", nil, "", nil)
	if len(layers) != 1 || !strings.Contains(layers[0].Text, "All file access is confined") || !strings.Contains(layers[0].Text, "read-only") {
		t.Fatal("reviewer permissions widened")
	}
	if strings.Contains(layers[0].Text, "An approved summary is not a review report") {
		t.Fatal("structured review is mandatory")
	}
}
