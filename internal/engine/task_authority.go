package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

type taskActionError struct{ Failure protocol.ActionFailure }

func (e *taskActionError) Error() string { raw, _ := json.Marshal(e.Failure); return string(raw) }
func taskDenied(op protocol.TaskOperation, category, message, next string) protocol.AuthorityDecision {
	return protocol.AuthorityDecision{Outcome: "deny", Failure: &protocol.ActionFailure{Category: category, OperationID: op.ID, Message: message, NextStep: next}}
}
func requirementApplies(ctx context.Context, scope taskRequirementScope, op protocol.TaskOperation) bool {
	if workflowDiscoveryTool(op.Tool) {
		return false
	}
	knownFile := op.Tool == "file.write" || op.Tool == "file.edit"
	if len(scope.Tools) > 0 {
		matched := false
		for _, name := range scope.Tools {
			if name == op.Tool {
				matched = true
				break
			}
		}
		// Opaque commands and plugin actions cannot prove they avoid the effect.
		if !matched && knownFile {
			return false
		}
	}
	if len(scope.Paths) > 0 && knownFile {
		var args struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(op.Args, &args) != nil {
			return true
		}
		canonical := func(path string) (string, error) {
			if s := tools.ScopeFrom(ctx); s != nil {
				resolved, err := s.Resolve(path)
				if err != nil {
					return "", err
				}
				return pathutil.Resolved(resolved), nil
			}
			if filepath.IsAbs(path) {
				return "", errors.New("absolute resource without project scope")
			}
			return filepath.Clean(path), nil
		}
		target, err := canonical(args.Path)
		if err != nil {
			return true
		}
		for _, p := range scope.Paths {
			restricted, err := canonical(p)
			if err != nil {
				return true
			}
			if target == restricted || strings.HasPrefix(target, restricted+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	return true
}
func (e *Engine) requirementApproval(ctx context.Context, task protocol.ExecutionTask, req protocol.TaskRequirement) (protocol.Approval, bool, error) {
	id := req.ApprovalID
	if id == "" {
		node := req.ID
		legacyNode := ""
		if req.Origin == "legacy" {
			legacyNode = req.OriginRef
		}
		err := e.Store.DB.QueryRowContext(ctx, `SELECT id FROM approvals WHERE thread_id=? AND ((kind='task_requirement' AND node_id=?) OR (kind='workflow' AND node_id=? AND work_id=?)) ORDER BY created_at DESC,rowid DESC LIMIT 1`, task.ThreadID, node, legacyNode, task.WorkID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.Approval{}, false, nil
		}
		if err != nil {
			return protocol.Approval{}, false, err
		}
	}
	a, err := e.Store.GetApproval(ctx, id)
	if err != nil {
		return a, false, err
	}
	if a.ThreadID != task.ThreadID {
		return a, false, errors.New("approval thread mismatch")
	}
	if req.Origin == "legacy" && a.Kind == "workflow" {
		if a.Kind != "workflow" || a.NodeID != req.OriginRef || a.WorkID != task.WorkID {
			return a, false, errors.New("legacy approval scope mismatch")
		}
		var revision int
		if err := e.Store.DB.QueryRowContext(ctx, `SELECT revision FROM work_nodes WHERE id=? AND work_id=?`, req.OriginRef, task.WorkID).Scan(&revision); err != nil {
			return a, false, err
		}
		if a.NodeRevision != revision && a.NodeRevision != revision-1 {
			return a, false, nil
		}
	} else {
		revision := req.Revision
		if req.ApprovalID != "" {
			revision--
		}
		if a.Kind != "task_requirement" || a.NodeID != req.ID || a.NodeRevision != revision {
			return a, false, nil
		}
	}
	return a, true, nil
}
func (e *Engine) checkTaskAuthority(ctx context.Context, th protocol.Thread, snapshot protocol.TaskSnapshot, op protocol.TaskOperation) (protocol.AuthorityDecision, error) {
	latest, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		return protocol.AuthorityDecision{}, err
	}
	if e.Cfg != nil && e.Cfg.Policy.RequireWorkflowApproval {
		found := false
		for _, r := range latest.Requirements {
			if r.Origin == "policy" && r.OriginRef == "policy.require_workflow_approval" {
				found = true
			}
		}
		if !found {
			if _, err := e.Store.PutTaskRequirement(ctx, protocol.RequirementProposal{TaskID: latest.Task.ID, ExpectedRevision: latest.Task.Revision, Origin: "policy", OriginRef: "policy.require_workflow_approval", Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)}); err != nil {
				return protocol.AuthorityDecision{}, err
			}
			latest, err = e.Store.ActiveExecutionTask(ctx, th.ID)
			if err != nil {
				return protocol.AuthorityDecision{}, err
			}
		}
	}
	if latest.Task.ID != snapshot.Task.ID || op.TaskID != latest.Task.ID {
		return taskDenied(op, "stale_state", "The active task changed.", "Refresh the task before proposing another action."), nil
	}
	if op.ArgsRef != "" {
		args, argsErr := e.operationArgs(ctx, op)
		if argsErr != nil {
			return protocol.AuthorityDecision{}, argsErr
		}
		op.Args = args
	}
	current, err := e.Store.GetThread(ctx, th.ID)
	if err != nil {
		return protocol.AuthorityDecision{}, err
	}
	if current.ProjectID != latest.Task.ProjectID {
		return taskDenied(op, "policy_denied", "The task's project changed.", "Confirm a new task in the current project."), nil
	}
	if latest.Task.Status == "cancelled" || latest.Task.Status == "superseded" || latest.Task.Status == "completed" && !workflowDiscoveryTool(op.Tool) {
		return taskDenied(op, "policy_denied", "This task is no longer active.", "Explicitly resume or start a new task."), nil
	}
	if scope := tools.ScopeFrom(ctx); scope != nil {
		if op.Tool == "file.write" || op.Tool == "file.edit" || op.Tool == "file.read" {
			var args struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(op.Args, &args) == nil && args.Path != "" {
				if _, err := scope.Resolve(args.Path); err != nil {
					return taskDenied(op, "policy_denied", err.Error(), "Keep the action within the current project permission scope."), nil
				}
			}
		}
		if scope.ReadOnly && !workflowDiscoveryTool(op.Tool) {
			return taskDenied(op, "policy_denied", "This execution scope is read-only.", "Use read tools or obtain a writable scope."), nil
		}
		if current.ProjectID != "" && e.Projects != nil {
			project, err := e.Projects.Get(ctx, current.ProjectID)
			if err != nil {
				return protocol.AuthorityDecision{}, err
			}
			sourceRoot := scope.ProjectRoot
			if sourceRoot == "" {
				sourceRoot = scope.Root
			}
			if pathutil.Resolved(project.Root) != pathutil.Resolved(sourceRoot) {
				return taskDenied(op, "policy_denied", "The project folder changed before execution.", "Refresh the project and retry."), nil
			}
		}
	}
	if !workflowDiscoveryTool(op.Tool) {
		for _, past := range latest.Operations {
			if past.Status == "outcome_unknown" && past.ResolvedByID == "" {
				d := taskDenied(op, "outcome_unknown", "A previous action's effects are uncertain.", "Inspect and verify its effects before any further mutation; do not replay it.")
				d.Failure.BlockerID = past.ID
				return d, nil
			}
		}
	}
	result := protocol.AuthorityDecision{Outcome: "allow"}
	for _, req := range latest.Requirements {
		var scope taskRequirementScope
		var condition taskRequirementCondition
		if err := strictTaskJSON(req.Scope, &scope); err != nil {
			return result, err
		}
		if err := strictTaskJSON(req.Condition, &condition); err != nil {
			return result, err
		}
		if !requirementApplies(ctx, scope, op) || taskArtifactExcepted(ctx, req, scope, op) {
			continue
		}
		// Configured workflow review is enforced by the revision-checked work graph,
		// including its original approval transport and replacement history.
		policyReviewBinding := ""
		if req.Origin == "policy" && req.OriginRef == "policy.require_workflow_approval" {
			if e.Cfg == nil || !e.Cfg.Policy.RequireWorkflowApproval {
				continue
			}
			binding, reviewed, _, reviewErr := e.configuredWorkReview(ctx, th.ID)
			if reviewErr != nil {
				return result, reviewErr
			}
			if reviewed {
				continue
			}
			policyReviewBinding = binding
		}
		if condition.Type == "no_actions" {
			d := taskDenied(op, "workflow_requirement", "An explicit task requirement prohibits this action.", "Clarify the task constraint with the user; changing tools cannot override it.")
			d.RequirementIDs = []string{req.ID}
			d.Failure.BlockerID = req.ID
			return d, nil
		}
		if condition.Type != "approval_before_actions" {
			return taskDenied(op, "workflow_requirement", "This task requirement cannot be resolved automatically.", "Review the requirement with the user."), nil
		}
		approval, found, err := e.requirementApproval(ctx, latest.Task, req)
		if err != nil {
			return result, err
		}
		if found && policyReviewBinding != "" && approval.Status == "approved" {
			var payload struct {
				WorkReviewBinding string `json:"work_review_binding"`
			}
			found = json.Unmarshal(approval.Args, &payload) == nil && payload.WorkReviewBinding == policyReviewBinding
		}
		if found {
			switch approval.Status {
			case "approved":
				result.ApprovalIDs = append(result.ApprovalIDs, approval.ID)
				continue
			case "denied":
				d := taskDenied(op, "approval_rejected", "The user rejected the required approval.", "Stop this action. A retry or another tool does not override rejection.")
				d.RequirementIDs = []string{req.ID}
				d.Failure.BlockerID = req.ID
				return d, nil
			case "pending":
				if approval.ExpiresAt.After(time.Now()) {
					d := taskDenied(op, "approval_required", "An approval is still pending.", "Wait for the user decision before proceeding.")
					d.Outcome = "blocked"
					d.RequirementIDs = []string{req.ID}
					d.Failure.BlockerID = req.ID
					return d, nil
				}
			}
		}
		result.Outcome = "ask"
		result.RequirementIDs = append(result.RequirementIDs, req.ID)
	}
	return result, nil
}

func (e *Engine) authorizeTaskOperation(ctx, sctx context.Context, th protocol.Thread, turn protocol.Turn, item protocol.Item, snapshot protocol.TaskSnapshot, op protocol.TaskOperation) (protocol.AuthorityDecision, error) {
	decision, err := e.checkTaskAuthority(ctx, th, snapshot, op)
	if err != nil || decision.Outcome != "ask" {
		return decision, err
	}
	for _, id := range decision.RequirementIDs {
		current, err := e.Store.ActiveExecutionTask(sctx, th.ID)
		if err != nil {
			return decision, err
		}
		var req protocol.TaskRequirement
		for _, candidate := range current.Requirements {
			if candidate.ID == id {
				req = candidate
				break
			}
		}
		if req.ID == "" {
			return decision, store.ErrTaskConflict
		}
		// This scope approval is explicit and separate from normal tool permissions.
		payloadFields := map[string]any{"task_id": current.Task.ID, "requirement_id": req.ID, "scope": req.Scope, "condition": req.Condition}
		if req.Origin == "policy" && req.OriginRef == "policy.require_workflow_approval" {
			binding, _, plan, reviewErr := e.configuredWorkReview(sctx, th.ID)
			if reviewErr != nil {
				return decision, reviewErr
			}
			payloadFields["work_review_binding"], payloadFields["reviewed_solution"] = binding, plan
		}
		payload, _ := json.Marshal(payloadFields)
		timeout := time.Duration(e.Cfg.Policy.ApprovalTimeoutMinutes) * time.Minute
		now := time.Now().UTC()
		a := protocol.Approval{ID: store.NewID("apr"), Kind: "task_requirement", ThreadID: th.ID, ProjectID: th.ProjectID, WorkID: current.Task.WorkID, TurnID: turn.ID, ItemID: item.ID, NodeID: req.ID, NodeRevision: req.Revision, Tool: "task.propose", Args: payload, Risk: "workflow", Reason: "Explicit task workflow requirement", ActionSummary: fmt.Sprintf("Approve this task requirement for %s; affected scope: %s. Proposed action: %s", current.Task.Objective, string(req.Scope), op.Tool), Status: "pending", CreatedAt: now, ExpiresAt: now.Add(timeout)}
		approved, waitErr := e.persistAndWaitApproval(ctx, sctx, a, timeout, "task.requirement.approval.request")
		if linkErr := e.Store.LinkTaskRequirementApproval(sctx, req.ID, req.Revision, a.ID); linkErr != nil {
			return decision, linkErr
		}
		if waitErr != nil {
			return decision, waitErr
		}
		if !approved {
			return taskDenied(op, "approval_rejected", "The user rejected this task requirement.", "Stop this action; clarify the task before continuing."), nil
		}
	}
	refreshed, err := e.Store.ActiveExecutionTask(sctx, th.ID)
	if err != nil {
		return decision, err
	}
	return e.checkTaskAuthority(ctx, th, refreshed, op)
}

func (e *Engine) finishTaskToolOperation(ctx context.Context, id, itemID string) {
	item, err := e.Store.GetItem(ctx, itemID)
	if err != nil {
		return
	}
	if err := e.recordTaskOutcome(ctx, id, item); err != nil && e.Log != nil {
		e.Log.Warn("record task outcome", "operation", id, "error", err)
	}
}

// Re-read every authority source after approval waits, immediately before Call.
func (e *Engine) revalidateTaskAction(ctx context.Context, th protocol.Thread, snapshot *protocol.TaskSnapshot, op protocol.TaskOperation) (protocol.AuthorityDecision, error) {
	if x := childExecutionFrom(ctx); x != nil {
		d, err := e.checkChildTaskAuthority(ctx, op)
		if err != nil || d.Outcome != "allow" {
			return d, err
		}
		if err := e.checkWorkflowGate(ctx, x.task.ThreadID, op.Tool); err != nil {
			return protocol.AuthorityDecision{}, err
		}
	} else {
		if err := e.checkWorkflowGate(ctx, th.ID, op.Tool); err != nil {
			return protocol.AuthorityDecision{}, err
		}
		if snapshot != nil {
			return e.checkTaskAuthority(ctx, th, *snapshot, op)
		}
	}
	return protocol.AuthorityDecision{Outcome: "allow"}, nil
}

func taskWorkflowFailure(operationID string, err error) protocol.ActionFailure {
	f := protocol.ActionFailure{AuthoritySource: "work_graph", Category: "workflow_requirement", OperationID: operationID, Message: err.Error(), Retryable: true, NextStep: "Refresh the current work graph and resolve the actual required approval; do not manufacture completed states."}
	var blocker *workflowBlocker
	if errors.As(err, &blocker) {
		f.BlockerID = blocker.WorkID
		f.State, _ = json.Marshal(blocker)
	}
	return f
}

// Mandatory review requires a real human record. Agent-approved bookkeeping
// may describe a solution, but it cannot satisfy configured human review.
func (e *Engine) configuredWorkReview(ctx context.Context, threadID string) (string, bool, []protocol.WorkNode, error) {
	w, ok, err := e.Store.OpenWorkForThread(ctx, threadID)
	if err != nil {
		return "", false, nil, err
	}
	if !ok {
		return "no-current-work", false, nil, nil
	}
	detail, err := e.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return "", false, nil, err
	}
	var keys []string
	var plan []protocol.WorkNode
	selected := map[string]bool{}
	for _, edge := range detail.Edges {
		if edge.Relation == "selects" {
			selected[edge.FromNodeID] = true
			keys = append(keys, "selects:"+edge.FromNodeID+":"+edge.ToNodeID)
		}
	}
	for _, node := range detail.Nodes {
		if node.ValidUntil != nil || node.Status == protocol.StatusSuperseded || node.Kind != protocol.NodeDecision && node.Kind != protocol.NodeOption {
			continue
		}
		plan = append(plan, node)
		keys = append(keys, fmt.Sprintf("%s:%s:%d:%s", node.Kind, node.ID, node.Revision, node.Status))
	}
	sort.Strings(keys)
	binding := fmt.Sprintf("%s:%x", w.ID, sha256.Sum256([]byte(strings.Join(keys, "\n"))))
	approvals, err := e.Store.ListApprovals(ctx, "")
	if err != nil {
		return "", false, nil, err
	}
	for _, a := range approvals {
		if a.Kind != "workflow" || a.Status != "approved" || a.ThreadID != threadID || a.WorkID != w.ID {
			continue
		}
		for _, node := range plan {
			if node.ID == a.NodeID && node.Status == protocol.StatusApproved && node.DecisionActor != "agent" && selected[node.ID] && (node.Revision == a.NodeRevision || node.Revision == a.NodeRevision+1) {
				return binding, true, plan, nil
			}
		}
	}
	return binding, false, plan, nil
}
