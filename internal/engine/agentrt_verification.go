package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
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
	var untracked *worktree.UntrackedError
	if errors.As(err, &untracked) {
		return fmt.Errorf("verification output left untracked files in the project checkout (%s), so the result cannot be recorded for the integrated state; remove them or ignore them in .gitignore, then run the checks again: %w", strings.Join(untracked.Paths, ", "), err)
	}
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

// planVerificationChecks calls the verification.plan tool and returns its
// checks, every one not_run, plus a blocked Visual QA check when the planner
// requires visual QA. Shared by the legacy integrate action and the runtime
// head (planRuntimeVerification).
func (e *Engine) planVerificationChecks(ctx context.Context) ([]store.AgentWorkflowCheck, error) {
	planner, ok := e.Tools.Get("verification.plan")
	if !ok {
		return nil, errors.New("verification planner unavailable")
	}
	raw, err := planner.Call(ctx, json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	var plan struct {
		Checks   []store.AgentWorkflowCheck `json:"checks"`
		VisualQA string                     `json:"visual_qa"`
	}
	if err = json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, err
	}
	checks := plan.Checks
	for i := range checks {
		checks[i].Status = "not_run"
	}
	if strings.HasPrefix(plan.VisualQA, "required:") {
		checks = append(checks, store.AgentWorkflowCheck{Label: "Visual QA", Command: "visual QA", Kind: "visual", Reason: plan.VisualQA, Status: "blocked", Summary: "Trusted interactive visual QA receipts are unavailable in delegated completion v1."})
	}
	return checks, nil
}

// verificationTarget is what a bracketed verification.run/browser.verify call
// records into: the legacy implementer workflow, or a runtime task's
// integration head (headTree is the tree its plan was made for).
type verificationTarget struct {
	legacy   *store.AgentWorkflow
	taskID   string
	headTree string
}

// begin/end bracket the real built-in call, after normal tool approval. Neither
// model text nor tool arguments provide status evidence. Pre-integration and
// child checks intentionally never create lead receipts.
//
// A task whose integration head row exists is a runtime task: that row is
// written only by runtime integration (Plan 3), and it is exactly what gets
// verified. Every other task keeps the legacy workflow target.
func (e *Engine) beginDelegationVerification(ctx context.Context, name string) (*verificationTarget, error) {
	if name != "verification.run" && name != "browser.verify" {
		return nil, nil
	}
	task, ok := DelegationTaskFromContext(ctx)
	if !ok {
		return nil, nil
	}
	head, err := e.Store.AgentIntegrationHead(ctx, task.ID)
	if err == nil {
		return e.beginRuntimeVerification(ctx, head)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	w, err := e.Store.AgentWorkflow(ctx, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = e.DelegationWorkspaceService.ValidateIntegrated(ctx, task.ID, w.ChildID, w.Revision); err != nil {
		return nil, err
	}
	return &verificationTarget{legacy: &w}, nil
}
func (e *Engine) finishDelegationVerification(ctx context.Context, name string, target *verificationTarget, raw string) error {
	if target == nil {
		return nil
	}
	if target.legacy == nil {
		return e.recordRuntimeVerification(ctx, target.taskID, target.headTree, name, raw)
	}
	w := target.legacy
	e.delegationWorkflowMu.Lock()
	defer e.delegationWorkflowMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.DelegationWorkspaceService.ValidateIntegrated(ctx, w.TaskID, w.ChildID, w.Revision); err != nil {
		return err
	}
	current, err := e.Store.AgentWorkflow(ctx, w.TaskID)
	if err != nil {
		return err
	}
	if current.Revision != w.Revision {
		return errors.New("verification revision changed")
	}
	if err = recordVerificationResults(current.Checks, name, raw); err != nil {
		return err
	}
	if err = e.Store.SaveAgentWorkflow(ctx, current); err != nil {
		return err
	}
	if task, readErr := e.Store.AgentTask(ctx, w.TaskID); readErr == nil {
		e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: task.ID, Task: &task, Workflow: &current})
	}
	return nil
}

// recordVerificationResults applies the raw output of a real verification.run
// or browser.verify call to the matching checks (same command and directory;
// browser checks only from browser.verify). The summary is the error, else
// the reason, else the output, truncated to 4096 bytes. Checks may be
// partially updated when it returns an error; callers discard them then.
func recordVerificationResults(checks []store.AgentWorkflowCheck, name, raw string) error {
	type checkResult struct {
		Command   string `json:"command"`
		Directory string `json:"directory"`
		Status    string `json:"status"`
		Output    string `json:"output"`
		Error     string `json:"error"`
		Reason    string `json:"reason"`
	}
	var results []checkResult
	if name == "browser.verify" {
		var r checkResult
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return err
		}
		results = append(results, r)
	} else {
		var r struct {
			Results []checkResult `json:"results"`
		}
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return err
		}
		results = r.Results
	}
	for _, r := range results {
		for i := range checks {
			c := &checks[i]
			if c.Command == r.Command && c.Directory == r.Directory && (c.Kind == "browser") == (name == "browser.verify") {
				switch r.Status {
				case "passed", "failed", "blocked", "not_run":
					c.Status = r.Status
				default:
					return errors.New("unknown verification status")
				}
				c.Summary = r.Error
				if c.Summary == "" {
					c.Summary = r.Reason
				}
				if c.Summary == "" {
					c.Summary = r.Output
				}
				if len(c.Summary) > 4096 {
					c.Summary = c.Summary[:4096]
				}
			}
		}
	}
	return nil
}
