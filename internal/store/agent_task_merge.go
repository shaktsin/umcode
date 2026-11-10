package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// TaskMergeRecord is the write-ahead merge intent and its durable outcome.
// An intent left by a crash is inspected, never automatically reapplied.
type TaskMergeRecord struct {
	TaskID     string          `json:"taskId"`
	SourceRoot string          `json:"sourceRoot"`
	HeadTree   string          `json:"headTree"`
	Plan       json.RawMessage `json:"plan"`
	Receipt    json.RawMessage `json:"receipt,omitempty"`
	State      string          `json:"state"`
	Error      string          `json:"error,omitempty"`
}

func (s *Store) TaskMerge(ctx context.Context, taskID string) (TaskMergeRecord, error) {
	var r TaskMergeRecord
	var plan, receipt string
	err := s.DB.QueryRowContext(ctx, `SELECT task_id,source_root,head_tree,plan_json,receipt_json,state,error FROM agent_task_merge_journal WHERE task_id=?`, taskID).Scan(&r.TaskID, &r.SourceRoot, &r.HeadTree, &plan, &receipt, &r.State, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Plan = json.RawMessage(plan)
	r.Receipt = json.RawMessage(receipt)
	return r, nil
}

func (s *Store) BeginTaskMerge(ctx context.Context, next TaskMergeRecord) error {
	if next.TaskID == "" || next.SourceRoot == "" || next.HeadTree == "" || !json.Valid(next.Plan) {
		return fmt.Errorf("%w: invalid merge intent", ErrAgentTransition)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var root, tree, plan, state string
	err = tx.QueryRowContext(ctx, `SELECT source_root,head_tree,plan_json,state FROM agent_task_merge_journal WHERE task_id=?`, next.TaskID).Scan(&root, &tree, &plan, &state)
	if err == nil {
		if root != next.SourceRoot || tree != next.HeadTree {
			return fmt.Errorf("%w: task merge intent differs", ErrAgentConflict)
		}
		if state == "intent" {
			if !bytes.Equal([]byte(plan), next.Plan) {
				return fmt.Errorf("%w: task merge intent differs", ErrAgentConflict)
			}
			return nil
		}
		if state != "conflict" {
			return fmt.Errorf("%w: task merge is %s", ErrAgentConflict, state)
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_task_merge_journal SET state='intent',plan_json=?,error='',updated_at=? WHERE task_id=? AND state='conflict'`, string(next.Plan), Now(), next.TaskID)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_task_merge_journal(task_id,source_root,head_tree,plan_json,state,created_at,updated_at) VALUES(?,?,?,?,'intent',?,?)`, next.TaskID, next.SourceRoot, next.HeadTree, string(next.Plan), Now(), Now())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkTaskMerge(ctx context.Context, taskID, state, reason string) error {
	if state != "conflict" && state != "ambiguous" {
		return fmt.Errorf("%w: invalid merge state", ErrAgentTransition)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE agent_task_merge_journal SET state=?,error=?,updated_at=? WHERE task_id=? AND state='intent'`, state, reason, Now(), taskID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAgentConflict
	}
	return nil
}

func (s *Store) RecordTaskMergeReceipt(ctx context.Context, taskID string, receipt json.RawMessage) error {
	if !json.Valid(receipt) {
		return fmt.Errorf("%w: invalid merge receipt", ErrAgentTransition)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE agent_task_merge_journal SET state='merged',receipt_json=?,error='',updated_at=? WHERE task_id=? AND state='intent'`, string(receipt), Now(), taskID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	existing, err := s.TaskMerge(ctx, taskID)
	if err != nil {
		return err
	}
	if existing.State == "merged" && bytes.Equal(existing.Receipt, receipt) {
		return nil
	}
	return ErrAgentConflict
}

func (s *Store) PendingTaskMerges(ctx context.Context) ([]TaskMergeRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT task_id FROM agent_task_merge_journal WHERE state IN ('intent','ambiguous') ORDER BY created_at,task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]TaskMergeRecord, 0, len(ids))
	for _, id := range ids {
		r, e := s.TaskMerge(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
