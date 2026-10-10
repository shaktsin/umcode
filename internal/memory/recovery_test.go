package memory

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
			if r := s.Recover(t.Context()); !reflect.DeepEqual(r, Report{}) {
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
	if r := s.Recover(t.Context()); !reflect.DeepEqual(r, Report{}) {
		t.Fatalf("repeat=%+v", r)
	}
}

func TestRecoveryOversizedThirdStateConflicts(t *testing.T) {
	for _, stage := range []string{"initial", "recheck"} {
		t.Run(stage, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			abs := filepath.Join(req.Project.Root, "UMCODE.md")
			os.WriteFile(abs, []byte("original\n"), 0644)
			s.boundary = func(at string) error {
				if at == "write" {
					return errors.New("interrupt")
				}
				return nil
			}
			if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
				t.Fatalf("prepare=%+v", r)
			}
			ops, _ := st.ListIncompleteMemoryPromotionOps(t.Context())
			if len(ops) != 1 {
				t.Fatal("missing prepared operation")
			}
			third := strings.Repeat("external third state\n", 2000)
			s.boundary = nil
			if stage == "initial" {
				os.WriteFile(abs, []byte(third), 0644)
			} else {
				s.boundary = func(at string) error {
					if at == "recheck" {
						return os.WriteFile(abs, []byte(third), 0644)
					}
					return nil
				}
			}
			if r := s.Recover(t.Context()); r.Conflicted != 1 || r.Pending != 0 {
				t.Fatalf("recover=%+v", r)
			}
			if readTarget(t, req.Project) != third {
				t.Fatal("oversized third-state bytes changed")
			}
			if r := s.Recover(t.Context()); !reflect.DeepEqual(r, Report{}) {
				t.Fatalf("repeat=%+v", r)
			}
			op, err := st.GetMemoryPromotionOp(t.Context(), ops[0].ID)
			if err != nil || op.State != protocol.MemoryOpConflicted {
				t.Fatalf("op=%+v %v", op, err)
			}
			row, err := st.GetProjectMemory(t.Context(), op.MemoryID)
			if err != nil || row.Status != protocol.MemoryStatusConflicted {
				t.Fatalf("memory=%+v %v", row, err)
			}
			d, err := st.GetWorkDetail(t.Context(), req.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range d.Nodes {
				if n.Kind == protocol.NodeMemoryCandidate && n.Status != protocol.StatusConflicted {
					t.Fatalf("candidate=%+v", n)
				}
			}
			incomplete, err := st.ListIncompleteMemoryPromotionOps(t.Context())
			if err != nil || len(incomplete) != 0 {
				t.Fatalf("reservation retained: %+v %v", incomplete, err)
			}
			changes, err := st.ListFileChanges(t.Context(), req.Project.ID, "", "", 100)
			if err != nil || len(changes) != 0 {
				t.Fatalf("history=%+v %v", changes, err)
			}
			assertCompleted(t, st, req)
		})
	}
}

func TestPromotionInitiallyOversizedTargetRemainsPending(t *testing.T) {
	s, st, req := serviceFixture(t)
	original := strings.Repeat("user-authored content\n", 2000)
	os.WriteFile(filepath.Join(req.Project.Root, "UMCODE.md"), []byte(original), 0644)
	if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
		t.Fatalf("report=%+v", r)
	}
	if readTarget(t, req.Project) != original {
		t.Fatal("oversized original changed")
	}
	ops, err := st.ListIncompleteMemoryPromotionOps(t.Context())
	if err != nil || len(ops) != 0 {
		t.Fatalf("ops=%+v %v", ops, err)
	}
}

func TestPromotionOversizedFinalCASConflicts(t *testing.T) {
	s, st, req := serviceFixture(t)
	abs := filepath.Join(req.Project.Root, "UMCODE.md")
	os.WriteFile(abs, []byte("original\n"), 0644)
	third := strings.Repeat("external third state\n", 2000)
	s.boundary = func(at string) error {
		if at == "rename" {
			return os.WriteFile(abs, []byte(third), 0644)
		}
		return nil
	}
	if r := s.PromoteCompleted(t.Context(), req); r.Conflicted != 1 {
		t.Fatalf("report=%+v", r)
	}
	if readTarget(t, req.Project) != third {
		t.Fatal("third-state bytes changed")
	}
	if r := s.Recover(t.Context()); !reflect.DeepEqual(r, Report{}) {
		t.Fatalf("repeat=%+v", r)
	}
	ops, err := st.ListIncompleteMemoryPromotionOps(t.Context())
	if err != nil || len(ops) != 0 {
		t.Fatalf("ops=%+v %v", ops, err)
	}
}

// Catches recovery panic containment leaving the global project lock poisoned.
func TestRecoveryPanicReleasesProjectLock(t *testing.T) {
	s, st, req := serviceFixture(t)
	s.boundary = func(stage string) error {
		if stage == "write" {
			return errors.New("prepare interruption")
		}
		return nil
	}
	if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
		t.Fatalf("prepare=%+v", r)
	}
	s.boundary = func(stage string) error {
		if stage == "recheck" {
			panic("PRIVATE RECOVERY PANIC")
		}
		return nil
	}
	panicked := false
	func() { defer func() { panicked = recover() != nil }(); s.Recover(t.Context()) }()
	if !panicked {
		t.Fatal("recovery seam did not panic")
	}
	s.boundary = nil
	done := make(chan Report, 1)
	go func() { done <- s.Recover(t.Context()) }()
	select {
	case r := <-done:
		if r.Promoted != 1 {
			t.Fatalf("next recovery=%+v", r)
		}
	case <-time.After(3 * time.Second):
		// Release the old implementation's poisoned mutex before cleanup.
		projectLock(req.Project.ID).Unlock()
		<-done
		t.Fatal("recovery panic poisoned project lock")
	}
	next := addCompletedWork(t, st, req.Project, "next-command", "Use go test ./internal/work for this repository")
	go func() { done <- s.PromoteCompleted(t.Context(), next) }()
	select {
	case r := <-done:
		if r.Promoted != 1 {
			t.Fatalf("later promotion=%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("later promotion blocked after recovery panic")
	}
}

func TestRecoveryRejectsExistenceChanges(t *testing.T) {
	for _, existed := range []bool{false, true} {
		t.Run(map[bool]string{false: "independent creation", true: "independent deletion"}[existed], func(t *testing.T) {
			s, st, req := serviceFixture(t)
			abs := filepath.Join(req.Project.Root, "UMCODE.md")
			if existed {
				if err := os.WriteFile(abs, []byte{}, 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.boundary = func(at string) error {
				if at == "rename" {
					return errors.New("interrupt")
				}
				return nil
			}
			if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
				t.Fatalf("prepare=%+v", r)
			}
			if existed {
				if err := os.Remove(abs); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(abs, []byte{}, 0600); err != nil {
				t.Fatal(err)
			}
			s.boundary = nil
			if r := s.Recover(t.Context()); r.Conflicted != 1 || r.Promoted != 0 {
				t.Fatalf("existence change accepted: %+v", r)
			}
			if r := s.Recover(t.Context()); !reflect.DeepEqual(r, Report{}) {
				t.Fatalf("repeat=%+v", r)
			}
			b, err := os.ReadFile(abs)
			if existed && !os.IsNotExist(err) || !existed && (err != nil || len(b) != 0) {
				t.Fatalf("third state changed: %q %v", b, err)
			}
			changes, err := st.ListFileChanges(t.Context(), req.Project.ID, "", "", 100)
			if err != nil || len(changes) != 0 {
				t.Fatalf("history=%+v %v", changes, err)
			}
		})
	}
}
