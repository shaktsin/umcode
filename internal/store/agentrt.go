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

const agentPlanCols = `id, thread_id, task_id, agents_json, limits_json, state, created_at, approved_at`

func scanAgentPlan(row interface{ Scan(...any) error }) (protocol.AgentPlan, error) {
	var p protocol.AgentPlan
	var agents, limits, created, approved string
	err := row.Scan(&p.ID, &p.ThreadID, &p.TaskID, &agents, &limits, &p.State, &created, &approved)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(agents), &p.Agents); err != nil {
		return p, fmt.Errorf("decode agent plan %s: %w", p.ID, err)
	}
	if err := json.Unmarshal([]byte(limits), &p.Limits); err != nil {
		return p, fmt.Errorf("decode agent plan %s: %w", p.ID, err)
	}
	p.CreatedAt = ParseTime(created)
	if approved != "" {
		t := ParseTime(approved)
		p.ApprovedAt = &t
	}
	return p, nil
}

// SaveAgentPlan inserts a plan or replaces the stored plan with the same ID.
func (s *Store) SaveAgentPlan(ctx context.Context, p protocol.AgentPlan) error {
	if p.ID == "" || p.ThreadID == "" {
		return fmt.Errorf("agent plan requires ID and thread ID")
	}
	if p.State == "" {
		p.State = protocol.PlanProposed
	}
	if p.Agents == nil {
		p.Agents = []protocol.PlanAgent{}
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	approved := ""
	if p.ApprovedAt != nil {
		approved = FormatTime(*p.ApprovedAt)
	}
	js, err := marshalAll(p.Agents, p.Limits)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO agent_plans (`+agentPlanCols+`) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET thread_id=excluded.thread_id,task_id=excluded.task_id,agents_json=excluded.agents_json,limits_json=excluded.limits_json,state=excluded.state,created_at=excluded.created_at,approved_at=excluded.approved_at`,
		p.ID, p.ThreadID, p.TaskID, js[0], js[1], p.State, FormatTime(p.CreatedAt), approved)
	return err
}

// ApproveAgentPlan approves a plan, binds it to taskID and supersedes the
// thread's other approved plans in a single transaction.
func (s *Store) ApproveAgentPlan(ctx context.Context, planID, taskID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var threadID string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM agent_plans WHERE id=?`, planID).Scan(&threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_plans SET state=? WHERE thread_id=? AND id<>? AND state=?`,
		protocol.PlanSuperseded, threadID, planID, protocol.PlanApproved); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_plans SET state=?,task_id=?,approved_at=? WHERE id=?`,
		protocol.PlanApproved, taskID, Now(), planID); err != nil {
		return err
	}
	return tx.Commit()
}

// ApprovedAgentPlanForTask returns the approved plan bound to a task.
func (s *Store) ApprovedAgentPlanForTask(ctx context.Context, taskID string) (protocol.AgentPlan, error) {
	return scanAgentPlan(s.DB.QueryRowContext(ctx, `SELECT `+agentPlanCols+` FROM agent_plans WHERE task_id=? AND state=? ORDER BY approved_at DESC,rowid DESC LIMIT 1`, taskID, protocol.PlanApproved))
}

// LatestApprovedAgentPlan returns the most recently approved plan of a thread.
func (s *Store) LatestApprovedAgentPlan(ctx context.Context, threadID string) (protocol.AgentPlan, error) {
	return scanAgentPlan(s.DB.QueryRowContext(ctx, `SELECT `+agentPlanCols+` FROM agent_plans WHERE thread_id=? AND state=? ORDER BY approved_at DESC,rowid DESC LIMIT 1`, threadID, protocol.PlanApproved))
}

// EnqueueAgentMessage stores an undelivered message and returns it with its ID.
func (s *Store) EnqueueAgentMessage(ctx context.Context, m protocol.AgentMessage) (protocol.AgentMessage, error) {
	if m.TaskID == "" || m.ToAgentID == "" {
		return protocol.AgentMessage{}, fmt.Errorf("agent message requires task and recipient")
	}
	if m.ID == "" {
		m.ID = NewID("msg")
	}
	m.CreatedAt = time.Now().UTC()
	m.DeliveredAt = nil
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_messages (id,task_id,from_agent_id,to_agent_id,body,created_at) VALUES(?,?,?,?,?,?)`,
		m.ID, m.TaskID, m.FromAgentID, m.ToAgentID, m.Body, FormatTime(m.CreatedAt))
	return m, err
}

// PendingAgentMessages returns undelivered messages for an agent, oldest first.
func (s *Store) PendingAgentMessages(ctx context.Context, taskID, toAgentID string) ([]protocol.AgentMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,task_id,from_agent_id,to_agent_id,body,created_at FROM agent_messages WHERE task_id=? AND to_agent_id=? AND delivered_at IS NULL ORDER BY created_at,rowid`, taskID, toAgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]protocol.AgentMessage, 0)
	for rows.Next() {
		var m protocol.AgentMessage
		var created string
		if err := rows.Scan(&m.ID, &m.TaskID, &m.FromAgentID, &m.ToAgentID, &m.Body, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = ParseTime(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// AcknowledgeAgentMessage marks a message delivered; repeating it is a no-op.
func (s *Store) AcknowledgeAgentMessage(ctx context.Context, taskID, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE agent_messages SET delivered_at=? WHERE task_id=? AND id=? AND delivered_at IS NULL`, Now(), taskID, id)
	return err
}
