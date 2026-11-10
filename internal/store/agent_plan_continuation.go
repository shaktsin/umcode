package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// AgentPlanContinuation returns the sole admitted continuation turn for a
// plan-approved task. A missing row means the approval still needs Continue.
func (s *Store) AgentPlanContinuation(ctx context.Context, taskID string) (string, error) {
	var turnID string
	err := s.DB.QueryRowContext(ctx, `SELECT turn_id FROM agent_plan_continuations WHERE task_id=?`, taskID).Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return turnID, err
}

// AgentPlanContinuationNeedsRetry reports whether no continuation exists or
// its admission failed. A running or completed turn is never replayed.
func (s *Store) AgentPlanContinuationNeedsRetry(ctx context.Context, taskID string) (bool, error) {
	var status string
	err := s.DB.QueryRowContext(ctx, `SELECT tr.status FROM agent_plan_continuations c JOIN turns tr ON tr.id=c.turn_id WHERE c.task_id=?`, taskID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return status == protocol.TurnFailed || status == protocol.TurnInterrupted, nil
}

type PlanContinuationCandidate struct {
	TaskID   string
	ThreadID string
}

// RetryablePlanContinuations finds approved active tasks whose continuation
// was never admitted or whose previous turn was interrupted or failed.
func (s *Store) RetryablePlanContinuations(ctx context.Context) ([]PlanContinuationCandidate, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id,t.thread_id FROM agent_tasks t
		JOIN agent_plans p ON p.task_id=t.id AND p.state='approved'
		LEFT JOIN agent_plan_continuations c ON c.task_id=t.id
		LEFT JOIN turns tr ON tr.id=c.turn_id
		WHERE t.activation='plan_approval' AND t.state IN ('queued','running','waiting_approval')
		AND (c.task_id IS NULL OR tr.status IN ('failed','interrupted'))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []PlanContinuationCandidate
	for rows.Next() {
		var candidate PlanContinuationCandidate
		if err := rows.Scan(&candidate.TaskID, &candidate.ThreadID); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// CreatePlanContinuationTurn atomically reserves and creates the one turn
// that can continue a prompt-first approved task. Replays cannot create a
// second turn, even when they supply a different turn ID.
func (s *Store) CreatePlanContinuationTurn(ctx context.Context, taskID string, turn protocol.Turn) error {
	if taskID == "" || turn.ID == "" || turn.ThreadID == "" || turn.Status != protocol.TurnRunning {
		return fmt.Errorf("%w: invalid plan continuation turn", ErrAgentTransition)
	}
	if turn.StartedAt.IsZero() {
		turn.StartedAt = time.Now().UTC()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var threadID, activation, state string
	err = tx.QueryRowContext(ctx, `SELECT thread_id,activation,state FROM agent_tasks WHERE id=?`, taskID).Scan(&threadID, &activation, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if threadID != turn.ThreadID || activation != string(protocol.AgentActivationPlanApproval) ||
		(state != string(protocol.AgentStateRunning) && state != string(protocol.AgentStateQueued) && state != string(protocol.AgentStateWaitingApproval)) {
		return fmt.Errorf("%w: task does not admit a plan continuation", ErrAgentTransition)
	}
	var existing, priorStatus string
	err = tx.QueryRowContext(ctx, `SELECT c.turn_id,tr.status FROM agent_plan_continuations c JOIN turns tr ON tr.id=c.turn_id WHERE c.task_id=?`, taskID).Scan(&existing, &priorStatus)
	if err == nil {
		if priorStatus != protocol.TurnFailed && priorStatus != protocol.TurnInterrupted {
			return fmt.Errorf("%w: task already continued in turn %s", ErrAgentConflict, existing)
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO turns (id,thread_id,status,sel_provider,sel_model,sel_complexity,sel_credential,res_provider,res_model,res_complexity,res_credential,auto_picked,error,started_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		turn.ID, turn.ThreadID, turn.Status,
		turn.Selection.Provider, turn.Selection.Model, string(turn.Selection.Complexity), turn.Selection.CredentialID,
		turn.Resolved.Provider, turn.Resolved.Model, string(turn.Resolved.Complexity), turn.Resolved.CredentialID,
		b2i(turn.AutoPicked), turn.Error, FormatTime(turn.StartedAt))
	if err != nil {
		return err
	}
	if existing == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_plan_continuations(task_id,turn_id,created_at) VALUES(?,?,?)`, taskID, turn.ID, Now())
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE agent_plan_continuations SET turn_id=?,created_at=? WHERE task_id=? AND turn_id=?`, turn.ID, Now(), taskID, existing)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
