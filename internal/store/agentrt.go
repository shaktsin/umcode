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

const runtimeAgentCols = `id, task_id, parent_agent_id, name, purpose, plan_id, assignment_json, capabilities_json, model_requested, model_actual, route_trail_json, budget_share, depth, context_mode, workspace_id, revision, state, result_json, usage_json, version, created_at, updated_at`

func scanRuntimeAgent(row interface{ Scan(...any) error }) (protocol.RuntimeAgent, error) {
	var a protocol.RuntimeAgent
	var assignment, caps, trail, result, usage, created, updated string
	err := row.Scan(&a.ID, &a.TaskID, &a.ParentAgentID, &a.Name, &a.Purpose, &a.PlanID, &assignment, &caps, &a.ModelRequested, &a.ModelActual, &trail, &a.BudgetShare, &a.Depth, &a.ContextMode, &a.WorkspaceID, &a.Revision, &a.State, &result, &usage, &a.Version, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	for _, field := range []struct {
		data   string
		target any
	}{{assignment, &a.Assignment}, {caps, &a.Capabilities}, {trail, &a.RouteTrail}, {result, &a.Result}, {usage, &a.Usage}} {
		if err := json.Unmarshal([]byte(field.data), field.target); err != nil {
			return a, fmt.Errorf("decode runtime agent %s: %w", a.ID, err)
		}
	}
	a.CreatedAt, a.UpdatedAt = ParseTime(created), ParseTime(updated)
	return a, nil
}

func marshalAll(vals ...any) ([]string, error) {
	out := make([]string, len(vals))
	for i, v := range vals {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[i] = string(b)
	}
	return out, nil
}

// CreateRuntimeAgent persists a new queued agent at version 1. It is idempotent
// per (task, callID): a repeated call returns the original agent unchanged.
func (s *Store) CreateRuntimeAgent(ctx context.Context, a protocol.RuntimeAgent, callID string) (protocol.RuntimeAgent, error) {
	if a.TaskID == "" || strings.TrimSpace(callID) == "" || strings.TrimSpace(a.Name) == "" {
		return protocol.RuntimeAgent{}, fmt.Errorf("%w: task, call ID and name required", ErrAgentTransition)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RuntimeAgent{}, err
	}
	defer tx.Rollback()
	existing, err := scanRuntimeAgent(tx.QueryRowContext(ctx, `SELECT `+runtimeAgentCols+` FROM agents WHERE task_id=? AND call_id=?`, a.TaskID, callID))
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return protocol.RuntimeAgent{}, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, `SELECT `+agentTaskCols+` FROM agent_tasks WHERE id=?`, a.TaskID))
	if err != nil {
		return protocol.RuntimeAgent{}, err
	}
	switch task.State {
	case protocol.AgentStateCompleted, protocol.AgentStateCancelled, protocol.AgentStateFailed, protocol.AgentStateInterrupted:
		return protocol.RuntimeAgent{}, fmt.Errorf("%w: task is terminal", ErrAgentTransition)
	}
	if a.State == "" {
		a.State = protocol.AgentStateQueued
	}
	if a.State != protocol.AgentStateQueued {
		return protocol.RuntimeAgent{}, fmt.Errorf("%w: new agent must be queued", ErrAgentTransition)
	}
	if a.ID == "" {
		a.ID = NewID("agt")
	}
	if a.RouteTrail == nil {
		a.RouteTrail = []string{}
	}
	a.Version = 1
	a.CreatedAt = time.Now().UTC()
	a.UpdatedAt = a.CreatedAt
	js, err := marshalAll(a.Assignment, a.Capabilities, a.RouteTrail, a.Result, a.Usage)
	if err != nil {
		return protocol.RuntimeAgent{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agents (`+runtimeAgentCols+`,call_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.TaskID, a.ParentAgentID, a.Name, a.Purpose, a.PlanID, js[0], js[1], a.ModelRequested, a.ModelActual, js[2], a.BudgetShare, a.Depth, a.ContextMode, a.WorkspaceID, a.Revision, a.State, js[3], js[4], a.Version, FormatTime(a.CreatedAt), FormatTime(a.UpdatedAt), callID)
	if err != nil {
		return protocol.RuntimeAgent{}, err
	}
	return a, tx.Commit()
}

// RuntimeAgentForCall returns the agent created for a (task, callID) pair.
func (s *Store) RuntimeAgentForCall(ctx context.Context, taskID, callID string) (protocol.RuntimeAgent, error) {
	return scanRuntimeAgent(s.DB.QueryRowContext(ctx, `SELECT `+runtimeAgentCols+` FROM agents WHERE task_id=? AND call_id=?`, taskID, callID))
}

// RuntimeAgent requires both identities; foreign agents are absent.
func (s *Store) RuntimeAgent(ctx context.Context, taskID, id string) (protocol.RuntimeAgent, error) {
	return scanRuntimeAgent(s.DB.QueryRowContext(ctx, `SELECT `+runtimeAgentCols+` FROM agents WHERE task_id=? AND id=?`, taskID, id))
}

// RuntimeAgents returns a task's agents in creation order.
func (s *Store) RuntimeAgents(ctx context.Context, taskID string) ([]protocol.RuntimeAgent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+runtimeAgentCols+` FROM agents WHERE task_id=? ORDER BY created_at,rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]protocol.RuntimeAgent, 0)
	for rows.Next() {
		a, err := scanRuntimeAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateRuntimeAgent updates the mutable fields (state, model_actual, route
// trail, workspace, revision, result, usage) under optimistic versioning and
// bumps the version. Identity, assignment and capabilities stay fixed.
func (s *Store) UpdateRuntimeAgent(ctx context.Context, a protocol.RuntimeAgent, expectedVersion int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := scanRuntimeAgent(tx.QueryRowContext(ctx, `SELECT `+runtimeAgentCols+` FROM agents WHERE task_id=? AND id=?`, a.TaskID, a.ID))
	if err != nil {
		return err
	}
	if old.Version != expectedVersion {
		return ErrAgentConflict
	}
	if !validAgentTransition(old.State, a.State) {
		return ErrAgentTransition
	}
	if a.RouteTrail == nil {
		a.RouteTrail = []string{}
	}
	js, err := marshalAll(a.RouteTrail, a.Result, a.Usage)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agents SET state=?,model_actual=?,route_trail_json=?,workspace_id=?,revision=?,result_json=?,usage_json=?,version=version+1,updated_at=? WHERE task_id=? AND id=? AND version=?`,
		a.State, a.ModelActual, js[0], a.WorkspaceID, a.Revision, js[1], js[2], Now(), a.TaskID, a.ID, expectedVersion)
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

// ReconcileRuntimeAgents interrupts active agents after a restart without
// touching results, workspaces or usage.
func (s *Store) ReconcileRuntimeAgents(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE agents SET state='interrupted',version=version+1,updated_at=? WHERE state IN ('queued','running','waiting_approval')`, Now())
	return err
}
