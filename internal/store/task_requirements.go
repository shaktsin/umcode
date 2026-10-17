package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"time"
)

func (s *Store) PutTaskRequirement(ctx context.Context, p protocol.RequirementProposal) (protocol.TaskRequirement, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.TaskRequirement{}, err
	}
	defer tx.Rollback()
	r, err := putTaskRequirementTx(ctx, tx, p)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func putTaskRequirementTx(ctx context.Context, tx *sql.Tx, p protocol.RequirementProposal) (protocol.TaskRequirement, error) {
	task, err := readExecutionTask(ctx, tx, p.TaskID)
	if err != nil {
		return protocol.TaskRequirement{}, err
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT current FROM engine_tasks WHERE id=?`, task.ID).Scan(&current); err != nil {
		return protocol.TaskRequirement{}, err
	}
	if current != 1 {
		return protocol.TaskRequirement{}, ErrTaskConflict
	}
	if task.Revision != p.ExpectedRevision {
		return protocol.TaskRequirement{}, ErrTaskConflict
	}
	if p.Origin != "user" && p.Origin != "policy" && p.Origin != "legacy" {
		return protocol.TaskRequirement{}, errors.New("untrusted requirement origin")
	}
	if !json.Valid(p.Scope) || !json.Valid(p.Condition) || p.OriginRef == "" {
		return protocol.TaskRequirement{}, errors.New("requirement scope, condition, and origin required")
	}
	if p.Origin == "user" {
		var owner, kind string
		if err := tx.QueryRowContext(ctx, `SELECT thread_id,kind FROM items WHERE id=?`, p.OriginRef).Scan(&owner, &kind); err != nil {
			return protocol.TaskRequirement{}, err
		}
		if owner != task.ThreadID || kind != "userMessage" {
			return protocol.TaskRequirement{}, errors.New("invalid user requirement origin")
		}
	}
	r := protocol.TaskRequirement{ID: NewID("rqr"), TaskID: task.ID, Revision: 1, Origin: p.Origin, OriginRef: p.OriginRef, Scope: p.Scope, Condition: p.Condition, Status: "active", ResourceBindings: p.ResourceBindings}
	if p.Origin == "legacy" {
		r.Status = "unresolved"
	}
	raw, err := taskJSON(r)
	if err != nil {
		return r, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_requirements(id,task_id,revision,data_json) VALUES(?,?,?,?)`, r.ID, r.TaskID, r.Revision, raw)
	if err != nil {
		return r, err
	}
	task.Revision++
	task.UpdatedAt = time.Now().UTC()
	updated, err := taskJSON(task)
	if err != nil {
		return r, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE engine_tasks SET revision=?,data_json=? WHERE id=? AND current=1`, task.Revision, updated, task.ID); err != nil {
		return r, err
	}
	return r, nil
}
func (s *Store) TaskRequirements(ctx context.Context, taskID string) ([]protocol.TaskRequirement, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT data_json FROM task_requirements WHERE task_id=? ORDER BY rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []protocol.TaskRequirement
	for rows.Next() {
		var raw []byte
		var req protocol.TaskRequirement
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		result = append(result, req)
	}
	return result, rows.Err()
}

// LinkTaskRequirementApproval binds only an engine-created approval for this
// exact requirement revision; it never decides the approval itself.
func (s *Store) LinkTaskRequirementApproval(ctx context.Context, id string, revision int, approvalID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT data_json FROM task_requirements WHERE id=?`, id).Scan(&raw); err != nil {
		return err
	}
	var req protocol.TaskRequirement
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	if req.Revision != revision {
		return ErrTaskConflict
	}
	task, err := readExecutionTask(ctx, tx, req.TaskID)
	if err != nil {
		return err
	}
	var thread, kind, node string
	var nodeRevision int
	if err := tx.QueryRowContext(ctx, `SELECT thread_id,kind,node_id,node_revision FROM approvals WHERE id=?`, approvalID).Scan(&thread, &kind, &node, &nodeRevision); err != nil {
		return err
	}
	if thread != task.ThreadID || kind != "task_requirement" || node != req.ID || nodeRevision != req.Revision {
		return errors.New("approval is not bound to this task requirement")
	}
	req.ApprovalID = approvalID
	req.Revision++
	raw, err = taskJSON(req)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_requirements SET revision=?,data_json=? WHERE id=? AND revision=?`, req.Revision, raw, req.ID, revision); err != nil {
		return err
	}
	task.Revision++
	task.UpdatedAt = time.Now().UTC()
	raw, err = taskJSON(task)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE engine_tasks SET revision=?,data_json=? WHERE id=? AND current=1`, task.Revision, raw, task.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrTaskConflict
	}
	return tx.Commit()
}
