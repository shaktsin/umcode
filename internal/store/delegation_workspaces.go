package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
func normalizeNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
