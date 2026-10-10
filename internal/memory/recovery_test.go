package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestRecoveryExactStatesAndIdempotence(t *testing.T) {
	for _, state := range []string{"before", "after", "third", "history_exists"} {
		t.Run(state, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			abs := filepath.Join(req.Project.Root, "UMCODE.md")
			original := "user text\n"
			os.WriteFile(abs, []byte(original), 0600)
			stop := "write"
			if state == "after" || state == "third" {
				stop = "record"
			}
			if state == "history_exists" {
				stop = "finalize"
			}
			s.boundary = func(at string) error {
				if at == stop {
					return errors.New("crash")
				}
				return nil
			}
			if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
				t.Fatalf("prepare=%+v", r)
			}
			ops, _ := st.ListIncompleteMemoryPromotionOps(t.Context())
			if len(ops) != 1 {
				t.Fatalf("ops=%+v", ops)
			}
			op := ops[0]
			if state == "third" {
				os.WriteFile(abs, []byte("external third state\n"), 0644)
			}
			s.boundary = nil
			r := s.Recover(t.Context())
			if state == "third" {
				if r.Conflicted != 1 || readTarget(t, req.Project) != "external third state\n" {
					t.Fatalf("conflict=%+v", r)
				}
			} else {
				if r.Promoted != 1 || readTarget(t, req.Project) != string(op.AfterBytes) {
					t.Fatalf("recovery=%+v", r)
				}
			}
			if r := s.Recover(t.Context()); r != (Report{}) {
				t.Fatalf("repeat=%+v", r)
			}
			changes, err := st.ListFileChanges(t.Context(), req.Project.ID, "", "", 100)
			want := 1
			if state == "third" {
				want = 0
			}
			if err != nil || len(changes) != want {
				t.Fatalf("history=%+v %v", changes, err)
			}
			assertCompleted(t, st, req)
			row, err := st.GetProjectMemory(t.Context(), op.MemoryID)
			wantStatus := protocol.MemoryStatusActive
			if state == "third" {
				wantStatus = protocol.MemoryStatusConflicted
			}
			if err != nil || row.Status != wantStatus {
				t.Fatalf("memory=%+v %v", row, err)
			}
		})
	}
}

func TestRecoveryPreparedAndFileWrittenStates(t *testing.T) {
	for _, state := range []string{protocol.MemoryOpPrepared, protocol.MemoryOpFileWritten} {
		t.Run(state, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			stage := "write"
			if state == protocol.MemoryOpFileWritten {
				stage = "finalize"
			}
			s.boundary = func(at string) error {
				if at == stage {
					return errors.New("interrupt")
				}
				return nil
			}
			if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
				t.Fatalf("report=%+v", r)
			}
			ops, _ := st.ListIncompleteMemoryPromotionOps(t.Context())
			if len(ops) != 1 {
				t.Fatal("missing op")
			}
			execService(t, st, `UPDATE memory_promotion_ops SET state=? WHERE id=?`, state, ops[0].ID)
			s.boundary = nil
			if r := s.Recover(t.Context()); r.Promoted != 1 {
				t.Fatalf("recover=%+v", r)
			}
			changes, err := st.ListFileChanges(t.Context(), req.Project.ID, "", "", 100)
			if err != nil || len(changes) != 1 || changes[0].Before != nil || changes[0].Action != protocol.FileCreated {
				t.Fatalf("creation history=%+v %v", changes, err)
			}
		})
	}
}

func TestRecoveryUnchangedDuplicateSkipsHistoryAndRename(t *testing.T) {
	s, st, req := serviceFixture(t)
	if r := s.PromoteCompleted(t.Context(), req); r.Promoted != 1 {
		t.Fatalf("initial=%+v", r)
	}
	duplicate := addCompletedWork(t, st, req.Project, "test-command", "Use go test ./... for this repository")
	s.boundary = func(at string) error {
		if at == "finalize" {
			return errors.New("crash")
		}
		return nil
	}
	if r := s.PromoteCompleted(t.Context(), duplicate); r.Pending != 1 {
		t.Fatalf("duplicate=%+v", r)
	}
	s.boundary = func(at string) error {
		if at == "create" || at == "record" {
			t.Fatalf("unchanged operation reached %s", at)
		}
		return nil
	}
	if r := s.Recover(t.Context()); r.Promoted != 1 || r.Unchanged != 1 {
		t.Fatalf("recover=%+v", r)
	}
	changes, err := st.ListFileChanges(t.Context(), req.Project.ID, "", "", 100)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes=%+v %v", changes, err)
	}
	if r := s.Recover(t.Context()); r != (Report{}) {
		t.Fatalf("repeat=%+v", r)
	}
}
