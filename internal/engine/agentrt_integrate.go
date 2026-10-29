package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// runtimeConflictText is the fixed text agent.integrate returns for a
// conflicted accept; %s is the conflicting paths.
const runtimeConflictText = `Conflict in %s. Nothing was applied. Options: agent.integrate {action:"drop", agent, reason}; fix the files in the project checkout, stage them, then agent.integrate {action:"resolved", agent}; or start a new agent with the conflict as context.`

// errRuntimeCheckoutTaken explains why no agent workspace can be created once
// a revision was integrated (gap G1): every workspace is a checkout of the
// task base and needs a clean project checkout, which now holds staged agent
// work.
const errRuntimeCheckoutTaken = "agent work is already integrated (staged) in the project checkout, and a new agent workspace needs a clean checkout of the task base; spawn every agent before the first accept"

// runtimeIntegrateResult is agent.integrate's result: the task's
// RuntimeIntegrationStatus plus the affected revision and what to do next.
type runtimeIntegrateResult struct {
	delegation.RuntimeIntegrationStatus
	Action   string
	Revision *store.AgentRevision `json:",omitempty"`
	// Message is the conflict text for a conflicted accept.
	Message string   `json:",omitempty"`
	Options []string `json:",omitempty"`
	// Checks are the head's planned checks; Verification says whether they
	// were planned ("planned"), planning failed ("plan pending") or the plan
	// is empty ("no checks planned").
	Checks         []protocol.AgentWorkflowCheck `json:",omitempty"`
	Verification   string                        `json:",omitempty"`
	TaskState      protocol.AgentState           `json:",omitempty"`
	CleanupWarning string                        `json:",omitempty"`
}

// registerRuntimeIntegrate registers the lead-only agent.integrate tool. It
// replaces the legacy agent.integrate in runtime_v2 mode.
func (e *Engine) registerRuntimeIntegrate(reg *tools.Registry) {
	reg.Add(tools.NewRuntimeIntegrateTool(e.authorizeDelegationTool, runtimeCaller, e.runtimeIntegrateCall))
}

// runtimeIntegrateCall runs one agent.integrate action for the lead. The tool
// has already refused non-lead contexts and read-only tools. Actions are
// serialised with the legacy workflow actions; the service additionally holds
// its per-task integration lock for each operation.
func (e *Engine) runtimeIntegrateCall(ctx context.Context, task protocol.AgentTask, args tools.RuntimeIntegrateArgs) (any, error) {
	svc := e.DelegationWorkspaceService
	if svc == nil {
		return nil, errors.New("agent integration unavailable")
	}
	e.delegationWorkflowMu.Lock()
	defer e.delegationWorkflowMu.Unlock()
	out := runtimeIntegrateResult{Action: args.Action}
	switch args.Action {
	case "accept":
		if err := e.runtimeAcceptAllowed(ctx, task.ID, args.Agent); err != nil {
			return nil, err
		}
		res, err := svc.IntegrateRuntime(ctx, task.ID, args.Agent)
		if err != nil {
			return nil, fmt.Errorf("accept agent %s: %w", args.Agent, err)
		}
		out.Revision = &res.Revision
		if len(res.Options) > 0 {
			paths := res.Revision.Conflict
			if len(paths) == 0 {
				paths = res.Revision.ChangedPaths
			}
			out.Message = fmt.Sprintf(runtimeConflictText, strings.Join(paths, ", "))
			out.Options = res.Options
			e.publishRuntimeTask(ctx, task.ID)
		} else if res.NeedsVerification {
			e.planRuntimeHead(ctx, task, args.Agent, &out)
		}
	case "drop":
		rev, err := svc.DropRuntime(ctx, task.ID, args.Agent, args.Reason)
		if err != nil {
			return nil, fmt.Errorf("drop agent %s: %w", args.Agent, err)
		}
		out.Revision = &rev
		e.publishRuntimeTask(ctx, task.ID)
	case "resolved":
		res, err := svc.ResolveRuntimeManually(ctx, task.ID, args.Agent)
		if err != nil {
			return nil, fmt.Errorf("resolve agent %s: %w", args.Agent, err)
		}
		out.Revision = &res.Revision
		e.planRuntimeHead(ctx, task, args.Agent, &out)
	case "status":
	case "complete":
		return e.completeRuntimeTask(ctx, task)
	default:
		return nil, fmt.Errorf("unknown action %q", args.Action)
	}
	return e.withRuntimeStatus(ctx, task.ID, out)
}

