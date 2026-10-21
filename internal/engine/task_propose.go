package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

type executionItemKey struct{}

type taskProposalTool struct{ engine *Engine }

func (*taskProposalTool) Name() string { return "task.propose" }
func (*taskProposalTool) Description() string {
	return "Propose current task continuity or requirements; engine validates scope and provenance. Cannot grant permissions, approve actions, or declare completion."
}
func (*taskProposalTool) SelectionMetadata() (string, string) { return "core", "engine" }
func (*taskProposalTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["task_id","expected_revision"],"properties":{"task_id":{"type":"string"},"expected_revision":{"type":"integer","minimum":1},"relation":{"type":"string","enum":["continue","new"]},"user_item_id":{"type":"string"},"requirement":{"type":"object","additionalProperties":false,"required":["origin","origin_ref","scope","condition"],"properties":{"origin":{"type":"string","enum":["user"]},"origin_ref":{"type":"string"},"scope":{"type":"object","additionalProperties":false,"properties":{"tools":{"type":"array","maxItems":32,"items":{"type":"string"}},"paths":{"type":"array","maxItems":32,"items":{"type":"string"}},"except_paths":{"type":"array","maxItems":32,"items":{"type":"string"}}}},"condition":{"type":"object","additionalProperties":false,"required":["type"],"properties":{"type":{"type":"string","enum":["approval_before_actions","no_actions"]}}}}},"resolution":{"type":"object","additionalProperties":false,"required":["operation_id","reason"],"properties":{"operation_id":{"type":"string"},"reason":{"type":"string","minLength":1,"maxLength":2048},"evidence_ids":{"type":"array","maxItems":32,"items":{"type":"string"}}}},"verification":{"type":"object","additionalProperties":false,"required":["operation_id","evidence_ids"],"properties":{"operation_id":{"type":"string"},"evidence_ids":{"type":"array","minItems":1,"items":{"type":"string"}},"supersedes_id":{"type":"string"}}}}}`)
}
func (*taskProposalTool) Assess(json.RawMessage) (tools.Risk, string) {
	return tools.RiskGreen, "Propose task metadata"
}
func (t *taskProposalTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	turn, ok := ctx.Value(executionTurnKey{}).(protocol.Turn)
	if !ok {
		return "", errors.New("task proposal requires a running engine turn")
	}
	if childExecutionFrom(ctx) != nil {
		return "", errors.New("children cannot change parent task requirements")
	}
	return t.engine.applyTaskProposal(ctx, turn, args)
}
func (e *Engine) applyTaskProposal(ctx context.Context, turn protocol.Turn, args json.RawMessage) (string, error) {
	if len(args) > 65536 {
		return "", errors.New("task proposal exceeds 65536 bytes")
	}
	var proposal struct {
		TaskID           string          `json:"task_id"`
		ExpectedRevision int             `json:"expected_revision"`
		Relation         string          `json:"relation"`
		UserItemID       string          `json:"user_item_id"`
		Requirement      json.RawMessage `json:"requirement"`
		Verification     json.RawMessage `json:"verification"`
		Resolution       json.RawMessage `json:"resolution"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("one task proposal object required")
	}
	count := 0
	if proposal.Relation != "" {
		count++
	}
	if len(proposal.Requirement) > 0 {
		count++
	}
	if len(proposal.Verification) > 0 {
		count++
	}
	if len(proposal.Resolution) > 0 {
		count++
	}
	if count != 1 {
		return "", errors.New("exactly one relationship, requirement, or verification proposal required")
	}
	snapshot, err := e.Store.ActiveExecutionTask(ctx, turn.ThreadID)
	if err != nil {
		return "", err
	}
	if proposal.TaskID != snapshot.Task.ID || proposal.ExpectedRevision != snapshot.Task.Revision {
		return "", store.ErrTaskConflict
	}
	user, err := e.currentTaskUser(ctx, turn)
	if err != nil {
		return "", err
	}
	if proposal.UserItemID != "" && proposal.UserItemID != user.ID {
		return "", errors.New("only the current submitted user item may define task intent")
	}
	// Legacy semantic proposals are advisory under the new runtime policy. They
	// cannot create an approval, enforce a task category, or widen permissions.
	if snapshot.Task.ExecutionPolicy == modelDrivenExecutionPolicy && (proposal.Relation != "" || len(proposal.Requirement) > 0) {
		raw, _ := json.Marshal(map[string]any{"advisory": true, "task_id": snapshot.Task.ID, "executionPolicy": snapshot.Task.ExecutionPolicy, "message": "Plans and task requirements are model-owned context. Follow the original user instructions; this metadata does not create an execution gate or permission."})
		return string(raw), nil
	}
	if len(proposal.Resolution) > 0 {
		var request struct {
			OperationID string   `json:"operation_id"`
			Reason      string   `json:"reason"`
			EvidenceIDs []string `json:"evidence_ids"`
		}
		if err := strictTaskJSON(proposal.Resolution, &request); err != nil {
			return "", err
		}
		if len(request.Reason) == 0 || len(request.Reason) > 2048 || len(request.EvidenceIDs) > 32 {
			return "", errors.New("bounded inspection reason and evidence required")
		}
		operation, err := e.Store.GetTaskOperation(ctx, request.OperationID)
		if err != nil {
			return "", err
		}
		if operation.TaskID != snapshot.Task.ID || operation.Status != "outcome_unknown" || operation.ResolvedByID != "" {
			return "", errors.New("only an unresolved uncertain operation in this task can be acknowledged")
		}
		for _, id := range request.EvidenceIDs {
			found := false
			for _, candidate := range snapshot.Operations {
				if candidate.ItemID == id && candidate.Status == "succeeded" && workflowDiscoveryTool(candidate.Tool) {
					found = true
				}
			}
			if !found {
				return "", errors.New("inspection evidence must be a successful read in this task")
			}
		}
		originalArgs, argsErr := e.operationArgs(ctx, operation)
		if argsErr != nil {
			return "", argsErr
		}
		operation.Args = originalArgs
		payload, _ := json.Marshal(map[string]any{"operation": operation, "inspection_reason": request.Reason, "evidence_ids": request.EvidenceIDs})
		approvalID, err := e.confirmTaskProposalApproval(ctx, turn, snapshot, user, "Acknowledge the inspected effects of this interrupted action and allow further work without replaying it. This does not claim the original invocation succeeded", payload)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{"status": "acknowledged", "resolution": taskOpaqueResolution{OperationID: operation.ID, ExpectedRevision: operation.Revision, ApprovalID: approvalID, EvidenceIDs: request.EvidenceIDs}})
		return string(raw), err
	}
	if len(proposal.Verification) > 0 {
		var v struct {
			OperationID  string   `json:"operation_id"`
			EvidenceIDs  []string `json:"evidence_ids"`
			SupersedesID string   `json:"supersedes_id"`
		}
		if err := strictTaskJSON(proposal.Verification, &v); err != nil {
			return "", err
		}
		operation, err := e.Store.GetTaskOperation(ctx, v.OperationID)
		if err != nil {
			return "", err
		}
		if operation.TaskID != snapshot.Task.ID {
			return "", errors.New("verification cannot cross task scope")
		}
		if err := e.verifyTaskOperation(ctx, v.OperationID, v.EvidenceIDs, v.SupersedesID); err != nil {
			return "", err
		}
		return `{"status":"verified"}`, nil
	}
	if len(proposal.Requirement) > 0 {
		var req struct {
			Origin    string          `json:"origin"`
			OriginRef string          `json:"origin_ref"`
			Scope     json.RawMessage `json:"scope"`
			Condition json.RawMessage `json:"condition"`
		}
		if err := strictTaskJSON(proposal.Requirement, &req); err != nil {
			return "", err
		}
		if req.Origin != "user" || req.OriginRef != user.ID {
			return "", errors.New("requirement must cite the current user item; policy provenance is engine-owned")
		}
		th, err := e.Store.GetThread(ctx, turn.ThreadID)
		if err != nil {
			return "", err
		}
		var scope taskRequirementScope
		var condition taskRequirementCondition
		if err := strictTaskJSON(req.Scope, &scope); err != nil {
			return "", err
		}
		if err := strictTaskJSON(req.Condition, &condition); err != nil {
			return "", err
		}
		if condition.Type != "no_actions" && condition.Type != "approval_before_actions" {
			return "", errors.New("unsupported restrictive requirement condition")
		}
		bindings, err := taskRequirementBindings(ctx, scope)
		if err != nil {
			return "", err
		}
		if err := e.confirmTaskProposal(ctx, turn, snapshot, user, "Confirm the proposed interpretation of your task requirements", func() json.RawMessage {
			raw, _ := json.Marshal(map[string]any{"requirement": json.RawMessage(proposal.Requirement), "resolved_artifacts": bindings})
			return raw
		}()); err != nil {
			return "", err
		}
		result, err := e.validateTaskRequirement(ctx, th, protocol.RequirementProposal{TaskID: proposal.TaskID, ExpectedRevision: proposal.ExpectedRevision, Origin: req.Origin, OriginRef: req.OriginRef, Scope: req.Scope, Condition: req.Condition, ResourceBindings: bindings})
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(result)
		return string(raw), err
	}
	if len(proposal.Requirement) > 0 || len(proposal.Verification) > 0 {
		return "", errors.New("task requirement or verification proposal requires validated engine evidence")
	}
	switch proposal.Relation {
	case "continue":
		if proposal.UserItemID != user.ID {
			return "", errors.New("current user_item_id required")
		}
	case "new":
		if proposal.UserItemID != user.ID || continuationRequest(user.Text) {
			return "", errors.New("a new objective from the current user is required")
		}
		if snapshot.Task.OriginItemID == user.ID {
			raw, err := json.Marshal(snapshot)
			return string(raw), err
		}
		payload, _ := json.Marshal(map[string]any{"old_task_id": snapshot.Task.ID, "new_objective": user.Text, "previous_requirements": snapshot.Requirements})
		if err := e.confirmTaskProposal(ctx, turn, snapshot, user, "Confirm starting a new task and replacing the previous task's constraints", payload); err != nil {
			return "", err
		}
		th, err := e.Store.GetThread(ctx, turn.ThreadID)
		if err != nil {
			return "", err
		}
		summary, truncated := executionTaskObjective(user.Text)
		next, err := e.Store.CreateExecutionTaskWithRequirements(ctx, protocol.ExecutionTask{ThreadID: th.ID, ProjectID: th.ProjectID, OriginItemID: user.ID, Objective: summary, ObjectiveTruncated: truncated}, e.policyTaskRequirements())
		if err != nil {
			return "", err
		}
		snapshot.Task = next

	default:
		return "", errors.New("a validated task relationship, requirement, or verification proposal is required")
	}
	raw, err := json.Marshal(snapshot)
	return string(raw), err
}

