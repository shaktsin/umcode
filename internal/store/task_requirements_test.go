package store

import (
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"testing"
	"time"
)

func TestExplicitTaskRequirements(t *testing.T) {
	st, th := workFixture(t)
	ctx := t.Context()
	user := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, Seq: 1, Kind: "userMessage", Text: "Review first", CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: user.Text})
	if err != nil {
		t.Fatal(err)
	}
	proposal := protocol.RequirementProposal{TaskID: task.ID, ExpectedRevision: task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)}
	if _, err := st.PutTaskRequirement(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutTaskRequirement(ctx, proposal); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale requirement accepted: %v", err)
	}
	snap, err := st.ActiveExecutionTask(ctx, th.ID)
	if err != nil || snap.Task.Revision != 2 || len(snap.Requirements) != 1 {
		t.Fatalf("requirement CAS=%+v %v", snap, err)
	}
	if _, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: "new task"}); err != nil {
		t.Fatal(err)
	}
	old, err := st.GetExecutionTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	proposal.ExpectedRevision = old.Revision
	if _, err := st.PutTaskRequirement(ctx, proposal); err == nil {
		t.Fatal("superseded task accepted a new requirement")
	}
}
