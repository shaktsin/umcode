package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
)

// Imported pre-task-engine denials have no authoritySource, including records
// already migrated by the first local build. Only engine-denied workflow items
// may be reevaluated; tool failures must never acquire engine provenance.
func TestTaskLegacyWorkflowDenialReevaluated(t *testing.T) {
	for _, tc := range []struct {
		name            string
		itemStatus      string
		message         string
		mandatory, want bool
	}{
		{"obsolete bare denial", protocol.ItemDenied, "workflow not ready", false, true},
		{"obsolete structured denial", protocol.ItemDenied, `workflow not ready: {"code":"workflow_not_ready","work_id":"old","expected_revision":3}`, false, true},
		{"mandatory review remains", protocol.ItemDenied, "workflow not ready", true, false},
		{"executed tool error is not engine authority", protocol.ItemFailed, "workflow not ready", false, false},
		{"ordinary denial remains", protocol.ItemDenied, "permission denied", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, th, add := taskContextFixture(t)
			user := add("Correct the specification")
			if _, err := e.Store.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, Goal: user.Text, WorkflowDepth: protocol.DepthDesigned}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.resolveTask(t.Context(), th, user); err != nil {
				t.Fatal(err)
			}
			e.Cfg = &config.Config{Policy: config.PolicyConfig{RequireWorkflowApproval: tc.mandatory}}
			item := protocol.Item{ID: "legacy-denial", ThreadID: th.ID, TurnID: user.TurnID, Seq: 2, Kind: protocol.ItemToolCall, Status: tc.itemStatus, Tool: &protocol.ToolCallData{Name: "file.edit", Args: json.RawMessage(`{"path":"arbitrary.md"}`), Error: tc.message}}
			if err := e.Store.SaveItem(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			op := protocol.TaskOperation{Status: "blocked", Tool: "file.edit", ItemID: item.ID, TurnID: user.TurnID, ImplementationID: "legacy_unverified", Failure: &protocol.ActionFailure{Category: "execution_failed", Message: tc.message, Retryable: true}}
			if got := e.taskWorkflowConditionCleared(t.Context(), th.ID, op); got != tc.want {
				t.Fatalf("cleared=%v want=%v", got, tc.want)
			}
			// An opaque tool's denied result cannot be promoted by the message alone.
			op.ImplementationID = "plugin"
			if e.taskWorkflowConditionCleared(t.Context(), th.ID, op) {
				t.Fatal("non-legacy failure promoted")
			}
		})
	}
}

func TestTaskLegacyWorkflowDenialVerifiedWriteActualTurn(t *testing.T) {
	e, th, root, _ := taskRuntimeFixture(t, "write_verified", false)
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := e.Store.GetItem(t.Context(), snap.Task.OriginItemID)
	if err != nil {
		t.Fatal(err)
	}
	// The denied edit deliberately asks for a different representation than the
	// eventual replacement. No byte-equivalence shortcut can resolve this denial.
	item := protocol.Item{ID: "legacy-denial", ThreadID: th.ID, TurnID: origin.TurnID, Seq: 2, Kind: protocol.ItemToolCall, Status: protocol.ItemDenied, Tool: &protocol.ToolCallData{Name: "file.edit", Args: json.RawMessage(`{"path":"notes.txt","old_string":"old\\ntext","new_string":"new\\ntext"}`), Error: "workflow not ready"}}
	if err := e.Store.SaveItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	old, err := e.Store.CreateTaskOperation(t.Context(), protocol.TaskOperation{TaskID: snap.Task.ID, TurnID: item.TurnID, ItemID: item.ID, Tool: item.Tool.Name, Args: item.Tool.Args, ImplementationID: "legacy_unverified", Status: "blocked", VerificationStatus: "pending", Failure: &protocol.ActionFailure{Category: "execution_failed", Message: item.Tool.Error, Retryable: true}})
	if err != nil {
		t.Fatal(err)
	}
	old, err = e.Store.TransitionTaskOperation(t.Context(), old.ID, protocol.OperationTransition{ExpectedRevision: old.Revision, Status: "blocked", VerificationStatus: "pending", Failure: &protocol.ActionFailure{Category: "execution_failed", Message: item.Tool.Error, Retryable: true}})
	if err != nil {
		t.Fatal(err)
	}
	finished := runTaskRuntimeTurn(t, e, th, "/superpowers:using-superpowers proceed")
	if finished.Status != protocol.TurnCompleted {
		t.Fatalf("verified replacement failed: %+v", finished)
	}
	body, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil || string(body) != "after" {
		t.Fatalf("effect=%q %v", body, err)
	}
	preserved, err := e.Store.GetTaskOperation(t.Context(), old.ID)
	if err != nil || preserved.Status != "blocked" {
		t.Fatalf("historical denial rewritten: %+v %v", preserved, err)
	}
	snap, err = e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil || snap.Task.Status != "completed" {
		t.Fatalf("task=%+v %v", snap.Task, err)
	}
	verified := false
	for _, op := range snap.Operations {
		if op.Tool == "file.write" && op.Status == "succeeded" && op.VerificationStatus == "passed" && len(op.EvidenceIDs) > 0 {
			verified = true
		}
	}
	if !verified {
		t.Fatal("write lacks read-back evidence")
	}
}