// withRuntimeStatus fills the result's status and, when not set yet, the
// head's current checks.
func (e *Engine) withRuntimeStatus(ctx context.Context, taskID string, out runtimeIntegrateResult) (runtimeIntegrateResult, error) {
	st, err := e.DelegationWorkspaceService.RuntimeIntegrationStatus(ctx, taskID)
	if err != nil {
		return out, err
	}
	out.RuntimeIntegrationStatus = st
	if out.Checks == nil && st.AppliedCount > 0 {
		if h, err := e.Store.AgentIntegrationHead(ctx, taskID); err == nil {
			if w, err := store.DecodeRuntimeWorkflow(h.Workflow); err == nil && w.HeadTree == h.HeadTree {
				out.Checks = w.Checks
			}
		}
	}
	if t, err := e.Store.AgentTask(ctx, taskID); err == nil {
		out.TaskState = t.State
	}
	return out, nil
}

// runtimeAcceptAllowed enforces the engine-side accept rules before the
// service runs.
//
// Accept rule (chosen): strict submission order. A revision may be accepted
// only when every revision submitted before it (lower Seq) is applied or
// dropped, or belongs to a cancelled agent (never integrable, spec 4.7). An
// earlier revision whose agent is still finishing (recorded, not yet marked
// completed), completed (accept, drop or resolve it), or failed/interrupted
// (drop it) blocks. Replays of an applied revision are not ordered.
//
// The target's own agent must have completed: a cancelled, failed or
// interrupted agent's revision is never integrable, and one still finishing
// must be waited for.
//
// The first accept also requires every live agent's checkout to be ready
// (RuntimeWorkspacesPending, which waits for any checkout being prepared):
// after it the project checkout holds staged changes and no agent checkout can
// be prepared any more, so such an agent could never start (gap G1).
func (e *Engine) runtimeAcceptAllowed(ctx context.Context, taskID, agentID string) error {
	svc := e.DelegationWorkspaceService
	st, err := svc.RuntimeIntegrationStatus(ctx, taskID)
	if err != nil {
		return err
	}
	agents, err := e.Store.RuntimeAgents(ctx, taskID)
	if err != nil {
		return err
	}
	state := map[string]protocol.AgentState{}
	for _, a := range agents {
		state[a.ID] = a.State
	}
	var target *store.AgentRevision
	for i := range st.Revisions {
		if st.Revisions[i].AgentID == agentID && (target == nil || st.Revisions[i].Seq > target.Seq) {
			target = &st.Revisions[i]
		}
	}
	if target == nil || (target.State != store.RevisionSubmitted && target.State != store.RevisionConflict) {
		return nil // the service reports a missing revision; a replay is not ordered
	}
	switch as := state[agentID]; {
	case as == protocol.AgentStateCompleted:
	case !agentrt.IsTerminal(as):
		return fmt.Errorf("accept refused: agent %s is still finishing (%s); wait for it to complete (agent.wait), then accept", agentID, as)
	case as == protocol.AgentStateCancelled:
		return fmt.Errorf("accept refused: agent %s was cancelled after submitting; its revision cannot be integrated. Drop it (agent.integrate {action:\"drop\", agent, reason}) or start a new agent", agentID)
	default:
		return fmt.Errorf("accept refused: agent %s ended %s after submitting; its revision cannot be integrated. Drop it (agent.integrate {action:\"drop\", agent, reason}) or start a new agent", agentID, as)
	}
	for _, r := range st.Revisions {
		if r.Seq >= target.Seq || r.ID == target.ID {
			continue
		}
		if r.State == store.RevisionApplied || r.State == store.RevisionDropped {
			continue
		}
		as := state[r.AgentID]
		if r.State == store.RevisionSubmitted && as == protocol.AgentStateCancelled {
			continue
		}
		const order = "accept refused: revisions are accepted in submission order, and agent %s's revision (submitted earlier) is %s"
		switch {
		case r.State == store.RevisionApplying || as == protocol.AgentStateCompleted:
			return fmt.Errorf(order+"; accept, drop or resolve it before agent %s", r.AgentID, r.State, agentID)
		case !agentrt.IsTerminal(as):
			return fmt.Errorf(order+" and its agent is still finishing (%s); wait for it and accept or drop it before agent %s", r.AgentID, r.State, as, agentID)
		default:
			return fmt.Errorf(order+" but its agent ended %s, so it cannot be integrated; drop it with a reason before agent %s", r.AgentID, r.State, as, agentID)
		}
	}
	if st.AppliedCount == 0 {
		pending, err := svc.RuntimeWorkspacesPending(ctx, taskID)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			a := pending[0]
			return fmt.Errorf("accept refused: agent %s (%s) is %s and its workspace is not ready yet; after the first accept the project checkout holds staged changes and no new agent workspace can be created. Wait until its workspace is ready, or cancel it, then accept", a.ID, a.Name, a.State)
		}
	}
	return nil
}

