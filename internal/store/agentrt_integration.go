package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Revision states. A revision is one writer agent's base->result change set
// awaiting lead-owned integration into the parent checkout.
const (
	RevisionSubmitted = "submitted"
	RevisionApplying  = "applying"
	RevisionApplied   = "applied"
	RevisionConflict  = "conflict"
	RevisionDropped   = "dropped"
)

const (
	maxRevisionReason   = 4096
	maxRevisionConflict = 64
)

// AgentRevision is a submitted writer revision and its integration evidence.
type AgentRevision struct {
	ID, TaskID, AgentID, WorkspaceID string
	BaseRevision, ResultRevision     string
	ResultTree, State, Reason        string
	Seq                              int
	ChangedPaths                     []string
	TreeBefore, TreeAfter            string
	Conflict                         []string
	Version                          int64
	CreatedAt, UpdatedAt             time.Time
}

// AgentIntegrationHead is the per-task integrated tree and its verification
// workflow state (opaque JSON; see DecodeRuntimeWorkflow).
type AgentIntegrationHead struct {
	TaskID, HeadTree string
	AppliedCount     int
	Workflow         json.RawMessage
	Version          int64
}

// RuntimeWorkflow is the decoded verification state recorded for a head tree.
type RuntimeWorkflow struct {
	HeadTree string               `json:"head_tree"`
	Checks   []AgentWorkflowCheck `json:"checks"`
}

// DecodeRuntimeWorkflow decodes AgentIntegrationHead.Workflow. Empty or nil
// input yields the zero value without error.
func DecodeRuntimeWorkflow(raw json.RawMessage) (RuntimeWorkflow, error) {
	var w RuntimeWorkflow
	if len(strings.TrimSpace(string(raw))) == 0 {
		return w, nil
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return RuntimeWorkflow{}, fmt.Errorf("decode runtime workflow: %w", err)
	}
	return w, nil
}

func validRevisionState(s string) bool {
	switch s {
	case RevisionSubmitted, RevisionApplying, RevisionApplied, RevisionConflict, RevisionDropped:
		return true
	}
	return false
}

// revisionTransitions lists the legal moves; applied and dropped are terminal.
var revisionTransitions = map[string]map[string]bool{
	RevisionSubmitted: {RevisionApplying: true, RevisionConflict: true, RevisionDropped: true},
	RevisionApplying:  {RevisionApplied: true, RevisionSubmitted: true, RevisionConflict: true},
	RevisionConflict:  {RevisionApplying: true, RevisionDropped: true},
}

func validRevisionTransition(from, to string) bool {
	return revisionTransitions[from][to]
}

const agentRevisionCols = `id, task_id, agent_id, workspace_id, base_revision, result_revision, result_tree, seq, state, reason, changed_paths_json, tree_before, tree_after, conflict_json, version, created_at, updated_at`

func scanAgentRevision(row interface{ Scan(...any) error }) (AgentRevision, error) {
	var r AgentRevision
	var paths, conflict, created, updated string
	err := row.Scan(&r.ID, &r.TaskID, &r.AgentID, &r.WorkspaceID, &r.BaseRevision, &r.ResultRevision, &r.ResultTree, &r.Seq, &r.State, &r.Reason, &paths, &r.TreeBefore, &r.TreeAfter, &conflict, &r.Version, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(paths), &r.ChangedPaths); err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(conflict), &r.Conflict); err != nil {
		return r, err
	}
	if r.ChangedPaths == nil {
		r.ChangedPaths = []string{}
	}
	if r.Conflict == nil {
		r.Conflict = []string{}
	}
	r.CreatedAt, r.UpdatedAt = ParseTime(created), ParseTime(updated)
	return r, nil
}

