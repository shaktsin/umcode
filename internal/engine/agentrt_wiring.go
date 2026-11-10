package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// runtimeCloseTimeout bounds how long Shutdown waits for runtime agents.
const runtimeCloseTimeout = 10 * time.Second

// registerAgentRuntime builds the agent runtime on base and registers its
// model-facing tools in place of the legacy delegation agent tools. The lead
// keeps the default RootCapabilities authority (agentrt.WithLeadCapabilities).
func (e *Engine) registerAgentRuntime(reg *tools.Registry, base context.Context) {
	e.AgentRuntime = agentrt.NewRuntime(e.Store, runtimeRunner{engine: e}, base)
	for _, tool := range tools.NewRuntimeAgentTools(e.AgentRuntime, e.authorizeDelegationTool, runtimeCaller) {
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

// rememberRuntimeBudget records the shared budget the task's lead turns run
// under; runtime agents slice it.
func (e *Engine) rememberRuntimeBudget(taskID string, b *delegation.Budget) {
	if b != nil {
		e.runtimeBudgets.Store(taskID, b)
	}
}

// forgetRuntimeBudget drops the task's tracker guard first (so a fresh budget
// can be tracked again), then its budget.
func (e *Engine) forgetRuntimeBudget(taskID string) {
	e.runtimeBudgetTrackers.Delete(taskID)
	e.runtimeBudgets.Delete(taskID)
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
	go e.trackRuntimeBudgetUntilIdle(taskID)
}

func (e *Engine) trackRuntimeBudgetUntilIdle(taskID string) {
	wait := runtimeBudgetPoll
	for !e.runtimeTaskIdle(taskID) {
		select {
		case <-e.baseCtx.Done():
			e.forgetRuntimeBudget(taskID)
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, runtimeBudgetPollMax)
	}
	e.forgetRuntimeBudget(taskID)
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