// The model cannot declare that a human imposed a requirement or removed one.
// A concrete proposed interpretation is confirmed through the existing transport.
func (e *Engine) confirmTaskProposal(ctx context.Context, turn protocol.Turn, snapshot protocol.TaskSnapshot, user protocol.Item, summary string, payload json.RawMessage) error {
	_, err := e.confirmTaskProposalApproval(ctx, turn, snapshot, user, summary, payload)
	return err
}
func (e *Engine) confirmTaskProposalApproval(ctx context.Context, turn protocol.Turn, snapshot protocol.TaskSnapshot, user protocol.Item, summary string, payload json.RawMessage) (string, error) {
	item, ok := ctx.Value(executionItemKey{}).(protocol.Item)
	if !ok || item.TurnID != turn.ID || item.ThreadID != turn.ThreadID || e.Cfg == nil {
		return "", errors.New("task interpretation requires confirmation in a running engine action")
	}
	timeout := time.Duration(e.Cfg.Policy.ApprovalTimeoutMinutes) * time.Minute
	now := time.Now().UTC()
	a := protocol.Approval{ID: store.NewID("apr"), Kind: "task_interpretation", NodeID: snapshot.Task.ID, NodeRevision: snapshot.Task.Revision, ThreadID: turn.ThreadID, ProjectID: snapshot.Task.ProjectID, TurnID: turn.ID, ItemID: item.ID, Tool: "task.propose", Args: payload, Risk: "workflow", Reason: "Model-proposed task interpretation", ActionSummary: fmt.Sprintf("%s. Your request: %s. Proposed interpretation: %s", summary, user.Text, string(payload)), Status: "pending", CreatedAt: now, ExpiresAt: now.Add(timeout)}
	allowed, err := e.persistAndWaitApproval(ctx, context.WithoutCancel(ctx), a, timeout, "task.interpretation.request")
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", &taskActionError{Failure: protocol.ActionFailure{Category: "approval_rejected", Message: "The proposed task interpretation was rejected.", NextStep: "Clarify the task; do not impose the rejected interpretation."}}
	}
	current, err := e.Store.ActiveExecutionTask(context.WithoutCancel(ctx), turn.ThreadID)
	if err != nil {
		return "", err
	}
	if current.Task.ID != snapshot.Task.ID || current.Task.Revision != snapshot.Task.Revision {
		return "", store.ErrTaskConflict
	}
	return a.ID, nil
}