func boundRevisionText(reason string, conflict []string) (string, []string) {
	if len(reason) > maxRevisionReason {
		reason = reason[:maxRevisionReason]
		for len(reason) > 0 && reason[len(reason)-1]&0xC0 == 0x80 {
			reason = reason[:len(reason)-1] // do not leave a split rune
		}
	}
	if len(conflict) > maxRevisionConflict {
		conflict = conflict[:maxRevisionConflict]
	}
	if conflict == nil {
		conflict = []string{}
	}
	return reason, conflict
}

// SubmitAgentRevision records a writer revision as submitted. It is idempotent
// per (task, agent, result revision). A second distinct revision from the same
// agent is refused (ErrAgentTransition) unless every earlier one is dropped.
func (s *Store) SubmitAgentRevision(ctx context.Context, r AgentRevision) (AgentRevision, error) {
	if r.TaskID == "" || r.AgentID == "" || r.BaseRevision == "" || r.ResultRevision == "" {
		return AgentRevision{}, fmt.Errorf("%w: task, agent, base and result revision required", ErrAgentTransition)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AgentRevision{}, err
	}
	defer tx.Rollback()
	existing, err := scanAgentRevision(tx.QueryRowContext(ctx, `SELECT `+agentRevisionCols+` FROM agent_revisions WHERE task_id=? AND agent_id=? AND result_revision=?`, r.TaskID, r.AgentID, r.ResultRevision))
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return AgentRevision{}, err
	}
	var live int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_revisions WHERE task_id=? AND agent_id=? AND state != ?`, r.TaskID, r.AgentID, RevisionDropped).Scan(&live); err != nil {
		return AgentRevision{}, err
	}
	if live > 0 {
		return AgentRevision{}, fmt.Errorf("%w: agent already has a revision that is not dropped", ErrAgentTransition)
	}
	if r.ID == "" {
		r.ID = NewID("rev")
	}
	r.State = RevisionSubmitted
	r.Version = 1
	r.Reason, r.Conflict = boundRevisionText(r.Reason, r.Conflict)
	if r.ChangedPaths == nil {
		r.ChangedPaths = []string{}
	}
	r.CreatedAt = time.Now().UTC()
	r.UpdatedAt = r.CreatedAt
	js, err := marshalAll(r.ChangedPaths, r.Conflict)
	if err != nil {
		return AgentRevision{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_revisions (`+agentRevisionCols+`) VALUES(?,?,?,?,?,?,?,(SELECT COALESCE(MAX(seq),0)+1 FROM agent_revisions WHERE task_id=?),?,?,?,?,?,?,?,?,?)`,
		r.ID, r.TaskID, r.AgentID, r.WorkspaceID, r.BaseRevision, r.ResultRevision, r.ResultTree, r.TaskID, r.State, r.Reason, js[0], r.TreeBefore, r.TreeAfter, js[1], r.Version, FormatTime(r.CreatedAt), FormatTime(r.UpdatedAt))
	if err != nil {
		return AgentRevision{}, err
	}
	out, err := scanAgentRevision(tx.QueryRowContext(ctx, `SELECT `+agentRevisionCols+` FROM agent_revisions WHERE id=?`, r.ID))
	if err != nil {
		return AgentRevision{}, err
	}
	return out, tx.Commit()
}

// AgentRevisions returns a task's revisions in submission order.
func (s *Store) AgentRevisions(ctx context.Context, taskID string) ([]AgentRevision, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+agentRevisionCols+` FROM agent_revisions WHERE task_id=? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AgentRevision, 0)
	for rows.Next() {
		r, err := scanAgentRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuntimeIntegrationsToReconcile returns, in task order, the tasks whose
// runtime integration a crash may have left unfinished: a revision still
// applying, or a head whose applied count differs from its applied revisions.
func (s *Store) RuntimeIntegrationsToReconcile(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT task_id FROM agent_revisions WHERE state=?
		UNION SELECT h.task_id FROM agent_integration_head h
		WHERE h.applied_count != (SELECT COUNT(*) FROM agent_revisions r WHERE r.task_id=h.task_id AND r.state=?)
		ORDER BY 1`, RevisionApplying, RevisionApplied)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AgentRevision returns the agent's latest revision (ErrNotFound if none).
