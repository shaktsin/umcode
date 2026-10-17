package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

var (
	// ErrAgentConflict means an optimistic child version no longer matches.
	ErrAgentConflict = errors.New("agent version conflict")
	// ErrAgentTransition means a lifecycle or immutable ownership change is invalid.
	ErrAgentTransition = errors.New("invalid agent transition")
)

const agentTaskCols = `id, thread_id, root_turn_id, activation, state, base_revision, base_root, project_id, created_at, updated_at, parent_root`
const agentChildCols = `id, task_id, role, assignment_json, state, revision, workspace_id, result_json, usage_json, correction_rounds, version, created_at, updated_at, model_json`

func validAgentState(state protocol.AgentState) bool {
	switch state {
	case protocol.AgentStateQueued, protocol.AgentStateRunning, protocol.AgentStateWaitingApproval,
		protocol.AgentStateCompleted, protocol.AgentStateFailed, protocol.AgentStateCancelled, protocol.AgentStateInterrupted:
		return true
	}
	return false
}

func scanAgentTask(row interface{ Scan(...any) error }) (protocol.AgentTask, error) {
	var task protocol.AgentTask
	var created, updated string
	err := row.Scan(&task.ID, &task.ThreadID, &task.RootTurnID, &task.Activation, &task.State, &task.BaseRevision, &task.BaseRoot, &task.ProjectID, &created, &updated, &task.ParentRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return task, ErrNotFound
	}
	if err != nil {
		return task, err
	}
	task.CreatedAt, task.UpdatedAt = ParseTime(created), ParseTime(updated)
	return task, nil
}

func scanAgentChild(row interface{ Scan(...any) error }) (protocol.AgentChild, error) {
	var child protocol.AgentChild
	var assignment, result, usage, created, updated, model string
	err := row.Scan(&child.ID, &child.TaskID, &child.Role, &assignment, &child.State, &child.Revision, &child.WorkspaceID, &result, &usage, &child.CorrectionRounds, &child.Version, &created, &updated, &model)
	if errors.Is(err, sql.ErrNoRows) {
		return child, ErrNotFound
	}
	if err != nil {
		return child, err
	}
	for _, field := range []struct {
		data   string
		target any
	}{{assignment, &child.Assignment}, {result, &child.Result}, {usage, &child.Usage}, {model, &child.Model}} {
		if err := json.Unmarshal([]byte(field.data), field.target); err != nil {
			return child, fmt.Errorf("decode agent child %s: %w", child.ID, err)
		}
	}
	child.CreatedAt, child.UpdatedAt = ParseTime(created), ParseTime(updated)
	return child, nil
}

