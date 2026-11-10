package engine

import (
	"context"
	"errors"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// runtimeWorkflow is the verification state of a runtime task's integration
// head, persisted in the head row's Workflow. The type lives in store
// (ruling R1) so the delegation service can compute Verified from it.
type runtimeWorkflow = store.RuntimeWorkflow

// planRuntimeVerification plans the checks for the task's current
// integration head with the verification.plan tool (exactly as the legacy
// integrate action does), resets every check to not_run and stores the plan,
// keyed to the head tree, in the head row. The lead's accept calls it after
// every apply; the head must be recorded and the parent must be exactly the
// head tree both before planning and when the plan is written.
func (e *Engine) planRuntimeVerification(ctx context.Context, task protocol.AgentTask) (runtimeWorkflow, error) {
	svc := e.DelegationWorkspaceService
	h, err := e.Store.AgentIntegrationHead(ctx, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		return runtimeWorkflow{}, errors.New("no integrated head to verify; accept a revision first")
	}
	if err != nil {
		return runtimeWorkflow{}, err
	}
	// The planner reads the parent checkout: it must be the head tree.
	if err = svc.ValidateRuntimeHead(ctx, task.ID, h.HeadTree); err != nil {
		return runtimeWorkflow{}, err
	}
	checks, err := e.planVerificationChecks(ctx)
	if err != nil {
		return runtimeWorkflow{}, err
	}
	w := runtimeWorkflow{HeadTree: h.HeadTree, Checks: checks}
	if err = svc.UpdateRuntimeWorkflow(ctx, task.ID, h.HeadTree, func(runtimeWorkflow) (runtimeWorkflow, error) {
		return w, nil
	}); err != nil {
		return runtimeWorkflow{}, err
	}
	e.publishRuntimeWorkflow(ctx, task.ID, w)
	return w, nil
}

// beginRuntimeVerification brackets a lead verification call on a runtime
// task. Without a plan for the current head there is nothing to record (as
// before a legacy integration); otherwise the parent must be exactly the
// planned head tree or the call is refused.
func (e *Engine) beginRuntimeVerification(ctx context.Context, h store.AgentIntegrationHead) (*verificationTarget, error) {
	w, err := store.DecodeRuntimeWorkflow(h.Workflow)
	if err != nil {
		return nil, err
	}
	if w.HeadTree == "" || w.HeadTree != h.HeadTree || len(w.Checks) == 0 {
		return nil, nil
	}
	if err = e.DelegationWorkspaceService.ValidateRuntimeHead(ctx, h.TaskID, w.HeadTree); err != nil {
		return nil, err
	}
	return &verificationTarget{taskID: h.TaskID, headTree: w.HeadTree}, nil
}

// recordRuntimeVerification records the raw output of the lead's real
// verification.run/browser.verify call into the head's workflow, with the
// legacy status and summary rules (recordVerificationResults). headTree is
// the tree the call was begun for: the write is refused, and nothing changes,
// unless the head and the parent checkout are still exactly that tree and the
// stored plan is still for it.
func (e *Engine) recordRuntimeVerification(ctx context.Context, taskID, headTree, name, raw string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var recorded runtimeWorkflow
	err := e.DelegationWorkspaceService.UpdateRuntimeWorkflow(ctx, taskID, headTree, func(current runtimeWorkflow) (runtimeWorkflow, error) {
		if current.HeadTree != headTree || len(current.Checks) == 0 {
			return current, errors.New("verification plan changed; plan the integrated head again")
		}
		if err := recordVerificationResults(current.Checks, name, raw); err != nil {
			return current, err
		}
		recorded = current
		return current, nil
	})
	if err != nil {
		return err
	}
	e.publishRuntimeWorkflow(ctx, taskID, recorded)
	return nil
}

// publishRuntimeWorkflow announces the head's checks. Revision stays empty:
// the head is a tree, not a commit.
func (e *Engine) publishRuntimeWorkflow(ctx context.Context, taskID string, w runtimeWorkflow) {
	task, err := e.Store.AgentTask(ctx, taskID)
	if err != nil {
		return
	}
	view := protocol.AgentWorkflow{TaskID: taskID, Checks: w.Checks}
	e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: task.ID, Task: &task, Workflow: &view})
}
