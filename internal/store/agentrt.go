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

const runtimeAgentCols = `id, task_id, parent_agent_id, name, purpose, plan_id, plan_entry, assignment_json, capabilities_json, rejected_json, model_requested, model_actual, route_trail_json, budget_share, depth, context_mode, workspace_id, revision, state, result_json, usage_json, version, created_at, updated_at`

func scanRuntimeAgent(row interface{ Scan(...any) error }) (protocol.RuntimeAgent, error) {
	var a protocol.RuntimeAgent
	var assignment, caps, rejected, trail, result, usage, created, updated string
	err := row.Scan(&a.ID, &a.TaskID, &a.ParentAgentID, &a.Name, &a.Purpose, &a.PlanID, &a.PlanEntry, &assignment, &caps, &rejected, &a.ModelRequested, &a.ModelActual, &trail, &a.BudgetShare, &a.Depth, &a.ContextMode, &a.WorkspaceID, &a.Revision, &a.State, &result, &usage, &a.Version, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	for _, field := range []struct {
		data   string
		target any
	}{{assignment, &a.Assignment}, {caps, &a.Capabilities}, {rejected, &a.Rejected}, {trail, &a.RouteTrail}, {result, &a.Result}, {usage, &a.Usage}} {
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
	if a.Rejected == nil {
		a.Rejected = []protocol.CapabilityRejection{}
	}
	a.Version = 1
	a.CreatedAt = time.Now().UTC()
	a.UpdatedAt = a.CreatedAt
	js, err := marshalAll(a.Assignment, a.Capabilities, a.Rejected, a.RouteTrail, a.Result, a.Usage)
	if err != nil {
		return protocol.RuntimeAgent{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agents (`+runtimeAgentCols+`,call_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.TaskID, a.ParentAgentID, a.Name, a.Purpose, a.PlanID, a.PlanEntry, js[0], js[1], js[2], a.ModelRequested, a.ModelActual, js[3], a.BudgetShare, a.Depth, a.ContextMode, a.WorkspaceID, a.Revision, a.State, js[4], js[5], a.Version, FormatTime(a.CreatedAt), FormatTime(a.UpdatedAt), callID)
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

// RuntimeAgents returns a task's agents in insertion (rowid) order.
func (s *Store) RuntimeAgents(ctx context.Context, taskID string) ([]protocol.RuntimeAgent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+runtimeAgentCols+` FROM agents WHERE task_id=? ORDER BY rowid`, taskID)
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
	// Keep the inert execution mirror row (if any) in step with the agent.
	if _, err = tx.ExecContext(ctx, `UPDATE agent_children SET state=?,version=version+1,updated_at=? WHERE task_id=? AND id=? AND call_id=?`, a.State, Now(), a.TaskID, a.ID, runtimeMirrorCallID(a.ID)); err != nil {
		return err
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

// SaveAgentPlan inserts a proposed plan or revises a stored plan that is still
// proposed. It refuses (ErrAgentTransition) any State other than proposed, a
// set ApprovedAt, and saving over a plan that is approved or superseded, so an
// approved plan can never be rewritten and a superseded one never revived. It
// performs no supersede logic; use ApproveAgentPlan for approval.
func (s *Store) SaveAgentPlan(ctx context.Context, p protocol.AgentPlan) error {
	if p.ID == "" || p.ThreadID == "" {
		return fmt.Errorf("agent plan requires ID and thread ID")
	}
	if p.State == "" {
		p.State = protocol.PlanProposed
	}
	if p.State != protocol.PlanProposed || p.ApprovedAt != nil {
		return fmt.Errorf("%w: only a proposed, unapproved plan can be saved; use ApproveAgentPlan", ErrAgentTransition)
	}
	if p.Agents == nil {
		p.Agents = []protocol.PlanAgent{}
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	// Zero limits (propose_plan carries only some of them) take the defaults.
	p.Limits = p.Limits.Normalized()
	js, err := marshalAll(p.Agents, p.Limits)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_plans (`+agentPlanCols+`) VALUES(?,?,?,?,?,?,?,'')
ON CONFLICT(id) DO UPDATE SET task_id=excluded.task_id,agents_json=excluded.agents_json,limits_json=excluded.limits_json,created_at=excluded.created_at
WHERE agent_plans.state='proposed' AND agent_plans.thread_id=excluded.thread_id`,
		p.ID, p.ThreadID, p.TaskID, js[0], js[1], p.State, FormatTime(p.CreatedAt))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: plan %s exists and is not a proposed plan of this thread", ErrAgentTransition, p.ID)
	}
	return nil
}

// ApproveAgentPlan approves a proposed plan, binds it to taskID (which must be
// a task of the plan's thread) and supersedes the thread's other approved
// plans in a single transaction. Approving again for the same task is a no-op.
func (s *Store) ApproveAgentPlan(ctx context.Context, planID, taskID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var threadID, boundTask string
	var state protocol.PlanState
	if err := tx.QueryRowContext(ctx, `SELECT thread_id, state, task_id FROM agent_plans WHERE id=?`, planID).Scan(&threadID, &state, &boundTask); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	switch state {
	case protocol.PlanProposed:
	case protocol.PlanApproved:
		if boundTask == taskID {
			return nil
		}
		return fmt.Errorf("%w: plan already approved for another task", ErrAgentConflict)
	default:
		return fmt.Errorf("%w: plan is %s", ErrAgentTransition, state)
	}
	var taskThread string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM agent_tasks WHERE id=?`, taskID).Scan(&taskThread); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %s: %w", taskID, ErrNotFound)
		}
		return err
	}
	if taskThread != threadID {
		return fmt.Errorf("%w: task %s does not belong to the plan's thread", ErrAgentConflict, taskID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_plans SET state=? WHERE thread_id=? AND id<>? AND state=?`,
		protocol.PlanSuperseded, threadID, planID, protocol.PlanApproved); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_plans SET state=?,task_id=?,approved_at=? WHERE id=? AND state=?`,
		protocol.PlanApproved, taskID, Now(), planID, protocol.PlanProposed)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: plan %s is no longer proposed", ErrAgentConflict, planID)
	}
	return tx.Commit()
}

// ApproveAgentPlanWithTask is the admission boundary for a plan proposed by an
// ordinary turn. The task, its private workspace identity and the plan grant
// become visible together. A repeated approval of the same plan/task is safe.
func (s *Store) ApproveAgentPlanWithTask(ctx context.Context, planID string, task protocol.AgentTask, workspace AgentTaskWorkspace, modelScope AgentModelScope, approvalID, decidedBy string) error {
	if planID == "" || approvalID == "" || decidedBy == "" || task.ID == "" || task.ThreadID == "" || task.RootTurnID == "" || task.Activation != protocol.AgentActivationPlanApproval ||
		task.State != protocol.AgentStateRunning || task.BaseRevision == "" || task.BaseRoot == "" || task.ParentRoot == "" ||
		workspace.TaskID != task.ID || workspace.SourceRoot == "" || workspace.ExecutionRoot != task.ParentRoot ||
		workspace.SnapshotCommit != task.BaseRevision || workspace.SourceHead == "" {
		return fmt.Errorf("%w: invalid plan-approved task workspace", ErrAgentTransition)
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.UpdatedAt = task.CreatedAt
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var threadID, boundTask string
	var state protocol.PlanState
	if err = tx.QueryRowContext(ctx, `SELECT thread_id,state,task_id FROM agent_plans WHERE id=?`, planID).Scan(&threadID, &state, &boundTask); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state == protocol.PlanApproved && boundTask == task.ID {
		return nil
	}
	if state != protocol.PlanProposed || boundTask != "" || threadID != task.ThreadID {
		return fmt.Errorf("%w: plan is not an unbound proposal of this chat", ErrAgentConflict)
	}
	var approvalThread, approvalTurn, approvalNode, approvalKind, approvalStatus string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id,turn_id,node_id,kind,status FROM approvals WHERE id=?`, approvalID).Scan(&approvalThread, &approvalTurn, &approvalNode, &approvalKind, &approvalStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if approvalThread != task.ThreadID || approvalTurn != task.RootTurnID || approvalNode != planID || approvalKind != "agent_plan" || approvalStatus != "pending" {
		return fmt.Errorf("%w: plan card no longer belongs to the pending proposal", ErrAgentConflict)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_tasks (`+agentTaskCols+`)
  SELECT ?,?,?,?,?,?,?,?,?,?,? FROM threads th JOIN turns tr ON tr.thread_id=th.id
  WHERE th.id=? AND tr.id=? AND th.project_id=?
 AND NOT EXISTS (SELECT 1 FROM agent_tasks active WHERE active.thread_id=th.id AND active.state IN ('queued','running','waiting_approval'))`,
		task.ID, task.ThreadID, task.RootTurnID, task.Activation, task.State, task.BaseRevision, task.BaseRoot, task.ProjectID, FormatTime(task.CreatedAt), FormatTime(task.UpdatedAt), task.ParentRoot,
		task.ThreadID, task.RootTurnID, task.ProjectID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: task turn/project changed or another task is active", ErrAgentConflict)
	}
	if err := s.SaveAgentTaskWorkspaceTx(ctx, tx, workspace); err != nil {
		return err
	}
	scopeJSON, err := json.Marshal(modelScope)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_model_scopes(task_id,scope_json) VALUES(?,?)`, task.ID, string(scopeJSON)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_plans SET state=? WHERE thread_id=? AND id<>? AND state=?`, protocol.PlanSuperseded, threadID, planID, protocol.PlanApproved); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `UPDATE agent_plans SET state=?,task_id=?,approved_at=? WHERE id=? AND state=? AND task_id=''`, protocol.PlanApproved, task.ID, Now(), planID, protocol.PlanProposed)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: plan changed while approving", ErrAgentConflict)
	}
	res, err = tx.ExecContext(ctx, `UPDATE approvals SET status='approved',decided_by=?,decided_at=?,agent_task_id=? WHERE id=? AND status='pending'`, decidedBy, Now(), task.ID, approvalID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: plan card changed while approving", ErrAgentConflict)
	}
	return tx.Commit()
}

// ApprovedAgentPlanForTask returns the approved plan bound to a task.
func (s *Store) ApprovedAgentPlanForTask(ctx context.Context, taskID string) (protocol.AgentPlan, error) {
	return scanAgentPlan(s.DB.QueryRowContext(ctx, `SELECT `+agentPlanCols+` FROM agent_plans WHERE task_id=? AND state=? ORDER BY approved_at DESC,rowid DESC LIMIT 1`, taskID, protocol.PlanApproved))
}

// HasProposedAgentPlan reports whether a task has a plan that is still
// proposed (its card pending, revised, or declined without closing the task).
func (s *Store) HasProposedAgentPlan(ctx context.Context, taskID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM agent_plans WHERE task_id=? AND state=?`, taskID, protocol.PlanProposed).Scan(&n)
	return n > 0, err
}

// AgentPlan returns one plan by ID, whatever its state.
func (s *Store) AgentPlan(ctx context.Context, id string) (protocol.AgentPlan, error) {
	return scanAgentPlan(s.DB.QueryRowContext(ctx, `SELECT `+agentPlanCols+` FROM agent_plans WHERE id=?`, id))
}

// SupersedeProposedAgentPlans marks every still-proposed plan of a task other
// than keepID superseded (a newer proposal replaces them) and returns their
// IDs. Approved plans are never touched; a superseded plan cannot be approved
// later (ApproveAgentPlan refuses it).
func (s *Store) SupersedeProposedAgentPlans(ctx context.Context, taskID, keepID string) ([]string, error) {
	if taskID == "" {
		return nil, fmt.Errorf("supersede proposed plans: task ID required")
	}
	rows, err := s.DB.QueryContext(ctx, `UPDATE agent_plans SET state=? WHERE task_id=? AND id<>? AND state=? RETURNING id`,
		protocol.PlanSuperseded, taskID, keepID, protocol.PlanProposed)
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
	return ids, rows.Err()
}

// LatestApprovedAgentPlan returns the most recently approved plan of a thread.
func (s *Store) LatestApprovedAgentPlan(ctx context.Context, threadID string) (protocol.AgentPlan, error) {
	return scanAgentPlan(s.DB.QueryRowContext(ctx, `SELECT `+agentPlanCols+` FROM agent_plans WHERE thread_id=? AND state=? ORDER BY approved_at DESC,rowid DESC LIMIT 1`, threadID, protocol.PlanApproved))
}

const agentMessageCols = `id,task_id,from_agent_id,to_agent_id,body,call_id,created_at,delivered_at`

func scanAgentMessage(row interface{ Scan(...any) error }) (protocol.AgentMessage, error) {
	var m protocol.AgentMessage
	var created string
	var delivered sql.NullString
	err := row.Scan(&m.ID, &m.TaskID, &m.FromAgentID, &m.ToAgentID, &m.Body, &m.CallID, &created, &delivered)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	m.CreatedAt = ParseTime(created)
	if delivered.Valid && delivered.String != "" {
		t := ParseTime(delivered.String)
		m.DeliveredAt = &t
	}
	return m, nil
}

// EnqueueAgentMessage stores an undelivered message and returns it with its ID.
// A message with a CallID is idempotent per (task, call ID): a repeat returns
// the stored message (delivered or not) and queues nothing.
func (s *Store) EnqueueAgentMessage(ctx context.Context, m protocol.AgentMessage) (protocol.AgentMessage, error) {
	if m.TaskID == "" || m.ToAgentID == "" {
		return protocol.AgentMessage{}, fmt.Errorf("agent message requires task and recipient")
	}
	if m.ID == "" {
		m.ID = NewID("msg")
	}
	m.CreatedAt = time.Now().UTC()
	m.DeliveredAt = nil
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_messages (id,task_id,from_agent_id,to_agent_id,body,call_id,created_at) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(task_id, call_id) WHERE call_id != '' DO NOTHING`,
		m.ID, m.TaskID, m.FromAgentID, m.ToAgentID, m.Body, m.CallID, FormatTime(m.CreatedAt))
	if err != nil {
		return protocol.AgentMessage{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return protocol.AgentMessage{}, err
	}
	if n == 1 {
		return m, nil
	}
	return scanAgentMessage(s.DB.QueryRowContext(ctx, `SELECT `+agentMessageCols+` FROM agent_messages WHERE task_id=? AND call_id=?`, m.TaskID, m.CallID))
}

// PendingAgentMessages returns undelivered messages for an agent in insertion
// (rowid) order.
func (s *Store) PendingAgentMessages(ctx context.Context, taskID, toAgentID string) ([]protocol.AgentMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+agentMessageCols+` FROM agent_messages WHERE task_id=? AND to_agent_id=? AND delivered_at IS NULL ORDER BY rowid`, taskID, toAgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]protocol.AgentMessage, 0)
	for rows.Next() {
		m, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AcknowledgeAgentMessage marks a message delivered; repeating it is a no-op.
func (s *Store) AcknowledgeAgentMessage(ctx context.Context, taskID, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE agent_messages SET delivered_at=? WHERE task_id=? AND id=? AND delivered_at IS NULL`, Now(), taskID, id)
	return err
}
