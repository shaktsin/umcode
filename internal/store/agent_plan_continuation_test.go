package store

import (
	"errors"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPlanContinuationTurnSingleAdmission(t *testing.T) {
	s, task := runtimeFixture(t)
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE agent_tasks SET activation='plan_approval' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	// The continuation marker is scoped to a task's existing chat.
	turn := protocol.Turn{ID: "continuation-1", ThreadID: task.ThreadID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err := s.CreatePlanContinuationTurn(t.Context(), task.ID, turn); err != nil {
		t.Fatal(err)
	}
	id, err := s.AgentPlanContinuation(t.Context(), task.ID)
	if err != nil || id != turn.ID {
		t.Fatalf("continuation %q, %v", id, err)
	}
	other := turn
	other.ID = "continuation-2"
	if err := s.CreatePlanContinuationTurn(t.Context(), task.ID, other); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("replay: %v", err)
	}
	var n int
	if err := s.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM turns WHERE id IN (?,?)`, turn.ID, other.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("turns=%d, %v", n, err)
	}
	if _, err := s.AgentPlanContinuation(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing continuation: %v", err)
	}
}
