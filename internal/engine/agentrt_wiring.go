package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// runtimeCloseTimeout bounds how long Shutdown waits for runtime agents.
const runtimeCloseTimeout = 10 * time.Second

// runtimeCloseBudget is how long Shutdown lets the agent runtime stop: at most
// runtimeCloseTimeout and at most half of the caller's remaining time (never
// less than 1s), so a hung runner cannot starve closing plugins and MCP.
func runtimeCloseBudget(ctx context.Context) time.Duration {
	d := runtimeCloseTimeout
	if deadline, ok := ctx.Deadline(); ok {
		d = min(d, max(time.Until(deadline)/2, time.Second))
	}
	return d
}

// registerAgentRuntime builds the agent runtime on base and registers its
// model-facing tools in place of the legacy delegation agent tools (including
// agent.integrate, which replaces the legacy workflow action, so it must run
// after the workflow tools are registered). The lead
// keeps the default RootCapabilities authority (agentrt.WithLeadCapabilities).
func (e *Engine) registerAgentRuntime(reg *tools.Registry, base context.Context) {
	e.AgentRuntime = agentrt.NewRuntime(e.Store, runtimeRunner{engine: e}, base, e.runtimeAgentOptions()...)
	for _, tool := range tools.NewRuntimeAgentTools(runtimeSpawnGate{Service: e.AgentRuntime, e: e}, e.authorizeDelegationTool, runtimeCaller) {
		reg.Add(tool)
	}
	// Lead-only, never granted through capabilities (agentrt.AllowedTools).
	e.registerRuntimeIntegrate(reg)
	// Lead-only activation tools: agent.suggest, offered only in turns that
	// are not admitted for delegation (see suggestOnlyTool), and
	// agent.propose_plan, offered only in admitted turns like agent.spawn.
	for _, tool := range tools.NewRuntimeActivationTools(tools.RuntimeActivation{Suggest: e.suggestAgents, ProposePlan: e.proposeAgentPlan, Caller: runtimeCaller}) {
		reg.Add(tool)
	}
}

// runtimeCaller resolves the calling agent for the runtime agent tools: a
// runtime agent's own turn resolves to its ID (so its capabilities, not the
// lead's, bound what it spawns), the lead's own turn (no child marker)
// resolves as the lead (""). Legacy child contexts and anything ambiguous,
// such as an ID that does not match the context's execution, fail closed.
func runtimeCaller(ctx context.Context) (string, bool) {
	if id, ok := runtimeAgentFrom(ctx); ok {
		if x := childExecutionFrom(ctx); x != nil && (x.runtime == nil || x.runtime.agent.ID != id) {
			return "", false
		}
		return id, true
	}
	if delegationChild(ctx) || childExecutionFrom(ctx) != nil {
		return "", false
	}
	return "", true
}

// authorizeRuntimeAgentTool authorizes an agent.* call made on a runtime
// agent's own turn. Its context carries the task of its execution instead of
// an admitted parent scope (its working folder is its own checkout), so the
// task is re-read from the store and must still match that execution and its
// chat, and be active. Arguments never select a task.
func (e *Engine) authorizeRuntimeAgentTool(ctx context.Context, id string) (protocol.AgentTask, error) {
	x := childExecutionFrom(ctx)
	if x == nil || x.runtime == nil || x.runtime.agent.ID != id || !delegationChild(ctx) {
		return protocol.AgentTask{}, errors.New("delegation requires an admitted parent task")
	}
	task, err := e.Store.AgentTask(ctx, x.task.ID)
	if err != nil {
		return task, err
	}
	if task.ThreadID != x.task.ThreadID || task.ProjectID != x.task.ProjectID || task.RootTurnID != x.task.RootTurnID || task.BaseRoot != x.task.BaseRoot || task.ParentRoot != x.task.ParentRoot || task.BaseRevision != x.task.BaseRevision || task.Activation != x.task.Activation {
		return task, errors.New("delegation task ownership changed")
	}
	if !unfinishedDelegation(task.State) {
		return task, fmt.Errorf("delegation task %s is no longer active", task.ID)
	}
	agent, err := e.Store.RuntimeAgent(ctx, task.ID, id)
	if err != nil {
		return task, err
	}
	if agent.TaskID != task.ID {
		return task, errors.New("runtime agent belongs to another task")
	}
	return task, nil
}

