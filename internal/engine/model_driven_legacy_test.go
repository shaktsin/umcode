package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func assertModelDrivenPolicy(t *testing.T, e *Engine, th protocol.Thread) protocol.TaskSnapshot {
	t.Helper()
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snap.Task)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["executionPolicy"] != "model_driven_v1" {
		t.Fatalf("legacy task was not adopted: %s", raw)
	}
	return snap
}
func TestLegacySemanticFailureDoesNotBlockResumeNow(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := e.Store.GetItem(t.Context(), snap.Task.OriginItemID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := e.Store.SetTaskExpectations(t.Context(), snap.Task.ID, snap.Task.Revision, origin.ID, origin.TurnID, []protocol.TaskExpectationProposal{{Source: protocol.TaskSourceSpan{ItemID: origin.ID, Start: 0, End: 6}, Kind: "mutation", Predicate: "file_content", Resource: json.RawMessage(`{"type":"file","path":"notes.txt"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.TransitionExecutionTask(t.Context(), old.Task.ID, protocol.TaskTransition{ExpectedRevision: old.Task.Revision, Status: "blocked"}); err != nil {
		t.Fatal(err)
	}
	p := &modelDrivenScript{answer: "Continuing from the recorded changes."}
	e.LLMs.Register(p)
	turn := runTaskRuntimeTurn(t, e, th, "resume now")
	if turn.Status != protocol.TurnCompleted || len(p.requests) != 1 {
		t.Fatalf("legacy gate blocked continuation: %s", turn.Error)
	}
	adopted := assertModelDrivenPolicy(t, e, th)
	if len(adopted.Expectations) != 1 || adopted.Expectations[0].ID != old.Expectations[0].ID {
		t.Fatal("legacy audit expectation deleted")
	}
}
func TestLegacySemanticRequirementDoesNotBlockEdit(t *testing.T) {
	e, th, root, _ := taskRuntimeFixture(t, "write_verified", true)
	turn := runTaskRuntimeTurn(t, e, th, "Update notes.txt now")
	b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil || string(b) != "after" || turn.Status != protocol.TurnCompleted {
		t.Fatalf("inferred contract blocked ordinary edit: %q %s %v", b, turn.Error, err)
	}
	assertModelDrivenPolicy(t, e, th)
}
func TestLegacyPermissionDenialStillApplies(t *testing.T) {
	e, th, root, _ := taskRuntimeFixture(t, "summary_only", false)
	p := &modelDrivenScript{failRead: "../outside.txt", answer: "The file is outside this project."}
	e.LLMs.Register(p)
	turn := runTaskRuntimeTurn(t, e, th, "Read the requested file")
	if turn.Status != protocol.TurnCompleted {
		t.Fatal(turn.Error)
	}
	snap := assertModelDrivenPolicy(t, e, th)
	denied := false
	for _, op := range snap.Operations {
		if op.Status == "blocked" && op.Failure != nil && op.Failure.Category == "policy_denied" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("project permission denial lost")
	}
	b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil || string(b) != "before" {
		t.Fatal("denied action changed project", err)
	}
}
func TestLegacyUnknownMutationIsNotReplayed(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := e.Store.GetItem(t.Context(), snap.Task.OriginItemID)
	if err != nil {
		t.Fatal(err)
	}
	op, err := e.Store.CreateTaskOperation(t.Context(), protocol.TaskOperation{TaskID: snap.Task.ID, TurnID: origin.TurnID, Tool: "shell.run", Args: json.RawMessage(`{"command":"touch should-not-run"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.TransitionTaskOperation(t.Context(), op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	p := &modelDrivenScript{answer: "The previous action has an unknown outcome; inspect it before retrying."}
	e.LLMs.Register(p)
	turn := runTaskRuntimeTurn(t, e, th, "resume now")
	if turn.Status != protocol.TurnCompleted {
		t.Fatal(turn.Error)
	}
	adopted := assertModelDrivenPolicy(t, e, th)
	if len(adopted.Operations) != 1 || adopted.Operations[0].Status != "outcome_unknown" || len(p.requests) != 1 {
		t.Fatalf("unknown operation changed or replayed: %+v", adopted.Operations)
	}
}
func TestLegacyIntentInputIsAdvisory(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	p := &modelDrivenScript{answer: "Here is the explanation."}
	e.LLMs.Register(p)
	wait := make(chan protocol.Turn, 1)
	_, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain this", TaskIntent: &protocol.TaskIntentProposal{Relation: "not-a-valid-old-contract"}, Override: protocol.ModelSelection{Provider: "claude", Model: "deterministic", Complexity: protocol.ComplexityStandard}}, wait: wait})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case turn := <-wait:
		if turn.Status != protocol.TurnCompleted || len(p.requests) != 1 {
			t.Fatalf("legacy metadata blocked input: %s", turn.Error)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("turn timed out")
	}
	assertModelDrivenPolicy(t, e, th)
}

func TestLegacyTaskProposalIsAdvisory(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	old, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := e.Store.AdoptModelDrivenTask(t.Context(), old.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := e.Store.GetItem(t.Context(), snap.Task.OriginItemID)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"task_id": snap.Task.ID, "expected_revision": snap.Task.Revision, "requirement": map[string]any{"origin": "user", "origin_ref": origin.ID, "scope": map[string]any{}, "condition": map[string]string{"type": "no_actions"}}})
	out, err := e.applyTaskProposal(t.Context(), protocol.Turn{ID: origin.TurnID, ThreadID: th.ID}, args)
	if err != nil {
		t.Fatalf("advisory model metadata invoked legacy requirement admission: %v", err)
	}
	var result struct {
		Advisory bool `json:"advisory"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || !result.Advisory {
		t.Fatalf("proposal became mandatory metadata: %s", out)
	}
	after, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil || len(after.Requirements) != len(snap.Requirements) {
		t.Fatal("proposal created an engine requirement", err)
	}
	approvals, err := e.Store.ListApprovals(t.Context(), "pending")
	if err != nil || len(approvals) != 0 {
		t.Fatal("advisory metadata created approvals", err)
	}
}
