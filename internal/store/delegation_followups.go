package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

func (s *Store) AgentChildForCall(ctx context.Context, taskID, callID string) (protocol.AgentChild, error) {
	return scanAgentChild(s.DB.QueryRowContext(ctx, `SELECT `+agentChildCols+` FROM agent_children WHERE task_id=? AND call_id=?`, taskID, callID))
}
func (s *Store) AgentFollowup(ctx context.Context, taskID, childID, callID string) (protocol.AgentFollowup, error) {
	var f protocol.AgentFollowup
	err := s.DB.QueryRowContext(ctx, `SELECT call_id,message,round FROM agent_followups WHERE task_id=? AND child_id=? AND call_id=?`, taskID, childID, callID).Scan(&f.CallID, &f.Message, &f.Round)
	return f, agentFollowupError(err)
}

// EnqueueAgentFollowup atomically preserves idempotency, consumes a correction
// round, and optionally requeues a stopped child. Original assignment is fixed.
func (s *Store) EnqueueAgentFollowup(ctx context.Context, taskID, childID, callID, message string) (protocol.AgentChild, error) {
	if strings.TrimSpace(callID) == "" || strings.TrimSpace(message) == "" {
		return protocol.AgentChild{}, ErrAgentTransition
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	defer tx.Rollback()
	child, err := scanAgentChild(tx.QueryRowContext(ctx, `SELECT `+agentChildCols+` FROM agent_children WHERE task_id=? AND id=?`, taskID, childID))
	if err != nil {
		return child, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM agent_followups WHERE task_id=? AND child_id=? AND call_id=?`, taskID, childID, callID).Scan(&exists)
	if err == nil {
		return child, tx.Commit()
	}
	if !errors.Is(agentFollowupError(err), ErrNotFound) {
		return child, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, `SELECT `+agentTaskCols+` FROM agent_tasks WHERE id=?`, taskID))
	if err != nil {
		return child, err
	}
	if task.State != protocol.AgentStateRunning && task.State != protocol.AgentStateQueued && task.State != protocol.AgentStateWaitingApproval {
		return child, ErrAgentTransition
	}
	if child.CorrectionRounds >= 2 || child.State == protocol.AgentStateCancelled || child.State == protocol.AgentStateInterrupted {
		return child, ErrAgentTransition
	}
	if child.State == protocol.AgentStateCompleted || child.State == protocol.AgentStateFailed {
		child.State = protocol.AgentStateQueued
	}
	child.CorrectionRounds++
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_followups(task_id,child_id,call_id,message,round,created_at) VALUES(?,?,?,?,?,?)`, taskID, childID, callID, message, child.CorrectionRounds, Now())
	if err != nil {
		return child, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_children SET state=?,correction_rounds=?,version=version+1,updated_at=? WHERE task_id=? AND id=? AND version=?`, child.State, child.CorrectionRounds, Now(), taskID, childID, child.Version)
	if err != nil {
		return child, err
	}
	child, err = scanAgentChild(tx.QueryRowContext(ctx, `SELECT `+agentChildCols+` FROM agent_children WHERE task_id=? AND id=?`, taskID, childID))
	if err != nil {
		return child, err
	}
	return child, tx.Commit()
}
func (s *Store) PendingAgentFollowups(ctx context.Context, taskID, childID string) ([]protocol.AgentFollowup, error) {
	if _, err := s.AgentChild(ctx, taskID, childID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT call_id,message,round FROM agent_followups WHERE task_id=? AND child_id=? AND delivered=0 ORDER BY round`, taskID, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]protocol.AgentFollowup, 0)
	for rows.Next() {
		var f protocol.AgentFollowup
		if err = rows.Scan(&f.CallID, &f.Message, &f.Round); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Store) AcknowledgeAgentFollowup(ctx context.Context, taskID, childID, callID string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE agent_followups SET delivered=1 WHERE task_id=? AND child_id=? AND call_id=?`, taskID, childID, callID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func agentFollowupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
