package engine

import (
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestTaskRecovery(t *testing.T) {
	for _, tc := range []struct {
		message, category, next string
		retry                   bool
	}{
		{"old_string was not found; re-read the file", "edit_conflict", "read", true},
		{"old_string matches 2 places", "edit_conflict", "read", true},
		{"file does not exist", "missing_resource", "location", true},
		{"nodes.expected_revision: stale", "stale_state", "refresh", true},
		{"The user denied this action.", "approval_rejected", "stop", false},
		{"Blocked by policy: outside project", "policy_denied", "permission", false},
		{"command exited with status 1", "execution_failed", "inspect", true},
	} {
		t.Run(tc.category+tc.message, func(t *testing.T) {
			f := classifyActionFailure("op", errors.New(tc.message))
			if f.Category != tc.category || f.OperationID != "op" || f.Retryable != tc.retry || !strings.Contains(strings.ToLower(f.NextStep), tc.next) {
				t.Fatalf("failure=%+v", f)
			}
		})
	}
}
func TestTaskRetryDoesNotReplay(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Update a file")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	for _, status := range []string{"succeeded", "running"} {
		op, err := e.Store.CreateTaskOperation(t.Context(), protocol.TaskOperation{TaskID: snap.Task.ID, TurnID: user.TurnID, Tool: "shell.run", Args: json.RawMessage(`{"command":"non-idempotent operation"}`)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.TransitionTaskOperation(t.Context(), op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.recoverInterruptedTask(t.Context(), snap.Task.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if snap.Operations[0].Status != "succeeded" || snap.Operations[1].Status != "outcome_unknown" {
		t.Fatalf("recovered=%+v", snap.Operations)
	}
	decision, err := e.checkTaskAuthority(t.Context(), th, snap, protocol.TaskOperation{TaskID: snap.Task.ID, Tool: "shell.run", Args: json.RawMessage(`{"command":"try a different mutation"}`)})
	if err != nil || decision.Outcome == "allow" || decision.Failure.Category != "outcome_unknown" {
		t.Fatalf("unknown effect replay allowed: %+v %v", decision, err)
	}
	read, err := e.checkTaskAuthority(t.Context(), th, snap, protocol.TaskOperation{TaskID: snap.Task.ID, Tool: "file.read", Args: json.RawMessage(`{"path":"notes.txt"}`)})
	if err != nil || read.Outcome != "allow" {
		t.Fatalf("inspection blocked: %+v %v", read, err)
	}
}
func TestTaskAmbiguousRetry(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Work on these files")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	for _, path := range []string{"one.txt", "two.txt"} {
		args, _ := json.Marshal(map[string]string{"path": path, "content": "new"})
		op, err := e.Store.CreateTaskOperation(t.Context(), protocol.TaskOperation{TaskID: snap.Task.ID, TurnID: user.TurnID, Tool: "file.write", Args: args})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.TransitionTaskOperation(t.Context(), op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: "failed", Failure: &protocol.ActionFailure{Category: "execution_failed", Retryable: true}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.resolveTask(t.Context(), th, add("retry"))
	if err != nil || got.Relation != "clarify" || got.Question == "" || strings.Count(got.Question, "?") != 1 {
		t.Fatalf("ambiguous retry=%+v %v", got, err)
	}
}

func TestTaskRecoveryStateStopsEquivalentFailures(t *testing.T) {
	var state taskRecoveryState
	for i, action := range []string{"edit-payload-one", "read-and-edit-payload-two", "write-alternative"} {
		stop := state.observe(protocol.ActionFailure{Category: "edit_conflict", BlockerID: "notes.txt", Retryable: true}, action)
		if stop != (i == 2) {
			t.Fatalf("attempt %d stop=%v", i, stop)
		}
	}
	state.resolved("unrelated")
	if state.failure() == nil {
		t.Fatal("unrelated success cleared failures")
	}
}