func (s *Store) AgentRevision(ctx context.Context, taskID, agentID string) (AgentRevision, error) {
	return scanAgentRevision(s.DB.QueryRowContext(ctx, `SELECT `+agentRevisionCols+` FROM agent_revisions WHERE task_id=? AND agent_id=? ORDER BY seq DESC LIMIT 1`, taskID, agentID))
}

// UpdateAgentRevision persists state, reason, changed paths, trees and conflict
// paths under optimistic versioning, enforcing the transition table. Identity
// and revision ids stay fixed.
func (s *Store) UpdateAgentRevision(ctx context.Context, r AgentRevision, expectedVersion int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := scanAgentRevision(tx.QueryRowContext(ctx, `SELECT `+agentRevisionCols+` FROM agent_revisions WHERE task_id=? AND id=?`, r.TaskID, r.ID))
	if err != nil {
		return err
	}
	if old.Version != expectedVersion {
		return ErrAgentConflict
	}
	if !validRevisionState(r.State) || !validRevisionTransition(old.State, r.State) {
		return ErrAgentTransition
	}
	r.Reason, r.Conflict = boundRevisionText(r.Reason, r.Conflict)
	if r.ChangedPaths == nil {
		r.ChangedPaths = []string{}
	}
	js, err := marshalAll(r.ChangedPaths, r.Conflict)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_revisions SET state=?,reason=?,changed_paths_json=?,tree_before=?,tree_after=?,conflict_json=?,version=version+1,updated_at=? WHERE task_id=? AND id=? AND version=?`,
		r.State, r.Reason, js[0], r.TreeBefore, r.TreeAfter, js[1], Now(), r.TaskID, r.ID, expectedVersion)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrAgentConflict
	}
	return tx.Commit()
}

// AgentIntegrationHead returns the task's integration head (ErrNotFound until
// first set).
func (s *Store) AgentIntegrationHead(ctx context.Context, taskID string) (AgentIntegrationHead, error) {
	var h AgentIntegrationHead
	var wf string
	err := s.DB.QueryRowContext(ctx, `SELECT task_id, head_tree, applied_count, workflow_json, version FROM agent_integration_head WHERE task_id=?`, taskID).
		Scan(&h.TaskID, &h.HeadTree, &h.AppliedCount, &wf, &h.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	if err != nil {
		return h, err
	}
	h.Workflow = json.RawMessage(wf)
	return h, nil
}

// SetAgentIntegrationHead creates the head (expectedVersion 0) or replaces it
// under optimistic versioning (ErrAgentConflict on mismatch or if it exists).
func (s *Store) SetAgentIntegrationHead(ctx context.Context, h AgentIntegrationHead, expectedVersion int64) error {
	if h.TaskID == "" {
		return fmt.Errorf("%w: task required", ErrAgentTransition)
	}
	wf := string(h.Workflow)
	if strings.TrimSpace(wf) == "" {
		wf = "{}"
	} else if !json.Valid([]byte(wf)) {
		return fmt.Errorf("%w: workflow is not valid JSON", ErrAgentTransition)
	}
	if expectedVersion == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_integration_head (task_id, head_tree, applied_count, workflow_json, version, updated_at) VALUES(?,?,?,?,1,?) ON CONFLICT(task_id) DO NOTHING`,
			h.TaskID, h.HeadTree, h.AppliedCount, wf, Now())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrAgentConflict
		}
		return nil
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE agent_integration_head SET head_tree=?, applied_count=?, workflow_json=?, version=version+1, updated_at=? WHERE task_id=? AND version=?`,
		h.HeadTree, h.AppliedCount, wf, Now(), h.TaskID, expectedVersion)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrAgentConflict
	}
	return nil
}
