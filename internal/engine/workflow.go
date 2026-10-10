package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/work"
)

var errWorkflowUnavailable = errors.New("workflow gate unavailable")
var errWorkflowNotReady = errors.New("workflow not ready")

func workflowDiscoveryTool(name string) bool {
	switch name {
	case "tools.discover", "file.read", "file.list", "file.search", "web.search", "web.fetch", "verification.plan", "computer.list", "computer.inspect", "visual.inspect", "work.update":
		return true
	}
	return false
}

// Prospective signals must persist before any hook, policy approval, or Call.
// Observations still perform their existing post-execution escalation.
func (e *Engine) escalateProspectiveWorkflow(ctx context.Context, threadID string, o work.Observation) error {
	if !e.optimizationPolicy(ctx).DesignedWorkflow || workflowDiscoveryTool(o.Tool) {
		return nil
	}
	w, ok, err := e.Store.OpenWorkForThread(ctx, threadID)
	if err != nil {
		return errWorkflowUnavailable
	}
	if !ok {
		return nil
	}
	d, err := e.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return errWorkflowUnavailable
	}
	depth := work.ObservedDepth(w.WorkflowDepth, d, o)
	if depth != w.WorkflowDepth {
		if err := e.Store.SetWorkDepth(ctx, w.ID, depth); err != nil {
			return errWorkflowUnavailable
		}
	}
	return nil
}

func (e *Engine) workflowDetail(ctx context.Context, threadID, tool string) (*protocol.WorkDetail, error) {
	if !e.optimizationPolicy(ctx).DesignedWorkflow || workflowDiscoveryTool(tool) {
		return nil, nil
	}
	w, ok, err := e.Store.OpenWorkForThread(ctx, threadID)
	if err != nil {
		return nil, errWorkflowUnavailable
	}
	if !ok {
		return nil, nil
	}
	d, err := e.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return nil, errWorkflowUnavailable
	}
	if w.WorkflowDepth != protocol.DepthDesigned {
		if !work.HasMaterialGate(d) {
			return nil, nil
		}
		// Repair older persisted graphs before allowing any mutation. New graph
		// updates already commit the gate and escalation in one transaction.
		if err := e.Store.SetWorkDepth(ctx, w.ID, protocol.DepthDesigned); err != nil {
			return nil, errWorkflowUnavailable
		}
		d, err = e.Store.GetWorkDetail(ctx, w.ID)
		if err != nil {
			return nil, errWorkflowUnavailable
		}
	}
	return &d, nil
}

func (e *Engine) checkWorkflowGate(ctx context.Context, threadID, tool string) error {
	d, err := e.workflowDetail(ctx, threadID, tool)
	if err != nil || d == nil {
		return err
	}
	if len(work.PendingWorkflowGates(*d)) != 0 {
		return errors.New("workflow approval pending")
	}
	if !work.MutationReady(*d) {
		return errWorkflowNotReady
	}
	return nil
}

func (e *Engine) requestWorkflowApproval(ctx, sctx context.Context, turn protocol.Turn, item protocol.Item, gate protocol.WorkflowGate) (bool, error) {
	timeout := time.Duration(e.Cfg.Policy.ApprovalTimeoutMinutes) * time.Minute
	now := time.Now().UTC()
	th, err := e.Store.GetThread(sctx, turn.ThreadID)
	if err != nil {
		return false, errWorkflowUnavailable
	}
	a := protocol.Approval{ID: store.NewID("apr"), Kind: "workflow", WorkID: gate.WorkID, NodeID: gate.NodeID, NodeRevision: gate.NodeRevision, ThreadID: turn.ThreadID, ProjectID: th.ProjectID, TurnID: turn.ID, ItemID: item.ID, Tool: "work.update", Args: json.RawMessage(`{}`), Risk: "workflow", Reason: gate.Reason, ActionSummary: gate.Summary, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(timeout)}
	return e.persistAndWaitApproval(ctx, sctx, a, timeout, "workflow.approval.request")
}

// recoverWorkflowGate gives every gate in the fixed persisted snapshot at most
// one approval opportunity. A live waiter already owns its approval transport.
func (e *Engine) recoverWorkflowGate(ctx, sctx context.Context, turn protocol.Turn, item protocol.Item, tool string, d *protocol.WorkDetail) error {
	if d == nil {
		return nil
	}
	gates := work.PendingWorkflowGates(*d)
	if e.optimizationPolicy(ctx).AutomaticWorkflow {
		// Obsolete workflow waiters convey no action authority. Keep the decision
		// proposed until the agent resolves it through a revision-checked update.
		for _, gate := range gates {
			a, found, err := e.Store.PendingWorkflowApproval(sctx, gate.WorkID, gate.NodeID, gate.NodeRevision)
			if err != nil {
				return errWorkflowUnavailable
			}
			if found {
				e.mu.Lock()
				live := e.approvals[a.ID] != nil
				e.mu.Unlock()
				if live {
					return errWorkflowNotReady
				}
				if err := e.expireApproval(sctx, a, "automatic workflow supersedes internal review"); err != nil {
					return errWorkflowUnavailable
				}
			}
		}
		if len(gates) > 0 {
			return errWorkflowNotReady
		}
		return e.checkWorkflowGate(sctx, turn.ThreadID, tool)
	}
	var blocked error
	for _, gate := range gates {
		a, found, err := e.Store.PendingWorkflowApproval(sctx, gate.WorkID, gate.NodeID, gate.NodeRevision)
		if err != nil {
			return errWorkflowUnavailable
		}
		if found {
			e.mu.Lock()
			live := e.approvals[a.ID] != nil
			e.mu.Unlock()
			if live {
				if blocked == nil {
					blocked = fmt.Errorf("workflow approval pending: %s", gate.NodeID)
				}
				continue
			}
			if err := e.expireApproval(sctx, a, "recovered orphan"); err != nil {
				return errWorkflowUnavailable
			}
		}
		approved, err := e.requestWorkflowApproval(ctx, sctx, turn, item, gate)
		if err != nil || !approved {
			status := "denied"
			if err != nil {
				status = "expired"
			}
			if blocked == nil {
				blocked = fmt.Errorf("workflow approval %s: %s", status, gate.NodeID)
			}
		}
	}
	if blocked != nil {
		return blocked
	}
	return e.checkWorkflowGate(sctx, turn.ThreadID, tool)
}

type workflowGateResult struct {
	Status string `json:"status"`
	NodeID string `json:"node_id"`
}

func (e *Engine) finishWorkflowUpdate(ctx, sctx context.Context, turn protocol.Turn, item protocol.Item, result protocol.WorkUpdateResult, gates []protocol.WorkflowGate) (string, bool, error) {
	if e.optimizationPolicy(ctx).AutomaticWorkflow {
		raw, err := json.Marshal(result)
		return string(raw), false, err
	}
	var outcomes []workflowGateResult
	for _, gate := range gates {
		approved, err := e.requestWorkflowApproval(ctx, sctx, turn, item, gate)
		if err != nil || !approved {
			status := "denied"
			if err != nil {
				status = "expired"
			}
			outcomes = append(outcomes, workflowGateResult{Status: status, NodeID: gate.NodeID})
		}
	}
	w, ok, err := e.Store.OpenWorkForThread(sctx, turn.ThreadID)
	if err != nil || !ok {
		return "", false, errWorkflowUnavailable
	}
	result.Revision = w.Revision
	output, err := json.Marshal(struct {
		protocol.WorkUpdateResult
		Gates []workflowGateResult `json:"gates,omitempty"`
	}{result, outcomes})
	return string(output), len(outcomes) > 0, err
}