// CreateAgentTask persists an admitted task. The root turn and project must
// belong to its parent thread; callers supply the stable task identity.
func (s *Store) CreateAgentTask(ctx context.Context, task protocol.AgentTask) error {
	if task.State == "" {
		task.State = protocol.AgentStateQueued
	}
	if task.ID == "" || task.ThreadID == "" || task.RootTurnID == "" || strings.TrimSpace(task.BaseRevision) == "" ||
		(task.Activation != protocol.AgentActivationExplicitUser && task.Activation != protocol.AgentActivationInstalledSkill) || !validAgentState(task.State) {
		return fmt.Errorf("%w: invalid admitted task", ErrAgentTransition)
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.UpdatedAt = task.CreatedAt
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_tasks (`+agentTaskCols+`)
  SELECT ?,?,?,?,?,?,?,?,?,?,? FROM threads th JOIN turns tr ON tr.thread_id=th.id
  WHERE th.id=? AND tr.id=? AND th.project_id=?
 AND NOT EXISTS (SELECT 1 FROM agent_tasks active WHERE active.thread_id=th.id AND active.state IN ('queued','running','waiting_approval'))`,
		task.ID, task.ThreadID, task.RootTurnID, task.Activation, task.State, task.BaseRevision, task.BaseRoot, task.ProjectID, FormatTime(task.CreatedAt), FormatTime(task.UpdatedAt), task.ParentRoot,
		task.ThreadID, task.RootTurnID, task.ProjectID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// AgentTask loads an admitted task. Task/chat authorization belongs to admission.
func (s *Store) AgentTask(ctx context.Context, id string) (protocol.AgentTask, error) {
	return scanAgentTask(s.DB.QueryRowContext(ctx, `SELECT `+agentTaskCols+` FROM agent_tasks WHERE id=?`, id))
}

// AgentTaskForRootTurn performs a durable root lookup scoped to its parent chat.
func (s *Store) AgentTaskForRootTurn(ctx context.Context, threadID, rootTurnID string) (protocol.AgentTask, error) {
	return scanAgentTask(s.DB.QueryRowContext(ctx, `SELECT `+agentTaskCols+` FROM agent_tasks WHERE thread_id=? AND root_turn_id=?`, threadID, rootTurnID))
}

// ActiveAgentTask loads the unfinished task scoped to the current chat/project.
func (s *Store) ActiveAgentTask(ctx context.Context, threadID, projectID string) (protocol.AgentTask, error) {
	return scanAgentTask(s.DB.QueryRowContext(ctx, `SELECT `+agentTaskCols+` FROM agent_tasks WHERE thread_id=? AND project_id=? AND state IN ('queued','running','waiting_approval') ORDER BY created_at LIMIT 1`, threadID, projectID))
}

// UpdateAgentTaskState checks ownership and the expected state transactionally.
// Terminal tasks cannot regain admission through this lifecycle API.
func (s *Store) UpdateAgentTaskState(ctx context.Context, taskID, threadID, projectID string, expected, next protocol.AgentState) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Revocation reduces authority and must remain possible after a parent
	// changes project. All execution/completion transitions retain live ownership.
	q := `SELECT ` + agentTaskCols + ` FROM agent_tasks WHERE id=? AND thread_id=? AND project_id=? AND EXISTS (SELECT 1 FROM threads WHERE id=?`
	args := []any{taskID, threadID, projectID, threadID}
	if next != protocol.AgentStateCancelled {
		q += ` AND project_id=?`
		args = append(args, projectID)
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, q+`)`, args...))
	if err != nil {
		return err
	}
	if task.State != expected {
		return ErrAgentConflict
	}
	active := task.State == protocol.AgentStateQueued || task.State == protocol.AgentStateRunning || task.State == protocol.AgentStateWaitingApproval
	if !validAgentState(next) || (!active && next != task.State) || (active && next != protocol.AgentStateCompleted && !validAgentTransition(task.State, next)) {
		return ErrAgentTransition
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_tasks SET state=?,updated_at=? WHERE id=? AND state=?`, next, FormatTime(time.Now().UTC()), taskID, expected)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// CreateAgentChild returns the original record for duplicate (task, call) IDs,
// regardless of a redelivered payload. New children always start queued at v1.
func (s *Store) CreateAgentChild(ctx context.Context, child protocol.AgentChild, callID string) (protocol.AgentChild, error) {
	if child.TaskID == "" || strings.TrimSpace(callID) == "" {
		return protocol.AgentChild{}, fmt.Errorf("%w: task and call IDs required", ErrAgentTransition)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	defer tx.Rollback()
	existing, err := scanAgentChild(tx.QueryRowContext(ctx, `SELECT `+agentChildCols+` FROM agent_children WHERE task_id=? AND call_id=?`, child.TaskID, callID))
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return protocol.AgentChild{}, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, `SELECT `+agentTaskCols+` FROM agent_tasks WHERE id=?`, child.TaskID))
	if err != nil {
		return protocol.AgentChild{}, err
	}
	if task.State == protocol.AgentStateCompleted || task.State == protocol.AgentStateCancelled || task.State == protocol.AgentStateFailed || task.State == protocol.AgentStateInterrupted {
		return protocol.AgentChild{}, fmt.Errorf("%w: task is terminal", ErrAgentTransition)
	}
	if child.State == "" {
		child.State = protocol.AgentStateQueued
	}
	if child.State != protocol.AgentStateQueued || (child.Role != protocol.AgentRoleImplementer && child.Role != protocol.AgentRoleReviewer) || strings.TrimSpace(child.Assignment.Prompt) == "" || child.CorrectionRounds != 0 {
		return protocol.AgentChild{}, fmt.Errorf("%w: invalid new child", ErrAgentTransition)
	}
	if child.ID == "" {
		child.ID = NewID("agt")
	}
	child.Version = 1
	child.CreatedAt = time.Now().UTC()
	child.UpdatedAt = child.CreatedAt
	assignment, err := json.Marshal(child.Assignment)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	result, err := json.Marshal(child.Result)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	usage, err := json.Marshal(child.Usage)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	model, err := json.Marshal(child.Model)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_children (`+agentChildCols+`,call_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		child.ID, child.TaskID, child.Role, string(assignment), child.State, child.Revision, child.WorkspaceID, string(result), string(usage), child.CorrectionRounds, child.Version, FormatTime(child.CreatedAt), FormatTime(child.UpdatedAt), string(model), callID)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	return child, tx.Commit()
}

// AgentChild deliberately requires both identities; foreign children are absent.
func (s *Store) AgentChild(ctx context.Context, taskID, childID string) (protocol.AgentChild, error) {
	return scanAgentChild(s.DB.QueryRowContext(ctx, `SELECT `+agentChildCols+` FROM agent_children WHERE task_id=? AND id=?`, taskID, childID))
}

