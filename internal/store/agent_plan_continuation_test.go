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

func TestPlanContinuationRetryAfterFailedAdmission(t *testing.T) {
	s, task := runtimeFixture(t)
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE agent_tasks SET activation='plan_approval' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	first := protocol.Turn{ID: "first-continuation", ThreadID: task.ThreadID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err := s.CreatePlanContinuationTurn(t.Context(), task.ID, first); err != nil {
		t.Fatal(err)
	}
	first.Status = protocol.TurnFailed
	if err := s.FinishTurn(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	needed, err := s.AgentPlanContinuationNeedsRetry(t.Context(), task.ID)
	if err != nil || !needed {
		t.Fatalf("retry after failed turn = %v, %v", needed, err)
	}
	second := protocol.Turn{ID: "second-continuation", ThreadID: task.ThreadID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err := s.CreatePlanContinuationTurn(t.Context(), task.ID, second); err != nil {
		t.Fatal(err)
	}
	id, err := s.AgentPlanContinuation(t.Context(), task.ID)
	if err != nil || id != second.ID {
		t.Fatalf("retry marker = %s, %v", id, err)
	}
	needed, err = s.AgentPlanContinuationNeedsRetry(t.Context(), task.ID)
	if err != nil || needed {
		t.Fatalf("retry after running turn = %v, %v", needed, err)
	}
}

func TestRetryablePlanContinuationsAfterRestart(t *testing.T) {
	s, task := runtimeFixture(t)
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE agent_tasks SET activation='plan_approval' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO agent_plans(id,thread_id,task_id,agents_json,limits_json,state,created_at) VALUES(?,?,?,?,?,?,?)`,
		"approved-plan", task.ThreadID, task.ID, "[]", "{}", "approved", Now()); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.RetryablePlanContinuations(t.Context())
	if err != nil || len(candidates) != 1 || candidates[0].TaskID != task.ID || candidates[0].ThreadID != task.ThreadID {
		t.Fatalf("retryable task: %+v, %v", candidates, err)
	}
	turn := protocol.Turn{ID: "running-continuation", ThreadID: task.ThreadID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err := s.CreatePlanContinuationTurn(t.Context(), task.ID, turn); err != nil {
		t.Fatal(err)
	}
	candidates, err = s.RetryablePlanContinuations(t.Context())
	if err != nil || len(candidates) != 0 {
		t.Fatalf("running task listed for retry: %+v, %v", candidates, err)
	}
	turn.Status = protocol.TurnInterrupted
	if err := s.FinishTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	candidates, err = s.RetryablePlanContinuations(t.Context())
	if err != nil || len(candidates) != 1 || candidates[0].TaskID != task.ID {
		t.Fatalf("interrupted task not listed for retry: %+v, %v", candidates, err)
	}
}
