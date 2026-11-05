package store

import (
	"errors"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func count(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(t.Context(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnsureRuntimeAgentExecutionIsIdempotent(t *testing.T) {
	s, task := runtimeFixture(t)
	a, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "one"), "c1")
	if err != nil {
		t.Fatal(err)
	}
	x1, err := s.EnsureRuntimeAgentExecution(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	x2, err := s.EnsureRuntimeAgentExecution(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if x1.ThreadID == "" || x1.ThreadID != x2.ThreadID {
		t.Fatalf("threads differ: %q %q", x1.ThreadID, x2.ThreadID)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM agent_children WHERE id=?`, a.ID); n != 1 {
		t.Fatalf("mirror rows = %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM threads WHERE internal_child_id=?`, a.ID); n != 1 {
		t.Fatalf("threads = %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM agent_executions WHERE child_id=?`, a.ID); n != 1 {
		t.Fatalf("executions = %d", n)
	}
	got, err := s.AgentExecution(t.Context(), task.ID, a.ID)
	if err != nil || got.ThreadID != x1.ThreadID {
		t.Fatalf("AgentExecution = %+v %v", got, err)
	}
	if err = s.UpdateAgentExecutionModel(t.Context(), task.ID, a.ID, protocol.ModelSelection{Provider: "p", Model: "m"}); err != nil {
		t.Fatalf("model scope not usable on runtime execution: %v", err)
	}
	var role, state, callID string
	if err = s.DB.QueryRowContext(t.Context(), `SELECT role,state,call_id FROM agent_children WHERE id=?`, a.ID).Scan(&role, &state, &callID); err != nil {
		t.Fatal(err)
	}
	if role != "implementer" || state != "queued" || callID != "rt:"+a.ID {
		t.Fatalf("mirror = %s %s %s", role, state, callID)
	}
	if _, err = s.EnsureRuntimeAgentExecution(t.Context(), protocol.RuntimeAgent{ID: "ghost", TaskID: task.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
}

func TestRuntimeAgentIDForThread(t *testing.T) {
	s, task := runtimeFixture(t)
	a, _ := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "one"), "c1")
	x, err := s.EnsureRuntimeAgentExecution(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.RuntimeAgentIDForThread(t.Context(), x.ThreadID); err != nil || id != a.ID {
		t.Fatalf("got %q %v", id, err)
	}
	if _, err := s.RuntimeAgentIDForThread(t.Context(), task.ThreadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("root thread: %v", err)
	}
	if _, err := s.RuntimeAgentIDForThread(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

func TestMirrorRowIsInert(t *testing.T) {
	s, task := runtimeFixture(t)
	a, _ := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "one"), "c1")
	if _, err := s.EnsureRuntimeAgentExecution(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	kids, err := s.AgentChildren(t.Context(), task.ID)
	if err != nil || len(kids) != 0 {
		t.Fatalf("legacy AgentChildren lists mirror: %+v %v", kids, err)
	}
	if _, err = s.AgentChild(t.Context(), task.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy AgentChild sees mirror: %v", err)
	}
	// A genuine legacy child on another task is unaffected.
	legacy := seedLegacyChild(t, s, task, "legacy", protocol.AgentStateCompleted)
	kids, _ = s.AgentChildren(t.Context(), task.ID)
	if len(kids) != 1 || kids[0].ID != legacy.ID {
		t.Fatalf("legacy child lost: %+v", kids)
	}
	// Runtime reconcile and legacy reconcile both leave a consistent mirror.
	if err = s.ReconcileAgentChildren(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRuntimeAgentSyncsMirrorState(t *testing.T) {
	s, task := runtimeFixture(t)
	a, _ := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "one"), "c1")
	// No mirror yet: update must not fail or create one.
	a.State = protocol.AgentStateRunning
	if err := s.UpdateRuntimeAgent(t.Context(), a, a.Version); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM agent_children WHERE id=?`, a.ID); n != 0 {
		t.Fatal("update created a mirror")
	}
	cur, _ := s.RuntimeAgent(t.Context(), task.ID, a.ID)
	if _, err := s.EnsureRuntimeAgentExecution(t.Context(), cur); err != nil {
		t.Fatal(err)
	}
	mirror := func() string {
		var st string
		if err := s.DB.QueryRowContext(t.Context(), `SELECT state FROM agent_children WHERE id=?`, a.ID).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if mirror() != "running" {
		t.Fatalf("mirror at ensure = %s", mirror())
	}
	cur.State = protocol.AgentStateCompleted
	if err := s.UpdateRuntimeAgent(t.Context(), cur, cur.Version); err != nil {
		t.Fatal(err)
	}
	if mirror() != "completed" {
		t.Fatalf("mirror after update = %s", mirror())
	}
	// A rejected (stale) update leaves the mirror alone.
	cur.State = protocol.AgentStateFailed
	if err := s.UpdateRuntimeAgent(t.Context(), cur, 1); !errors.Is(err, ErrAgentConflict) {
		t.Fatal(err)
	}
	if mirror() != "completed" {
		t.Fatal("stale update moved mirror")
	}
}

// S3: a runtime agent's internal execution thread carries an empty approval
// mode (agents read the lead's mode live); a legacy child's thread still
// copies the lead's mode as before.
func TestRuntimeExecutionThreadHasEmptyApprovalMode(t *testing.T) {
	s, task := runtimeFixture(t)
	if err := s.UpdateThread(t.Context(), task.ThreadID, map[string]any{"approval_mode": "auto_all"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "one"), "c1")
	if err != nil {
		t.Fatal(err)
	}
	x, err := s.EnsureRuntimeAgentExecution(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if mode := threadApprovalMode(t, s, x.ThreadID); mode != "" {
		t.Fatalf("runtime execution thread approval mode = %q, want empty", mode)
	}

}

// threadApprovalMode reads a thread's stored mode, internal threads included.
func threadApprovalMode(t *testing.T, s *Store, id string) string {
	t.Helper()
	var mode string
	if err := s.DB.QueryRowContext(t.Context(), `SELECT approval_mode FROM threads WHERE id=?`, id).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}
