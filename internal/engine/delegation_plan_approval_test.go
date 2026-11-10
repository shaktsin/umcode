package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// A plan-approved task is tied to the private parent. Unrelated edits to its
// source checkout must not prevent the lead from continuing the task.
func TestPlanApprovalAdmissionUsesPrivateParent(t *testing.T) {
	e, th, source, provider := delegationFixture(t)
	rootTurn := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "use two agents"})
	private := filepath.Join(t.TempDir(), "private")
	delegationGit(t, source, "clone", "-q", source, private)
	base := delegationGit(t, private, "rev-parse", "HEAD")
	plan := protocol.AgentPlan{ID: "private-plan", ThreadID: th.ID, State: protocol.PlanProposed, Agents: []protocol.PlanAgent{{Name: "worker", Purpose: "implement"}}, CreatedAt: time.Now()}
	if err := e.Store.SaveAgentPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	approvalID := "private-approval"
	if err := e.Store.CreateApproval(t.Context(), protocol.Approval{ID: approvalID, Kind: "agent_plan", NodeID: plan.ID, ThreadID: th.ID, TurnID: rootTurn.ID, Status: "pending", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "private-task", ThreadID: th.ID, RootTurnID: rootTurn.ID, Activation: protocol.AgentActivationPlanApproval, State: protocol.AgentStateRunning, BaseRevision: base, BaseRoot: pathutil.Resolved(source), ParentRoot: private, ProjectID: th.ProjectID, CreatedAt: time.Now()}
	workspace := store.AgentTaskWorkspace{TaskID: task.ID, SourceRoot: pathutil.Resolved(source), ExecutionRoot: private, SourceHead: base, SnapshotCommit: base, Manifest: []byte(`{"head":"base"}`), State: "active"}
	if err := e.Store.ApproveAgentPlanWithTask(t.Context(), plan.ID, task, workspace, store.AgentModelScope{Provider: "claude", Model: "deterministic"}, approvalID, "app", plan, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "untracked.txt"), []byte("user edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "continue", DelegationTaskID: task.ID})
	if got := provider.last(); got != task.ID {
		t.Fatalf("lead admitted task %q, want %q", got, task.ID)
	}
	if _, err := os.Stat(filepath.Join(private, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("private parent received later user edit: %v", err)
	}
}