// AgentChildren returns children in stable creation order, or ErrNotFound for
// an unknown task. An existing task with no children returns an empty slice.
func (s *Store) AgentChildren(ctx context.Context, taskID string) ([]protocol.AgentChild, error) {
	if _, err := s.AgentTask(ctx, taskID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+agentChildCols+` FROM agent_children WHERE task_id=? ORDER BY created_at,id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]protocol.AgentChild, 0)
	for rows.Next() {
		child, err := scanAgentChild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, child)
	}
	return out, rows.Err()
}

func validAgentTransition(from, to protocol.AgentState) bool {
	if !validAgentState(to) {
		return false
	}
	if from == to {
		return true
	}
	switch from {
	case protocol.AgentStateQueued:
		return to == protocol.AgentStateRunning || to == protocol.AgentStateFailed || to == protocol.AgentStateCancelled || to == protocol.AgentStateInterrupted
	case protocol.AgentStateRunning:
		return to == protocol.AgentStateWaitingApproval || to == protocol.AgentStateCompleted || to == protocol.AgentStateFailed || to == protocol.AgentStateCancelled || to == protocol.AgentStateInterrupted
	case protocol.AgentStateWaitingApproval:
		return to == protocol.AgentStateRunning || to == protocol.AgentStateFailed || to == protocol.AgentStateCancelled || to == protocol.AgentStateInterrupted
	case protocol.AgentStateCompleted, protocol.AgentStateFailed, protocol.AgentStateInterrupted:
		// Explicit follow-up/retry admission is the coordinator's responsibility.
		return to == protocol.AgentStateQueued
	}
	return false
}

// UpdateAgentChild updates mutable execution fields using persisted ownership,
// role, state and version. Version/timestamps in child are ignored; reload to get
// the incremented version. Assignment and role remain fixed for this identity.
func (s *Store) UpdateAgentChild(ctx context.Context, child protocol.AgentChild, expectedVersion int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := scanAgentChild(tx.QueryRowContext(ctx, `SELECT `+agentChildCols+` FROM agent_children WHERE task_id=? AND id=?`, child.TaskID, child.ID))
	if err != nil {
		return err
	}
	if old.Version != expectedVersion {
		return ErrAgentConflict
	}
	if old.Role != child.Role || !validAgentTransition(old.State, child.State) || child.CorrectionRounds < old.CorrectionRounds || child.CorrectionRounds > old.CorrectionRounds+1 || child.CorrectionRounds > 2 {
		return ErrAgentTransition
	}
	if child.State == protocol.AgentStateQueued && (old.State == protocol.AgentStateCompleted || old.State == protocol.AgentStateFailed) {
		if child.CorrectionRounds != old.CorrectionRounds+1 {
			return fmt.Errorf("%w: correction must consume a round", ErrAgentTransition)
		}
	} else if child.CorrectionRounds != old.CorrectionRounds {
		return fmt.Errorf("%w: correction rounds only change on requeue", ErrAgentTransition)
	}
	assignment, err := json.Marshal(child.Assignment)
	if err != nil {
		return err
	}
	oldAssignment, err := json.Marshal(old.Assignment)
	if err != nil {
		return err
	}
	if string(assignment) != string(oldAssignment) {
		return fmt.Errorf("%w: assignment is immutable", ErrAgentTransition)
	}
	result, err := json.Marshal(child.Result)
	if err != nil {
		return err
	}
	usage, err := json.Marshal(child.Usage)
	if err != nil {
		return err
	}
	model, err := json.Marshal(child.Model)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_children SET state=?,revision=?,workspace_id=?,result_json=?,usage_json=?,model_json=?,correction_rounds=?,version=version+1,updated_at=? WHERE task_id=? AND id=? AND version=?`,
		child.State, child.Revision, child.WorkspaceID, string(result), string(usage), string(model), child.CorrectionRounds, Now(), child.TaskID, child.ID, expectedVersion)
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
	return tx.Commit()
}

// ReconcileAgentChildren interrupts active children after restart without
// modifying results, workspace references, verification evidence or usage.
// Repeating reconciliation leaves already interrupted children untouched.
func (s *Store) ReconcileAgentChildren(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE agent_children SET state='interrupted',version=version+1,updated_at=? WHERE state IN ('queued','running','waiting_approval')`, Now()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_tasks SET state='interrupted',updated_at=? WHERE state IN ('queued','running','waiting_approval')`, Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// AgentTasksForThread includes terminal and interrupted recovery records.
func (s *Store) AgentTasksForThread(ctx context.Context, threadID string) ([]protocol.AgentTask, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+agentTaskCols+` FROM agent_tasks WHERE thread_id=? ORDER BY created_at DESC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.AgentTask{}
	for rows.Next() {
		task, err := scanAgentTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}
