package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestAdoptModelDrivenTaskPreservesOperations(t *testing.T) {
	st, th := workFixture(t)
	ctx := t.Context()
	turn := protocol.Turn{ID: NewID("trn"), ThreadID: th.ID, Status: "completed", StartedAt: time.Now()}
	if err := st.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "Edit notes.txt", CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: user.Text})
	if err != nil {
		t.Fatal(err)
	}
	op, err := st.CreateTaskOperation(ctx, protocol.TaskOperation{TaskID: task.ID, TurnID: turn.ID, Tool: "shell.run", Args: json.RawMessage(`{"command":"uncertain effect"}`)})
	if err != nil {
		t.Fatal(err)
	}
	op, err = st.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	op, err = st.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: "outcome_unknown", Failure: &protocol.ActionFailure{Category: "outcome_unknown", Message: "Effect uncertain"}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(op)
	for _, kind := range []string{"task_interpretation", "tool"} {
		if err := st.CreateApproval(ctx, protocol.Approval{ID: kind, ThreadID: th.ID, TurnID: turn.ID, ItemID: user.ID, Tool: "shell.run", Args: json.RawMessage(`{}`), Kind: kind, NodeID: task.ID, Status: "pending", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	adopter, ok := any(st).(interface {
		AdoptModelDrivenTask(context.Context, string) (protocol.TaskSnapshot, error)
	})
	if !ok {
		t.Fatal("store cannot adopt a legacy task without changing its execution facts")
	}
	snap, err := adopter.AdoptModelDrivenTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snap.Task)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["executionPolicy"] != "model_driven_v1" {
		t.Fatalf("policy not persisted: %s", raw)
	}
	if len(snap.Operations) != 1 {
		t.Fatal("operation lost")
	}
	after, _ := json.Marshal(snap.Operations[0])
	if string(after) != string(before) {
		t.Fatal("adoption changed uncertain operation")
	}
	obsolete, err := st.GetApproval(ctx, "task_interpretation")
	if err != nil || obsolete.Status == "pending" || obsolete.Status == "approved" {
		t.Fatalf("obsolete approval: %+v %v", obsolete, err)
	}
	actual, err := st.GetApproval(ctx, "tool")
	if err != nil || actual.Status != "pending" {
		t.Fatalf("actual permission changed: %+v %v", actual, err)
	}
	again, err := adopter.AdoptModelDrivenTask(ctx, task.ID)
	if err != nil || again.Task.Revision != snap.Task.Revision {
		t.Fatal("adoption is not idempotent", err)
	}
}
