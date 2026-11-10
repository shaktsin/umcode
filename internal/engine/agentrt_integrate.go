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
const runtimeConflictText = `Conflict in %s. Nothing was applied. Options: agent.integrate {action:"drop", agent, reason}; or fix the files in the project checkout, stage them, then agent.integrate {action:"resolved", agent}. A new writer agent works on the task base, not on the integrated state, so it would hit the same conflict: to redo the work on top of the integrated state, finish this task (drop or resolve, verify, complete) and start a new delegated task.`

// runtimeAppliedFinal states the v1 dead end (see the known limitation on
// delegation.IntegrateRuntime): an applied revision cannot be dropped or
// amended, so a head that cannot be verified blocks every further accept and
// the completion.
const runtimeAppliedFinal = "applied revisions are final (they cannot be dropped or amended in this version): if the integrated state cannot pass its checks, report that to the user, who can cancel the task; the applied work stays staged in the project checkout for the user to keep or discard"

// errRuntimeIntegrationApplying refuses a spawn while a revision is being
// applied to the project checkout (it may be mid-write). Agents can otherwise
// be spawned at any time, also after accepts: a writer works on the task
// base, a read-only agent on a view of the integrated head.
const errRuntimeIntegrationApplying = delegation.IntegrationApplyingText

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
// An agent whose checkout is not prepared yet does not block an accept: its
// preparation and the accept are serialised by the task's integration lock,
// and a checkout can be prepared after accepts too.
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
	// A new agent can be spawned at any time (its revision is integrated
	// onto the current state like any other).
	const redo = "or start a new agent"
	switch as := state[agentID]; {
	case as == protocol.AgentStateCompleted:
	case !agentrt.IsTerminal(as):
		return fmt.Errorf("accept refused: agent %s is still finishing (%s); wait for it to complete (agent.wait), then accept", agentID, as)
	case as == protocol.AgentStateCancelled:
		return fmt.Errorf("accept refused: agent %s was cancelled after submitting; its revision cannot be integrated. Drop it (agent.integrate {action:\"drop\", agent, reason}) %s", agentID, redo)
	default:
		return fmt.Errorf("accept refused: agent %s ended %s after submitting; its revision cannot be integrated. Drop it (agent.integrate {action:\"drop\", agent, reason}) %s", agentID, as, redo)
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
		case r.State == store.RevisionApplying:
			// Drop and resolved both refuse an applying revision.
			return fmt.Errorf(order+": its apply was interrupted; accept agent %s again (that settles it from the project checkout) or, if that reports a blocked apply, restore the checkout as it says, before agent %s", r.AgentID, r.State, r.AgentID, agentID)
		case as == protocol.AgentStateCompleted:
			return fmt.Errorf(order+"; accept, drop or resolve it before agent %s", r.AgentID, r.State, agentID)
		case !agentrt.IsTerminal(as):
			return fmt.Errorf(order+" and its agent is still finishing (%s); wait for it and accept or drop it before agent %s", r.AgentID, r.State, as, agentID)
		default:
			return fmt.Errorf(order+" but its agent ended %s, so it cannot be integrated; drop it with a reason before agent %s", r.AgentID, r.State, as, agentID)
		}
	}
	if st.AppliedCount > 0 && !st.Verified {
		h, err := e.Store.AgentIntegrationHead(ctx, taskID)
		if err != nil {
			return err
		}
		w, err := store.DecodeRuntimeWorkflow(h.Workflow)
		if err != nil {
			return err
		}
		return fmt.Errorf("accept refused: the integrated state %s is not verified: %s", st.HeadTree, runtimeCheckSummary(w, st.HeadTree))
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
		checks = w.Checks
	}
	// The final verified check and the state change run under the task's
	// integration lock, so no check result is recorded in between.
	if err = svc.FinishRuntime(ctx, task.ID, st.HeadTree, st.AppliedCount, func() error {
		return e.Store.UpdateAgentTaskState(ctx, cur.ID, cur.ThreadID, cur.ProjectID, cur.State, protocol.AgentStateCompleted)
	}); err != nil {
		return out, fmt.Errorf("completion refused: %w", err)
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

// runtimeCheckSummary says why a head's workflow is not verified and what
// the lead can do: plan (pending plan), run the checks, or, when the head
// cannot be verified, the v1 dead end (runtimeAppliedFinal).
func runtimeCheckSummary(w runtimeWorkflow, headTree string) string {
	if w.HeadTree != headTree {
		return "plan pending: its checks are not planned yet; retry planning by repeating agent.integrate accept for the agent accepted last"
	}
	if len(w.Checks) == 0 {
		return "no checks planned, so it can never be verified; " + runtimeAppliedFinal
	}
	var parts []string
	failed := false
	for _, c := range w.Checks {
		if c.Status != "passed" {
			parts = append(parts, fmt.Sprintf("check %s is %s", c.Command, c.Status))
			failed = failed || c.Status == "failed" || c.Status == "blocked"
		}
	}
	if failed {
		return strings.Join(parts, "; ") + "; run the checks again with verification.run if they can pass, otherwise " + runtimeAppliedFinal
	}
	return strings.Join(parts, "; ") + "; run them with verification.run"
}

// publishRuntimeTask announces a change of the task's integration.
func (e *Engine) publishRuntimeTask(ctx context.Context, taskID string) {
	if t, err := e.Store.AgentTask(ctx, taskID); err == nil {
		e.Bus.Publish(t.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: t.ThreadID, TaskID: t.ID, Task: &t})
	}
}

// runtimeSpawnGate refuses new runtime agents while a revision of the task
// is being applied to the project checkout (the parent may be mid-write).
// Otherwise spawning stays possible after accepts. A replayed spawn (same
// call) still returns its stored agent. The agent's workspace preparation
// re-checks under the task's integration lock (delegation.PrepareRuntime), so
// an accept starting after this check is waited for, not observed half way.
type runtimeSpawnGate struct {
	agentrt.Service
	e *Engine
}

func (g runtimeSpawnGate) Spawn(ctx context.Context, req agentrt.SpawnRequest) (agentrt.SpawnResult, error) {
	if req.TaskID != "" && req.CallID != "" {
		if _, err := g.e.Store.RuntimeAgentForCall(ctx, req.TaskID, req.CallID); errors.Is(err, store.ErrNotFound) {
			if applying, found, err := g.e.runtimeIntegrationApplying(ctx, req.TaskID); err != nil {
				return agentrt.SpawnResult{}, err
			} else if found {
				return agentrt.SpawnResult{}, errors.New("spawn refused: " + delegation.IntegrationApplyingAdvice(applying))
			}
		}
	}
	return g.Service.Spawn(ctx, req)
}

// runtimeIntegrationApplying returns the revision being applied to the
// project checkout, if any.
func (e *Engine) runtimeIntegrationApplying(ctx context.Context, taskID string) (store.AgentRevision, bool, error) {
	revs, err := e.Store.AgentRevisions(ctx, taskID)
	if err != nil {
		return store.AgentRevision{}, false, err
	}
	for _, r := range revs {
		if r.State == store.RevisionApplying {
			return r, true, nil
		}
	}
	return store.AgentRevision{}, false, nil
}
