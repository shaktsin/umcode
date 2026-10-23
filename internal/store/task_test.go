package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Catches missing durable state, lost CAS predicates, cross-thread authority,
// and accidental replay or success attribution after reopening the database.
func TestTaskLedger(t *testing.T) {
	st, th := workFixture(t)
	ctx := t.Context()
	w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "edit a file"})
	if err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, Kind: "userMessage", Status: "completed", Text: "edit notes.txt", Seq: 1, CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, WorkID: w.ID, OriginItemID: user.ID, Objective: user.Text})
	if err != nil {
		t.Fatal(err)
	}
	if task.Revision != 1 || task.Status != "active" || task.ID == "" {
		t.Fatalf("task=%+v", task)
	}
	if _, err := st.TransitionExecutionTask(ctx, task.ID, protocol.TaskTransition{ExpectedRevision: 0, Status: "completed"}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale=%v", err)
	}
	if _, err := st.TransitionExecutionTask(ctx, task.ID, protocol.TaskTransition{ExpectedRevision: task.Revision, Status: "active|blocked"}); err == nil {
		t.Fatal("composite task status accepted")
	}
	for _, status := range []string{"blocked", "awaiting_approval", "active", "completed", "active", "cancelled", "active"} {
		task, err = st.TransitionExecutionTask(ctx, task.ID, protocol.TaskTransition{ExpectedRevision: task.Revision, Status: status})
		if err != nil || task.Status != status {
			t.Fatalf("transition %s: %+v %v", status, task, err)
		}
	}
	if _, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, WorkID: w.ID, OriginItemID: user.ID, Objective: "wrong project", ProjectID: "foreign"}); err == nil {
		t.Fatal("foreign project accepted")
	}
	if _, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: strings.Repeat("x", 65537)}); err == nil {
		t.Fatal("oversized task accepted")
	}
	turn := protocol.Turn{ID: NewID("trn"), ThreadID: th.ID, Status: "running", StartedAt: time.Now()}
	if err := st.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	item := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 2, Kind: "toolCall", Status: "running", CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	op, err := st.CreateTaskOperation(ctx, protocol.TaskOperation{TaskID: task.ID, TurnID: turn.ID, ItemID: item.ID, Tool: "file.write", Args: []byte(`{"path":"notes.txt","content":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"blocked", "awaiting_approval", "running", "failed", "running", "succeeded", "outcome_unknown", "cancelled"} {
		op, err = st.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: status, VerificationStatus: "pending"})
		if err != nil || op.Status != status {
			t.Fatalf("operation=%+v %v", op, err)
		}
	}
	for _, v := range []string{"passed", "failed", "blocked", "pending"} {
		op, err = st.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: op.Status, VerificationStatus: v})
		if err != nil || op.VerificationStatus != v {
			t.Fatalf("verification=%+v %v", op, err)
		}
	}
	if _, err := st.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: 1, Status: "succeeded"}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale op=%v", err)
	}
	if _, err := st.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: "succeeded", EvidenceIDs: []string{"foreign"}}); err == nil {
		t.Fatal("foreign evidence accepted")
	}
	other, err := st.CreateThread(ctx, protocol.Thread{Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	foreignTurn := protocol.Turn{ID: NewID("trn"), ThreadID: other.ID, Status: "running", StartedAt: time.Now()}
	if err := st.CreateTurn(ctx, foreignTurn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTaskOperation(ctx, protocol.TaskOperation{TaskID: task.ID, TurnID: foreignTurn.ID, Tool: "file.write"}); err == nil {
		t.Fatal("foreign operation accepted")
	}
	if _, err := st.DB.ExecContext(ctx, `CREATE TRIGGER fail_new_task BEFORE INSERT ON engine_tasks BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: "rollback"}); err == nil {
		t.Fatal("injected failure did not fail")
	}
	unchanged, err := st.ActiveExecutionTask(ctx, th.ID)
	if err != nil || unchanged.Task.ID != task.ID || unchanged.Task.Revision != task.Revision {
		t.Fatal("partial supersession", err)
	}
	if _, err := st.DB.ExecContext(ctx, `DROP TRIGGER fail_new_task`); err != nil {
		t.Fatal(err)
	}
	next, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, WorkID: w.ID, OriginItemID: user.ID, Objective: "new objective"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.ActiveExecutionTask(ctx, th.ID)
	if err != nil || snapshot.Task.ID != next.ID {
		t.Fatalf("active=%+v %v", snapshot, err)
	}
	var status string
	if err := st.DB.QueryRowContext(ctx, "SELECT status FROM engine_tasks WHERE id=?", task.ID).Scan(&status); err != nil || status != "superseded" {
		t.Fatalf("prior=%s %v", status, err)
	}
	old, err := st.GetItem(ctx, user.ID)
	if err != nil || old.Text != user.Text {
		t.Fatal("history changed", err)
	}
	if got, err := st.GetWorkDetail(ctx, w.ID); err != nil || got.Work.Revision != w.Revision {
		t.Fatal("work changed", err)
	}
}

func TestTaskLedgerInterruptedOperation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	th, err := st.CreateThread(ctx, protocol.Thread{Title: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, Seq: 1, Kind: "userMessage", Text: "edit", CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	turn := protocol.Turn{ID: NewID("trn"), ThreadID: th.ID, Status: "running", StartedAt: time.Now()}
	if err := st.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"succeeded", "running"} {
		op, err := st.CreateTaskOperation(ctx, protocol.TaskOperation{TaskID: task.ID, TurnID: turn.ID, Tool: "shell.run", Args: []byte(`{"command":"irreversible"}`)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	snap, err := st.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Operations) != 2 || snap.Operations[0].Status != "succeeded" || snap.Operations[1].Status != "running" {
		t.Fatalf("reopened=%+v", snap)
	}
}

func TestTaskLedgerLargeArguments(t *testing.T) {
	st, th := workFixture(t)
	ctx := t.Context()
	turn := protocol.Turn{ID: NewID("trn"), ThreadID: th.ID, Status: "running", StartedAt: time.Now()}
	if err := st.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Text: "Write a large document", CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: user.Text})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": "large.md", "content": strings.Repeat("document ", 20000)})
	item := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 2, Kind: protocol.ItemToolCall, Tool: &protocol.ToolCallData{Name: "file.write", Args: args}, CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	op, err := st.CreateTaskOperation(ctx, protocol.TaskOperation{TaskID: task.ID, TurnID: turn.ID, ItemID: item.ID, Tool: "file.write", Args: args})
	if err != nil {
		t.Fatal("metadata limit blocked supported file payload", err)
	}
	if op.ArgsRef != item.ID || op.ArgsSHA256 == "" || len(op.Args) > 16384 {
		t.Fatalf("large arguments were duplicated or lost: ref=%s len=%d", op.ArgsRef, len(op.Args))
	}
	stored, err := st.GetItem(ctx, item.ID)
	if err != nil || !bytes.Equal(stored.Tool.Args, args) {
		t.Fatal("original payload changed", err)
	}
}
