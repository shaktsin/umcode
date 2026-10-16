package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/shaktsin/umcode/internal/protocol"
)

// AgentWorkspace records a planned checkout before creation, so setup failures
// and stopped child execution remain discoverable without trusting model paths.
type AgentWorkspace struct {
	ID, TaskID, ChildID, Kind, Root, ParentRoot, BaseRevision, SourceID string
	ResultRevision                                                      string
	ResultTree                                                          string
	ChangedPaths                                                        []string
	Cleaned                                                             bool
	Cleaning                                                            bool
}
type AgentIntegration struct {
	BaseRevision, ResultRevision string
	ChangedPaths                 []string
	CheckResults                 []protocol.AgentCheckEvidence
	Applied, Verified            bool
	PreviousAttempts             []AgentIntegration
}

func (s *Store) AgentWorkspace(ctx context.Context, id string) (AgentWorkspace, error) {
	var b string
	err := s.DB.QueryRowContext(ctx, `SELECT data_json FROM agent_workspaces WHERE id=?`, id).Scan(&b)
	if err != nil {
		return AgentWorkspace{}, normalizeNotFound(err)
	}
	var w AgentWorkspace
	err = json.Unmarshal([]byte(b), &w)
	return w, err
}
func (s *Store) AgentWorkspaceForChild(ctx context.Context, childID, kind string) (AgentWorkspace, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM agent_workspaces WHERE child_id=? AND kind=?`, childID, kind).Scan(&id)
	if err != nil {
		return AgentWorkspace{}, normalizeNotFound(err)
	}
	return s.AgentWorkspace(ctx, id)
}
func (s *Store) SaveAgentWorkspace(ctx context.Context, w AgentWorkspace) error {
	old, err := s.AgentWorkspace(ctx, w.ID)
	if err == nil && (old.TaskID != w.TaskID || old.ChildID != w.ChildID || old.Kind != w.Kind || old.Root != w.Root || old.ParentRoot != w.ParentRoot || old.BaseRevision != w.BaseRevision || old.SourceID != w.SourceID) {
		return ErrAgentTransition
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_workspaces(id,task_id,child_id,kind,data_json) SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM agent_children WHERE task_id=? AND id=?) ON CONFLICT(id) DO UPDATE SET data_json=excluded.data_json`, w.ID, w.TaskID, w.ChildID, w.Kind, string(b), w.TaskID, w.ChildID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) BindAgentReviewer(ctx context.Context, taskID, reviewerID, implementerID, revision string) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_review_sources(task_id,reviewer_id,implementer_id,revision) SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM agent_children WHERE task_id=? AND id=? AND role='reviewer') AND EXISTS(SELECT 1 FROM agent_children WHERE task_id=? AND id=? AND role='implementer' AND state='completed' AND revision=?) ON CONFLICT(reviewer_id) DO NOTHING`, taskID, reviewerID, implementerID, revision, taskID, reviewerID, taskID, implementerID, revision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrAgentConflict
	}
	return nil
}
func (s *Store) AgentReviewerSource(ctx context.Context, taskID, reviewerID string) (string, string, error) {
	var id, rev string
	err := s.DB.QueryRowContext(ctx, `SELECT implementer_id,revision FROM agent_review_sources WHERE task_id=? AND reviewer_id=?`, taskID, reviewerID).Scan(&id, &rev)
	return id, rev, normalizeNotFound(err)
}
func (s *Store) AgentIntegration(ctx context.Context, taskID, childID string) (AgentIntegration, error) {
	var b string
	err := s.DB.QueryRowContext(ctx, `SELECT data_json FROM agent_integrations WHERE task_id=? AND child_id=?`, taskID, childID).Scan(&b)
	if err != nil {
		return AgentIntegration{}, normalizeNotFound(err)
	}
	var e AgentIntegration
	err = json.Unmarshal([]byte(b), &e)
	return e, err
}
func (s *Store) SaveAgentIntegration(ctx context.Context, taskID, childID string, e AgentIntegration) error {
	old, err := s.AgentIntegration(ctx, taskID, childID)
	if err == nil && (old.BaseRevision != e.BaseRevision || old.ResultRevision != e.ResultRevision || old.Applied && !e.Applied || old.Verified && !e.Verified || !reflect.DeepEqual(old.PreviousAttempts, e.PreviousAttempts)) {
		return ErrAgentTransition
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_integrations(task_id,child_id,data_json) SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM agent_children WHERE task_id=? AND id=? AND role='implementer') ON CONFLICT(child_id) DO UPDATE SET data_json=excluded.data_json`, taskID, childID, string(b), taskID, childID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

func normalizeNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Store) AgentWorkspaces(ctx context.Context, taskID string) ([]AgentWorkspace, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT data_json FROM agent_workspaces WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentWorkspace
	for rows.Next() {
		var b string
		var w AgentWorkspace
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SupersedeAgentIntegration is a CAS for a proven-unapplied attempt. The caller
// must hold the manager's clean-parent integration lock. Prior recovery evidence
// remains in the same durable row, including across restart.
func (s *Store) SupersedeAgentIntegration(ctx context.Context, taskID, childID, expectedRevision string, next AgentIntegration) (AgentIntegration, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return next, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT data_json FROM agent_integrations WHERE task_id=? AND child_id=?`, taskID, childID).Scan(&raw)
	if err != nil {
		return next, normalizeNotFound(err)
	}
	var old AgentIntegration
	if err = json.Unmarshal([]byte(raw), &old); err != nil {
		return next, err
	}
	if old.ResultRevision != expectedRevision {
		return next, ErrAgentConflict
	}
	if old.Applied || old.Verified || old.BaseRevision != next.BaseRevision || next.ResultRevision == "" || next.ResultRevision == old.ResultRevision || next.Applied || next.Verified || len(next.PreviousAttempts) != 0 || len(next.CheckResults) != 0 {
		return next, ErrAgentTransition
	}
	next.PreviousAttempts = append([]AgentIntegration(nil), old.PreviousAttempts...)
	old.PreviousAttempts = nil
	next.PreviousAttempts = append(next.PreviousAttempts, old)
	data, err := json.Marshal(next)
	if err != nil {
		return next, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_integrations SET data_json=? WHERE task_id=? AND child_id=? AND data_json=? AND EXISTS(SELECT 1 FROM agent_children WHERE task_id=? AND id=? AND role='implementer' AND state='completed' AND revision=?)`, string(data), taskID, childID, raw, taskID, childID, next.ResultRevision)
	if err != nil {
		return next, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return next, err
	}
	if n != 1 {
		return next, ErrAgentConflict
	}
	return next, tx.Commit()
}
