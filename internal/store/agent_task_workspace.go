package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// AgentTaskWorkspace records the distinct selected checkout and private task
// parent. Its identity is immutable once a task is admitted.
type AgentTaskWorkspace struct {
	TaskID         string          `json:"taskId"`
	SourceRoot     string          `json:"sourceRoot"`
	ExecutionRoot  string          `json:"executionRoot"`
	SourceHead     string          `json:"sourceHead"`
	SnapshotCommit string          `json:"snapshotCommit"`
	Manifest       json.RawMessage `json:"manifest"`
	State          string          `json:"state"`
}

func (s *Store) AgentTaskWorkspace(ctx context.Context, taskID string) (AgentTaskWorkspace, error) {
	var w AgentTaskWorkspace
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT task_id,source_root,execution_root,source_head,snapshot_commit,manifest_json,state FROM agent_task_workspaces WHERE task_id=?`, taskID).
		Scan(&w.TaskID, &w.SourceRoot, &w.ExecutionRoot, &w.SourceHead, &w.SnapshotCommit, &raw, &w.State)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	w.Manifest = json.RawMessage(raw)
	return w, nil
}

func (s *Store) SaveAgentTaskWorkspace(ctx context.Context, w AgentTaskWorkspace) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.SaveAgentTaskWorkspaceTx(ctx, tx, w); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveAgentTaskWorkspaceTx is used by plan approval after inserting the task
// in the same transaction. A replay with identical ownership is idempotent.
func (s *Store) SaveAgentTaskWorkspaceTx(ctx context.Context, tx *sql.Tx, w AgentTaskWorkspace) error {
	if w.TaskID == "" || w.SourceRoot == "" || w.ExecutionRoot == "" || w.SourceHead == "" || w.SnapshotCommit == "" || !json.Valid(w.Manifest) ||
		(w.State != "prepared" && w.State != "active" && w.State != "merged" && w.State != "conflict") {
		return fmt.Errorf("%w: invalid task workspace", ErrAgentTransition)
	}
	var got AgentTaskWorkspace
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT task_id,source_root,execution_root,source_head,snapshot_commit,manifest_json,state FROM agent_task_workspaces WHERE task_id=?`, w.TaskID).
		Scan(&got.TaskID, &got.SourceRoot, &got.ExecutionRoot, &got.SourceHead, &got.SnapshotCommit, &raw, &got.State)
	if err == nil {
		if got.SourceRoot == w.SourceRoot && got.ExecutionRoot == w.ExecutionRoot && got.SourceHead == w.SourceHead && got.SnapshotCommit == w.SnapshotCommit && bytes.Equal([]byte(raw), w.Manifest) && got.State == w.State {
			return nil
		}
		return fmt.Errorf("%w: task workspace ownership changed", ErrAgentConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_task_workspaces(task_id,source_root,execution_root,source_head,snapshot_commit,manifest_json,state,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		w.TaskID, w.SourceRoot, w.ExecutionRoot, w.SourceHead, w.SnapshotCommit, string(w.Manifest), w.State, Now())
	return err
}