// cancelRuntimeAgents cancels every runtime agent of the task, with their
// descendants, as the lead. It is a no-op when the runtime is off.
func (e *Engine) cancelRuntimeAgents(ctx context.Context, taskID string) error {
	if e.AgentRuntime == nil {
		return nil
	}
	agents, err := e.AgentRuntime.List(ctx, taskID, "")
	if err != nil {
		return fmt.Errorf("list runtime agents: %w", err)
	}
	var errs []error
	for _, a := range agents {
		if a.ParentAgentID == "" {
			if err = e.AgentRuntime.Cancel(ctx, taskID, "", a.ID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// reconcileRuntimeIntegrations runs at startup, whatever agents.runtime_v2
// says (revisions left by an earlier flag-on run still need it). For every
// task with a revision left applying, or a head that missed its last applied
// revision, it settles the revision from the actual parent tree
// (ReconcileRuntimeIntegration): tree before ⇒ back to submitted (or conflict)
// for the lead to accept again; tree after ⇒ applied; anything else ⇒ left
// applying, blocked with its reason. It never applies or resumes anything
// (the apply is predicted read-only, never run). A task that cannot be
// settled, for example because its project folder is gone, is logged and
// does not stop the engine, nor does failing to list them; the next accept or
// completion reports it.
func (e *Engine) reconcileRuntimeIntegrations(ctx context.Context) {
	ids, err := e.Store.RuntimeIntegrationsToReconcile(ctx)
	if err != nil {
		// Never fatal: the engine must start whatever runtime_v2 says; an
		// unsettled revision is still reconciled by the next accept or
		// completion.
		e.Log.Warn("could not list runtime integrations to reconcile", "err", fmt.Errorf("list runtime integrations to reconcile: %w", err))
		return
	}
	for _, id := range ids {
		if err = e.DelegationWorkspaceService.ReconcileRuntimeIntegration(ctx, id); err != nil {
			e.Log.Warn("agent integration left unsettled after restart", "task", id, "err", err)
		}
	}
}

// cancelledIntegrationReport describes what a cancelled task leaves in the
// project checkout, or nil when the task has no runtime agent revision. The
// caller calls it only for a task the cancel left cancelled. It
// waits for an accept in flight (the service's per-task integration lock):
// the cancel is already recorded, so the accept either stops at its next
// guard, before git writes anything, or, once git is writing, finishes and is
// recorded; it is never half applied. Then it settles any revision left
// applying. Applied work stays staged and nothing is reverted; agent
// workspaces are retained. Reconcile failures are reported, not returned: the
// cancel itself has succeeded.
func (e *Engine) cancelledIntegrationReport(ctx context.Context, taskID string) (*protocol.AgentIntegrationReport, error) {
	svc := e.DelegationWorkspaceService
	if svc == nil {
		return nil, nil
	}
	revs, err := e.Store.AgentRevisions(ctx, taskID)
	if err != nil || len(revs) == 0 {
		return nil, err
	}
	report := &protocol.AgentIntegrationReport{}
	if err = svc.ReconcileRuntimeIntegration(ctx, taskID); err != nil {
		report.Blocked = err.Error()
	}
	st, err := svc.RuntimeIntegrationStatus(ctx, taskID)
	if err != nil {
		// For example the project folder is gone: still a successful cancel.
		if report.Blocked == "" {
			report.Blocked = err.Error()
		}
		report.Message = "The integration state could not be read (" + err.Error() + "). Applied agent work, if any, stays staged in the project checkout; nothing was reverted. Agent workspaces are retained."
		return report, nil
	}
	report.HeadTree, report.AppliedCount, report.Verified = st.HeadTree, st.AppliedCount, st.Verified
	for _, r := range st.Revisions {
		switch r.State {
		case store.RevisionApplied:
			report.Applied = append(report.Applied, r.AgentID)
		case store.RevisionApplying:
			// Not settled by the reconcile: the checkout may hold part of it.
			report.BlockedAgents = append(report.BlockedAgents, r.AgentID)
		case store.RevisionDropped:
		default:
			report.Unintegrated = append(report.Unintegrated, r.AgentID)
		}
	}
	report.Message = integrationReportMessage(report)
	return report, nil
}

func integrationReportMessage(r *protocol.AgentIntegrationReport) string {
	var b strings.Builder
	switch {
	case r.AppliedCount == 0 && len(r.BlockedAgents) == 0:
		b.WriteString("No agent work was integrated; agents did not change the project checkout.")
	case r.AppliedCount == 0:
		b.WriteString("No agent revision was recorded as applied; nothing was reverted.")
	case r.Verified:
		fmt.Fprintf(&b, "%d agent revision(s) (%s) stay applied (staged) in the project checkout; nothing was reverted. Their integrated result %s passed its checks.",
			r.AppliedCount, strings.Join(r.Applied, ", "), r.HeadTree)
	default:
		fmt.Fprintf(&b, "%d agent revision(s) (%s) stay applied (staged) in the project checkout; nothing was reverted. Their integrated result %s is not verified: its checks have not all passed, so review the staged changes before committing them.",
			r.AppliedCount, strings.Join(r.Applied, ", "), r.HeadTree)
	}
	if len(r.BlockedAgents) > 0 {
		fmt.Fprintf(&b, " The project checkout may hold partly integrated, unverified work of %s, mixed with other edits: the apply was interrupted and could not be settled, and the cancelled task will not integrate it. Inspect the staged changes and restore or keep them yourself before committing.",
			strings.Join(r.BlockedAgents, ", "))
	}
	if len(r.Unintegrated) > 0 {
		fmt.Fprintf(&b, " Not integrated (never applied): %s.", strings.Join(r.Unintegrated, ", "))
	}
	if r.Blocked != "" {
		fmt.Fprintf(&b, " Details: %s.", r.Blocked)
	}
	b.WriteString(" Agent workspaces are retained.")
	return b.String()
}

// rememberRuntimeBudget records the shared budget the task's lead turns run
// under; runtime agents slice it.
func (e *Engine) rememberRuntimeBudget(taskID string, b *delegation.Budget) {
	if b != nil {
		e.runtimeBudgets.Store(taskID, b)
	}
}

// forgetRuntimeBudget drops the task's tracker guard first (so a fresh budget
// can be tracked again), then compare-and-deletes b: a budget registered for
// the task in the meantime is never clobbered, and is handed to a tracker
// (the one that raced in, or a new one) so it cannot leak either.
func (e *Engine) forgetRuntimeBudget(taskID string, b *delegation.Budget) {
	e.runtimeBudgetTrackers.Delete(taskID)
	// The task has no live agent left: drop any agent slices it still holds
	// (a backstop; each is normally removed when its agent is terminal).
	e.runtimeAgentBudgets.dropTask(taskID)
	if e.runtimeBudgets.CompareAndDelete(taskID, b) {
		return
	}
	if cur, ok := e.runtimeBudgets.Load(taskID); ok {
		e.trackRuntimeBudget(taskID, cur.(*delegation.Budget))
	}
}

// Backoff for the budget tracker's checks of whether its task is still active.
const (
	runtimeBudgetPoll    = 100 * time.Millisecond
	runtimeBudgetPollMax = 2 * time.Second
)

// trackRuntimeBudget registers the task's budget and, once per task, starts a
// tracker that drops it when the task is no longer active (completed, failed,
// cancelled, ...) AND none of its runtime agents is queued or running, so a
// running runner never loses its budget and finished tasks do not leak. It
// does not depend on how the task ended. It stops when the engine shuts down.
func (e *Engine) trackRuntimeBudget(taskID string, b *delegation.Budget) {
	e.rememberRuntimeBudget(taskID, b)
	if _, loaded := e.runtimeBudgetTrackers.LoadOrStore(taskID, struct{}{}); loaded {
		return
	}
	go e.trackRuntimeBudgetUntilIdle(taskID, b)
}

func (e *Engine) trackRuntimeBudgetUntilIdle(taskID string, b *delegation.Budget) {
	wait := runtimeBudgetPoll
	for !e.runtimeTaskIdle(taskID) {
		select {
		case <-e.baseCtx.Done():
			e.forgetRuntimeBudget(taskID, b)
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, runtimeBudgetPollMax)
	}
	e.forgetRuntimeBudget(taskID, b)
}

// runtimeTaskIdle reports that the task left the active states and has no live
// runtime agent. Any read error counts as not idle (retry later), except a
// missing task.
func (e *Engine) runtimeTaskIdle(taskID string) bool {
	ctx := context.WithoutCancel(e.baseCtx)
	task, err := e.Store.AgentTask(ctx, taskID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return false
	case unfinishedDelegation(task.State):
		return false
	}
	return !e.runtimeAgentsLive(taskID)
}

// runtimeAgentsLive reports whether any runtime agent of the task is queued or
// running. An unreadable list counts as live (retry later).
func (e *Engine) runtimeAgentsLive(taskID string) bool {
	if e.AgentRuntime == nil {
		return false
	}
	agents, err := e.Store.RuntimeAgents(context.WithoutCancel(e.baseCtx), taskID)
	if err != nil {
		return true
	}
	for _, a := range agents {
		if a.State == protocol.AgentStateQueued || a.State == protocol.AgentStateRunning {
			return true
		}
	}
	return false
}

// runtimeTaskBudget returns the shared budget a runtime agent of the task
// slices: the one on ctx if any, else the one its lead turn registered.
func (e *Engine) runtimeTaskBudget(ctx context.Context, taskID string) *delegation.Budget {
	if b := delegation.BudgetFromContext(ctx); b != nil {
		return b
	}
	if b, ok := e.runtimeBudgets.Load(taskID); ok {
		return b.(*delegation.Budget)
	}
	return nil
}

type toolOccurrenceKey struct{}

// withToolOccurrence records the per-turn occurrence of the provider call ID
// being executed (see nextToolReductionKey): providers without call IDs reuse
// synthetic IDs such as "call_0" in every model iteration of a turn.
func withToolOccurrence(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, toolOccurrenceKey{}, n)
}

func toolOccurrenceFrom(ctx context.Context) int {
	if n, ok := ctx.Value(toolOccurrenceKey{}).(int); ok && n > 0 {
		return n
	}
	return 1
}

// runtimeAgentCallID is the idempotency key the agent runtime stores for a
// tool call: the turn, the per-turn occurrence and the provider call ID. A
// genuine retry of one call (same turn, call ID and occurrence) maps to the
// same key; a reused synthetic call ID in a later iteration does not. Turn IDs
// and occurrences contain no ':', so the provider ID is the unambiguous tail.
func runtimeAgentCallID(turnID, callID string, occurrence int) string {
	if callID == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d:%s", turnID, occurrence, callID)
}
