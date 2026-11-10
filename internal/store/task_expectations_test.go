package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func expectationFixture(t *testing.T) (*Store, protocol.ExecutionTask, protocol.Item) {
	t.Helper()
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "expectations"})
	if err != nil {
		t.Fatal(err)
	}
	turn := protocol.Turn{ID: NewID("trn"), ThreadID: th.ID, Status: "running", StartedAt: time.Now()}
	if err := st.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "Save notes.txt", CreatedAt: time.Now()}
	if err := st.SaveItem(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(t.Context(), protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: user.Text})
	if err != nil {
		t.Fatal(err)
	}
	return st, task, user
}
func expectationProposal(user protocol.Item) protocol.TaskExpectationProposal {
	return protocol.TaskExpectationProposal{Source: protocol.TaskSourceSpan{ItemID: user.ID, Start: 0, End: 14}, Kind: "mutation", Predicate: "file_content", Resource: json.RawMessage(`{"type":"file","path":"notes.txt"}`)}
}

// Catches lost outcome contracts, non-atomic CAS and resurrected legacy intent.
func TestTaskExpectationsPersistence(t *testing.T) {
	st, task, user := expectationFixture(t)
	got, err := st.SetTaskExpectations(t.Context(), task.ID, task.Revision, user.ID, user.TurnID, []protocol.TaskExpectationProposal{expectationProposal(user)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.ExpectationsVersion != 1 || len(got.Expectations) != 1 || got.Expectations[0].Status != "pending" {
		t.Fatalf("contract=%+v", got)
	}
	first := got.Expectations[0]
	again, err := st.SetTaskExpectations(t.Context(), task.ID, got.Task.Revision, user.ID, user.TurnID, []protocol.TaskExpectationProposal{expectationProposal(user)})
	if err != nil || len(again.Expectations) != 1 || again.Task.Revision != got.Task.Revision {
		t.Fatalf("not idempotent: %+v %v", again, err)
	}
	if _, err = st.SetTaskExpectations(t.Context(), task.ID, task.Revision, user.ID, user.TurnID, []protocol.TaskExpectationProposal{expectationProposal(user)}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale=%v", err)
	}
	path := ""
	if err := st.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snap, err := reopened.ActiveExecutionTask(t.Context(), task.ThreadID)
	if err != nil || len(snap.Expectations) != 1 || snap.Expectations[0].ID != first.ID || snap.Expectations[0].Source.End != 14 || string(snap.Expectations[0].Resource) != `{"type":"file","path":"notes.txt"}` {
		t.Fatalf("reopen=%+v %v", snap, err)
	}
}
func TestTaskExpectationLimits(t *testing.T) {
	for _, mode := range []string{"foreign_origin", "bad_span", "too_many", "bad_resource", "forged_evidence", "divergent_batch"} {
		t.Run(mode, func(t *testing.T) {
			st, task, user := expectationFixture(t)
			p := expectationProposal(user)
			origin := user.ID
			batch := []protocol.TaskExpectationProposal{p}
			switch mode {
			case "foreign_origin":
				origin = "missing"
			case "bad_span":
				batch[0].Source.End = 999
			case "too_many":
				batch = make([]protocol.TaskExpectationProposal, 17)
				for i := range batch {
					batch[i] = p
				}
			case "bad_resource":
				batch[0].Resource = json.RawMessage(`{"type":"file","path":"notes.txt","approved":true}`)
			}
			snap, err := st.SetTaskExpectations(t.Context(), task.ID, task.Revision, origin, user.TurnID, batch)
			if mode == "forged_evidence" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = st.ResolveTaskExpectation(t.Context(), snap.Expectations[0].ID, 1, []string{user.ID})
			}
			if mode == "divergent_batch" {
				if err != nil {
					t.Fatal(err)
				}
				batch[0].Kind = "inspection"
				batch[0].Predicate = "read"
				_, err = st.SetTaskExpectations(t.Context(), task.ID, snap.Task.Revision, origin, user.TurnID, batch)
			}
			if err == nil {
				t.Fatal("invalid contract accepted")
			}
			if mode != "forged_evidence" && mode != "divergent_batch" {
				unchanged, _ := st.ActiveExecutionTask(t.Context(), task.ThreadID)
				if len(unchanged.Expectations) != 0 || unchanged.Task.ExpectationsVersion != 0 {
					t.Fatal("partial batch persisted")
				}
			}
		})
	}
}
func TestTaskCorrectionReservation(t *testing.T) {
	st, _, user := expectationFixture(t)
	accepted := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, ok, err := st.ReserveTaskCorrection(context.Background(), user.TurnID)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				accepted++
				mu.Unlock()
				if n < 1 || n > 2 {
					t.Errorf("reservation=%d", n)
				}
			}
		}()
	}
	wg.Wait()
	if accepted != 2 {
		t.Fatalf("accepted=%d want=2", accepted)
	}
	_, ok, err := st.ReserveTaskCorrection(t.Context(), user.TurnID)
	if err != nil || ok {
		t.Fatalf("budget reset: %v %v", ok, err)
	}
	var dbPath string
	if err := st.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, ok, err := reopened.ReserveTaskCorrection(t.Context(), user.TurnID); err != nil || ok {
		t.Fatal("reopen reset correction allowance", err)
	}
	next := protocol.Turn{ID: NewID("trn"), ThreadID: user.ThreadID, Status: "running", StartedAt: time.Now()}
	if err := reopened.CreateTurn(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if n, ok, err := reopened.ReserveTaskCorrection(t.Context(), next.ID); err != nil || !ok || n != 1 {
		t.Fatal("new turn did not receive its own allowance", err)
	}
	st = reopened
	if _, _, err = st.ReserveTaskCorrection(t.Context(), "foreign"); err == nil {
		t.Fatal("unknown turn accepted")
	}
}