// planRuntimeHead plans the checks for the head after an accept or a manual
// resolution. A non-empty plan already stored for the head is kept (a replay
// must not reset recorded results); a stored empty plan has no results to
// lose and is planned again, so a replay retries it. A failed plan is
// reported as pending, never as a failed accept: the revision is applied and
// the lead retries with accept.
func (e *Engine) planRuntimeHead(ctx context.Context, task protocol.AgentTask, agentID string, out *runtimeIntegrateResult) {
	if h, err := e.Store.AgentIntegrationHead(ctx, task.ID); err == nil {
		if w, err := store.DecodeRuntimeWorkflow(h.Workflow); err == nil && w.HeadTree == h.HeadTree && len(w.Checks) > 0 {
			out.Checks, out.Verification = w.Checks, runtimePlanText(w)
			return
		}
	}
	w, err := e.planRuntimeVerification(ctx, task)
	if err != nil {
		out.Verification = fmt.Sprintf(`plan pending: the revision is integrated but its checks could not be planned (%v); retry planning with agent.integrate {action:"accept", agent:%q}`, err, agentID)
		return
	}
	out.Checks, out.Verification = w.Checks, runtimePlanText(w)
}

func runtimePlanText(w runtimeWorkflow) string {
	if len(w.Checks) == 0 {
		return "no checks planned: the planner found no executable checks for the integrated state, so it can never be verified; no further revision can be accepted and the task cannot complete"
	}
	return fmt.Sprintf("planned: run the %d checks with verification.run (or browser.verify) on the project checkout; every one must pass for this exact state before the next accept or completion", len(w.Checks))
}

// completeRuntimeTask implements the runtime completion rule: no unfinished
// agents; every revision settled (delegation.RevisionSettled: applied,
// dropped, or submitted by a cancelled agent; a failed or interrupted
// agent's revision needs an explicit drop); and, if anything was integrated, the lead's own
// passing receipts for every planned check of the final head, with the
// project checkout still exactly that head. It then marks the task completed,
// cleans up the applied and verified workspaces (a failure is a warning) and
// publishes the update. Completing a completed task returns its state again.
func (e *Engine) completeRuntimeTask(ctx context.Context, task protocol.AgentTask) (runtimeIntegrateResult, error) {
	svc := e.DelegationWorkspaceService
	out := runtimeIntegrateResult{Action: "complete"}
	cur, err := e.Store.AgentTask(ctx, task.ID)
	if err != nil {
		return out, err
	}
	if cur.State == protocol.AgentStateCompleted {
		return e.withRuntimeStatus(ctx, task.ID, out)
	}
	if !unfinishedDelegation(cur.State) {
		return out, fmt.Errorf("task %s is %s and cannot complete", cur.ID, cur.State)
	}
	if err = svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
		return out, err
	}
	agents, err := e.Store.RuntimeAgents(ctx, task.ID)
	if err != nil {
		return out, err
	}
	state := map[string]protocol.AgentState{}
	var live []string
	for _, a := range agents {
		state[a.ID] = a.State
		if !agentrt.IsTerminal(a.State) {
			live = append(live, fmt.Sprintf("%s (%s) is %s", a.ID, a.Name, a.State))
		}
	}
	if len(live) > 0 {
		return out, fmt.Errorf("completion refused: wait for or cancel the unfinished agents: %s", strings.Join(live, "; "))
	}
	st, err := svc.RuntimeIntegrationStatus(ctx, task.ID)
	if err != nil {
		return out, err
	}
	var open []string
	for _, r := range st.Revisions {
		if delegation.RevisionSettled(r, state[r.AgentID] == protocol.AgentStateCancelled) {
			continue
		}
		if r.State == store.RevisionSubmitted && state[r.AgentID] != protocol.AgentStateCompleted {
			open = append(open, fmt.Sprintf("agent %s's revision is submitted but the agent ended %s, so it cannot be integrated: drop it with a reason", r.AgentID, state[r.AgentID]))
			continue
		}
		open = append(open, fmt.Sprintf("agent %s's revision is %s", r.AgentID, r.State))
	}
	if len(open) > 0 {
		return out, fmt.Errorf("completion refused: accept or drop every revision first: %s", strings.Join(open, "; "))
	}
	var checks []protocol.AgentWorkflowCheck
	if st.AppliedCount > 0 {
		h, err := e.Store.AgentIntegrationHead(ctx, task.ID)
		if err != nil {
			return out, err
		}
		w, err := store.DecodeRuntimeWorkflow(h.Workflow)
		if err != nil {
			return out, err
		}
		if !st.Verified {
			return out, fmt.Errorf("completion refused: the final integrated state %s is not verified: %s", st.HeadTree, runtimeCheckSummary(w, st.HeadTree))
		}
		if err = svc.ValidateRuntimeHead(ctx, task.ID, st.HeadTree); err != nil {
			return out, fmt.Errorf("completion refused: the project checkout is no longer the verified integrated state: %w", err)
		}
		checks = w.Checks
	}
	if err = e.Store.UpdateAgentTaskState(ctx, cur.ID, cur.ThreadID, cur.ProjectID, cur.State, protocol.AgentStateCompleted); err != nil {
		return out, err
	}
	// Completion is durable before cleanup; a cleanup refusal only retains.
	if cleanupErr := svc.CleanupRuntime(context.WithoutCancel(ctx), task.ID); cleanupErr != nil {
		out.CleanupWarning = "Verified work retained: " + cleanupErr.Error()
	}
	if latest, readErr := e.Store.AgentTask(ctx, task.ID); readErr == nil {
		view := protocol.AgentWorkflow{TaskID: task.ID, Checks: checks, CleanupWarning: out.CleanupWarning}
		e.Bus.Publish(latest.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: latest.ThreadID, TaskID: latest.ID, Task: &latest, Workflow: &view})
	}
	out.Checks = checks
	return e.withRuntimeStatus(ctx, task.ID, out)
}

