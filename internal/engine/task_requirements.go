package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/tools"
	"io"
	"os"
	"path/filepath"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/work"
)

type taskRequirementScope struct {
	ExceptPaths []string `json:"except_paths,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Paths       []string `json:"paths,omitempty"`
}
type taskRequirementCondition struct {
	Type string `json:"type"`
}

func strictTaskJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("one JSON object required")
	}
	return nil
}
func (e *Engine) validateTaskRequirement(ctx context.Context, th protocol.Thread, p protocol.RequirementProposal) (protocol.TaskRequirement, error) {
	snap, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		return protocol.TaskRequirement{}, err
	}
	if snap.Task.ID != p.TaskID || snap.Task.ProjectID != th.ProjectID || snap.Task.Revision != p.ExpectedRevision {
		return protocol.TaskRequirement{}, store.ErrTaskConflict
	}
	var scope taskRequirementScope
	var condition taskRequirementCondition
	if err := strictTaskJSON(p.Scope, &scope); err != nil {
		return protocol.TaskRequirement{}, err
	}
	if err := strictTaskJSON(p.Condition, &condition); err != nil {
		return protocol.TaskRequirement{}, err
	}
	if condition.Type != "approval_before_actions" && condition.Type != "no_actions" {
		return protocol.TaskRequirement{}, errors.New("unsupported restrictive requirement condition")
	}
	switch p.Origin {
	case "user":
		origin := snap.Task.ContinuationItemID
		if origin == "" {
			origin = snap.Task.OriginItemID
		}
		if p.OriginRef != origin {
			return protocol.TaskRequirement{}, errors.New("requirement must cite the current submitted user item")
		}
	case "policy":
		if e.Cfg == nil || !e.Cfg.Policy.RequireWorkflowApproval || p.OriginRef != "policy.require_workflow_approval" || condition.Type != "approval_before_actions" {
			return protocol.TaskRequirement{}, errors.New("requirement does not match trusted policy")
		}
	default:
		return protocol.TaskRequirement{}, errors.New("model cannot supply requirement provenance")
	}
	bindings, err := taskRequirementBindings(ctx, scope)
	if err != nil {
		return protocol.TaskRequirement{}, err
	}
	if p.ResourceBindings != nil {
		for path, bound := range p.ResourceBindings {
			if bindings[path] != bound {
				return protocol.TaskRequirement{}, errors.New("artifact target changed during confirmation")
			}
		}
	}
	p.ResourceBindings = bindings
	return e.Store.PutTaskRequirement(ctx, p)
}

// Legacy recovery only creates metadata. It never executes a tool or changes
// old work graphs and cannot translate inferred depth into a human approval.
func (e *Engine) recoverLegacyTask(ctx context.Context, th protocol.Thread, user protocol.Item) (protocol.TaskSnapshot, error) {
	existing, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return existing, err
	}
	w, ok, err := e.Store.OpenWorkForThread(ctx, th.ID)
	if err != nil {
		return existing, err
	}
	if !ok {
		return existing, store.ErrNotFound
	}
	items, err := e.Store.ListItems(ctx, th.ID, 0)
	if err != nil {
		return existing, err
	}
	var origin protocol.Item
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.ID != user.ID && it.Kind == protocol.ItemUserMessage && !continuationRequest(it.Text) && it.Text != "" {
			origin = it
			break
		}
	}
	if origin.ID == "" {
		if continuationRequest(user.Text) {
			return existing, store.ErrNotFound
		}
		origin = user
	}
	detail, err := e.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return existing, err
	}
	summary, truncated := executionTaskObjective(origin.Text)
	task := protocol.ExecutionTask{ThreadID: th.ID, ProjectID: th.ProjectID, WorkID: w.ID, OriginItemID: origin.ID, Objective: summary, ObjectiveTruncated: truncated}
	proposals := e.policyTaskRequirements()
	// Only material decisions with unknown provenance need one explicit resolution.
	// Mere designed depth or missing planning bookkeeping must not lock all tools.
	for _, node := range detail.Nodes {
		if node.Kind != protocol.NodeDecision || node.Status == protocol.StatusSuperseded || node.ValidUntil != nil {
			continue
		}
		one := protocol.WorkDetail{Nodes: []protocol.WorkNode{node}}
		if !work.HasMaterialGate(one) {
			continue
		}
		condition, _ := json.Marshal(taskRequirementCondition{Type: "approval_before_actions"})
		proposals = append(proposals, protocol.RequirementProposal{Origin: "legacy", OriginRef: node.ID, Scope: json.RawMessage(`{}`), Condition: condition})
	}
	var history []protocol.TaskOperation
	for _, item := range items {
		if item.Kind != protocol.ItemToolCall || item.Tool == nil || item.Seq <= origin.Seq || item.Seq >= user.Seq {
			continue
		}
		op := protocol.TaskOperation{TurnID: item.TurnID, ItemID: item.ID, Tool: item.Tool.Name, Args: item.Tool.Args, ImplementationID: "legacy_unverified", VerificationStatus: "pending"}
		switch item.Status {
		case protocol.ItemCompleted:
			op.Status = "succeeded"
			if workflowDiscoveryTool(op.Tool) {
				op.VerificationStatus = "passed"
			}
		case protocol.ItemFailed, protocol.ItemDenied:
			op.Status = "failed"
			if item.Status == protocol.ItemDenied {
				op.Status = "blocked"
			}
			f := classifyActionFailure("", errors.New(item.Tool.Error))
			f.Advisory = workflowDiscoveryTool(op.Tool) && f.Retryable
			op.Failure = &f
			if f.Category == "outcome_unknown" {
				op.Status = "outcome_unknown"
			}
		default:
			op.Status = "outcome_unknown"
			op.Failure = &protocol.ActionFailure{Category: "outcome_unknown", Message: "A historical action was interrupted; its effects are uncertain.", NextStep: "Inspect and verify its effects before any further mutation; do not replay it."}
		}
		history = append(history, op)
	}
	if _, err := e.Store.CreateExecutionTaskWithHistory(ctx, task, proposals, history); err != nil {
		return existing, err
	}
	return e.Store.ActiveExecutionTask(ctx, th.ID)
}

func (e *Engine) policyTaskRequirements() []protocol.RequirementProposal {
	if e.Cfg == nil || !e.Cfg.Policy.RequireWorkflowApproval {
		return nil
	}
	return []protocol.RequirementProposal{{Origin: "policy", OriginRef: "policy.require_workflow_approval", Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)}}
}

func taskRequirementBindings(ctx context.Context, scope taskRequirementScope) (map[string]string, error) {
	if len(scope.Tools) > 32 || len(scope.Paths) > 32 || len(scope.ExceptPaths) > 32 {
		return nil, errors.New("requirement scope exceeds 32 entries")
	}
	if len(scope.ExceptPaths) == 0 {
		return nil, nil
	}
	s := tools.ScopeFrom(ctx)
	if s == nil {
		return nil, errors.New("artifact exceptions require a project scope")
	}
	bindings := map[string]string{}
	for _, path := range scope.ExceptPaths {
		if path == "" {
			return nil, errors.New("empty artifact target")
		}
		resolved, err := s.Resolve(path)
		if err != nil {
			return nil, err
		}
		for probe := resolved; probe != filepath.Dir(probe); probe = filepath.Dir(probe) {
			if info, err := os.Lstat(probe); err == nil && info.Mode()&os.ModeSymlink != 0 {
				if _, err := filepath.EvalSymlinks(probe); err != nil {
					return nil, errors.New("artifact target contains an unresolved symlink")
				}
			}
		}
		bindings[path] = pathutil.Resolved(resolved)
	}
	return bindings, nil
}
func taskArtifactExcepted(ctx context.Context, req protocol.TaskRequirement, scope taskRequirementScope, op protocol.TaskOperation) bool {
	if op.Tool != "file.write" && op.Tool != "file.edit" {
		return false
	}
	target, err := taskFileTarget(ctx, op.Args)
	if err != nil {
		return false
	}
	for _, path := range scope.ExceptPaths {
		bound := req.ResourceBindings[path]
		current, err := taskRequirementBindings(ctx, taskRequirementScope{ExceptPaths: []string{path}})
		if err == nil && bound != "" && current[path] == bound && target == bound {
			return true
		}
	}
	return false
}
