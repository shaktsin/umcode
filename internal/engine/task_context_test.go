package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/teststore"
)

func taskContextFixture(t *testing.T) (*Engine, protocol.Thread, func(string) protocol.Item) {
	t.Helper()
	st, err := teststore.Open(t.Context(), filepath.Join(t.TempDir(), "task-context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "task context"})
	if err != nil {
		t.Fatal(err)
	}
	seq := int64(0)
	add := func(text string) protocol.Item {
		seq++
		turn := protocol.Turn{ID: store.NewID("trn"), ThreadID: th.ID, Status: "running", StartedAt: time.Now()}
		if err := st.CreateTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
		it := protocol.Item{ID: store.NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: seq, Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: text, CreatedAt: time.Now()}
		if err := st.SaveItem(t.Context(), it); err != nil {
			t.Fatal(err)
		}
		return it
	}
	return &Engine{Store: st}, th, add
}

// Catches losing the original objective when the next turn says only retry.
func TestTaskContinuity(t *testing.T) {
	e, th, add := taskContextFixture(t)
	ctx := t.Context()
	first := add("Update notes.txt with the requested corrections")
	got, err := e.resolveTask(ctx, th, first)
	if err != nil || got.Relation != "new" {
		t.Fatalf("new=%+v %v", got, err)
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil || snap.Task.OriginItemID != first.ID {
		t.Fatal("lost origin", err)
	}
	id := snap.Task.ID
	for _, text := range []string{"retry", "/superpowers:using-superpowers retry", "continue", "resume"} {
		current := add(text)
		got, err = e.resolveTask(ctx, th, current)
		if err != nil || got.Relation != "continue" || got.TaskID != id {
			t.Fatalf("continuation=%+v %v", got, err)
		}
		context, err := e.taskContext(ctx, th.ID)
		if err != nil || !strings.Contains(context, "Update notes.txt") || !strings.Contains(context, id) {
			t.Fatalf("context=%q %v", context, err)
		}
	}
	next := add("New task: inspect a different project file")
	got, err = e.resolveTask(ctx, th, next)
	if err != nil || got.Relation != "new" || got.TaskID == id {
		t.Fatalf("new task=%+v %v", got, err)
	}
	old, err := e.Store.GetExecutionTask(ctx, id)
	if err != nil || old.Status != "superseded" {
		t.Fatal("old task active", err)
	}
	other, err := e.Store.CreateThread(ctx, protocol.Thread{Title: "no active task"})
	if err != nil {
		t.Fatal(err)
	}
	user := add("retry")
	user.ThreadID = other.ID
	user.ID = store.NewID("itm")
	user.TurnID = ""
	if err := e.Store.SaveItem(ctx, user); err != nil {
		t.Fatal(err)
	}
	got, err = e.resolveTask(ctx, other, user)
	if err != nil || got.Relation != "clarify" || got.Question == "" {
		t.Fatalf("missing task=%+v %v", got, err)
	}
}

// Catches models turning task metadata into permission or approval credentials.
func TestTaskProposalCannotGrantAuthority(t *testing.T) {
	e, th, add := taskContextFixture(t)
	ctx := t.Context()
	first := add("Edit notes.txt")
	if _, err := e.resolveTask(ctx, th, first); err != nil {
		t.Fatal(err)
	}
	current := add("retry")
	if _, err := e.resolveTask(ctx, th, current); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn := protocol.Turn{ID: current.TurnID, ThreadID: th.ID}
	for _, extra := range []map[string]any{
		{"status": "completed"}, {"approval_id": "unrelated"}, {"policy": "auto_all"},
		{"relation": "new", "user_item_id": first.ID},
		{"requirement": map[string]any{"origin": "user", "originRef": first.ID, "status": "approved"}},
	} {
		a := map[string]any{"task_id": snap.Task.ID, "expected_revision": snap.Task.Revision}
		for k, v := range extra {
			a[k] = v
		}
		raw, _ := json.Marshal(a)
		if _, err := e.applyTaskProposal(ctx, turn, raw); err == nil {
			t.Fatalf("authority proposal accepted: %s", raw)
		}
	}
	raw, _ := json.Marshal(map[string]any{"task_id": snap.Task.ID, "expected_revision": snap.Task.Revision - 1, "relation": "continue", "user_item_id": current.ID})
	if _, err := e.applyTaskProposal(ctx, turn, raw); err == nil {
		t.Fatal("stale proposal accepted")
	}
	after, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil || after.Task.Revision != snap.Task.Revision || after.Task.Status != "active" || len(after.Requirements) != 0 {
		t.Fatalf("invalid proposal changed state: %+v %v", after, err)
	}
}

func TestTaskProposalCannotInventRequirements(t *testing.T) {
	e, th, add := taskContextFixture(t)
	first := add("Edit notes.txt")
	if _, err := e.resolveTask(t.Context(), th, first); err != nil {
		t.Fatal(err)
	}
	current := add("retry")
	if _, err := e.resolveTask(t.Context(), th, current); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	args, _ := json.Marshal(map[string]any{"task_id": snap.Task.ID, "expected_revision": snap.Task.Revision, "requirement": map[string]any{"origin": "user", "origin_ref": current.ID, "scope": map[string]any{}, "condition": map[string]string{"type": "no_actions"}}})
	if _, err := e.applyTaskProposal(t.Context(), protocol.Turn{ID: current.TurnID, ThreadID: th.ID}, args); err == nil {
		t.Fatal("model invented a human requirement without engine approval")
	}
	after, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if len(after.Requirements) != 0 {
		t.Fatal("unconfirmed model interpretation became enforced policy")
	}
}

func TestTaskLongRequestDoesNotRegress(t *testing.T) {
	e, th, add := taskContextFixture(t)
	text := "Edit the document described below.\n" + strings.Repeat("Detailed supplied material.\n", 5000) + "\nDo not change implementation code."
	user := add(text)
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal("task metadata limit rejected an otherwise valid chat message", err)
	}
}

func TestTaskArchivedContext(t *testing.T) {
	e, th, add := taskContextFixture(t)
	text := strings.Repeat("α supplied context\n", 6000) + "Do not change implementation code."
	user := add(text)
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	tool := &taskContextTool{engine: e}
	ctx := context.WithValue(t.Context(), executionTurnKey{}, protocol.Turn{ID: user.TurnID, ThreadID: th.ID})
	args, _ := json.Marshal(map[string]any{"task_id": snap.Task.ID, "offset": len([]rune(text)) - 40, "limit": 100})
	raw, err := tool.Call(ctx, args)
	if err != nil || !strings.Contains(raw, "Do not change implementation code.") {
		t.Fatalf("original constraint lost: %s %v", raw, err)
	}
	args, _ = json.Marshal(map[string]any{"task_id": "foreign", "offset": 0, "limit": 100})
	if _, err := tool.Call(ctx, args); err == nil {
		t.Fatal("cross-task context read allowed")
	}
}

func TestTaskContextRemainsBounded(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Update several files")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	for i := 0; i < 8; i++ {
		args, _ := json.Marshal(map[string]string{"path": "notes.txt", "content": strings.Repeat("a", 14000)})
		if _, err := e.Store.CreateTaskOperation(t.Context(), protocol.TaskOperation{TaskID: snap.Task.ID, TurnID: user.TurnID, Tool: "file.write", Args: args}); err != nil {
			t.Fatal(err)
		}
	}
	text, err := e.taskContext(t.Context(), th.ID)
	if err != nil || len(text) > 65536 || !strings.Contains(text, "task.context") {
		t.Fatalf("bounded task context: bytes=%d error=%v", len(text), err)
	}
}