// runtimeCheckSummary says why a head's workflow is not verified.
func runtimeCheckSummary(w runtimeWorkflow, headTree string) string {
	if w.HeadTree != headTree {
		return "no checks are planned for it; plan them with agent.integrate accept"
	}
	if len(w.Checks) == 0 {
		return "no checks planned, so it cannot be verified"
	}
	var parts []string
	for _, c := range w.Checks {
		if c.Status != "passed" {
			parts = append(parts, fmt.Sprintf("check %s is %s", c.Command, c.Status))
		}
	}
	return strings.Join(parts, "; ")
}

// publishRuntimeTask announces a change of the task's integration.
func (e *Engine) publishRuntimeTask(ctx context.Context, taskID string) {
	if t, err := e.Store.AgentTask(ctx, taskID); err == nil {
		e.Bus.Publish(t.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: t.ThreadID, TaskID: t.ID, Task: &t})
	}
}

// runtimeSpawnGate refuses new runtime agents once agent work is integrated
// into the project checkout (gap G1), with an explanation instead of the raw
// Git error the workspace preparation would end with. A replayed spawn (same
// call) still returns its stored agent.
type runtimeSpawnGate struct {
	agentrt.Service
	e *Engine
}

func (g runtimeSpawnGate) Spawn(ctx context.Context, req agentrt.SpawnRequest) (agentrt.SpawnResult, error) {
	if req.TaskID != "" && req.CallID != "" {
		if _, err := g.e.Store.RuntimeAgentForCall(ctx, req.TaskID, req.CallID); errors.Is(err, store.ErrNotFound) {
			if taken, err := g.e.runtimeCheckoutTaken(ctx, req.TaskID); err != nil {
				return agentrt.SpawnResult{}, err
			} else if taken {
				return agentrt.SpawnResult{}, errors.New("spawn refused: " + errRuntimeCheckoutTaken + ". Integrate, verify and complete this task, then start a new delegated task for further work")
			}
		}
	}
	return g.Service.Spawn(ctx, req)
}

// runtimeCheckoutTaken reports whether any revision of the task was applied
// or is being applied to the project checkout.
func (e *Engine) runtimeCheckoutTaken(ctx context.Context, taskID string) (bool, error) {
	revs, err := e.Store.AgentRevisions(ctx, taskID)
	if err != nil {
		return false, err
	}
	for _, r := range revs {
		if r.State == store.RevisionApplied || r.State == store.RevisionApplying {
			return true, nil
		}
	}
	return false, nil
}
