package engine

import (
	"context"
	"errors"
	"sync"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/protocol"
)

// errParentBudgetGone fails a nested agent closed when its parent agent's
// budget no longer exists (the parent is terminal and no slice was reserved
// at spawn): slicing the task instead would let the subtree outspend the
// parent's slice.
var errParentBudgetGone = errors.New("the parent agent's budget is gone (the parent has finished); the agent cannot run")

// shareEpsilon matches the runtime's float tolerance for budget shares.
const shareEpsilon = 1e-9

// agentBudget is one runtime agent's budget slice and its absolute share of
// the task (the plan's BudgetShare), which its children's shares are relative to.
type agentBudget struct {
	budget *delegation.Budget
	share  float64
}

// agentBudgetRegistry maps task ID -> agent ID -> the agent's budget slice,
// so a nested agent can slice its parent's slice. An entry is added at spawn
// (or lazily when the runner starts) and removed when the agent is terminal:
// after its runner returns, on a terminal runtime event (an agent cancelled
// before it ran) and, as a backstop, together with the task's budget.
type agentBudgetRegistry struct {
	mu    sync.Mutex
	tasks map[string]map[string]agentBudget
}

func (r *agentBudgetRegistry) get(taskID, id string) (agentBudget, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.tasks[taskID][id]
	return b, ok
}

// put stores b for the agent unless one is already there, and returns the
// stored one.
func (r *agentBudgetRegistry) put(taskID, id string, b *delegation.Budget, share float64) *delegation.Budget {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.tasks[taskID][id]; ok {
		return cur.budget
	}
	if r.tasks == nil {
		r.tasks = map[string]map[string]agentBudget{}
	}
	if r.tasks[taskID] == nil {
		r.tasks[taskID] = map[string]agentBudget{}
	}
	r.tasks[taskID][id] = agentBudget{budget: b, share: share}
	return b
}

func (r *agentBudgetRegistry) remove(taskID, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks[taskID], id)
	if len(r.tasks[taskID]) == 0 {
		delete(r.tasks, taskID)
	}
}

func (r *agentBudgetRegistry) dropTask(taskID string) {
	r.mu.Lock()
	delete(r.tasks, taskID)
	r.mu.Unlock()
}

// len counts entries across tasks. A test seam.
func (r *agentBudgetRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.tasks {
		n += len(m)
	}
	return n
}

// childShare is a nested agent's share of its parent's slice: both shares
// are fractions of the task, so the child gets childShare/parentShare of the
// parent's slice, clamped to [epsilon, 1].
func childShare(child, parent float64) float64 {
	if parent <= shareEpsilon {
		return 1
	}
	return min(max(child/parent, shareEpsilon), 1)
}

// sliceRuntimeAgentBudget carves a's slice from its parent's budget: the
// task budget for the lead's agents (share a.BudgetShare), the parent
// agent's slice for nested agents (share relative to the parent's share).
func (e *Engine) sliceRuntimeAgentBudget(ctx context.Context, a protocol.RuntimeAgent) (*delegation.Budget, error) {
	if a.ParentAgentID == "" {
		shared := e.runtimeTaskBudget(ctx, a.TaskID)
		if shared == nil {
			return nil, errors.New("task budget unavailable")
		}
		return shared.Slice(a.BudgetShare), nil
	}
	parent, ok := e.runtimeAgentBudgets.get(a.TaskID, a.ParentAgentID)
	if !ok {
		return nil, errParentBudgetGone
	}
	return parent.budget.Slice(childShare(a.BudgetShare, parent.share)), nil
}

// admitRuntimeAgentBudget reserves a newly spawned agent's slice at spawn
// (agentrt.WithAdmitted), so its size is its share of what its parent had
// left when it was spawned, not of what earlier siblings left over: a lead
// that spawns A (0.5) and B (0.5) gives each half of the remaining task
// budget even when B runs only after A finished. Spend is still charged to
// the parent as it happens, so the parent's budget stays the hard cap.
// Failures are left to the runner, which fails closed.
func (e *Engine) admitRuntimeAgentBudget(ctx context.Context, a protocol.RuntimeAgent) {
	if b, err := e.sliceRuntimeAgentBudget(ctx, a); err == nil {
		e.runtimeAgentBudgets.put(a.TaskID, a.ID, b, a.BudgetShare)
	}
}

// runtimeAgentBudget returns the agent's slice reserved at spawn, or carves
// it now (an agent created without the spawn hook).
func (e *Engine) runtimeAgentBudget(ctx context.Context, a protocol.RuntimeAgent) (*delegation.Budget, error) {
	if b, ok := e.runtimeAgentBudgets.get(a.TaskID, a.ID); ok {
		return b.budget, nil
	}
	b, err := e.sliceRuntimeAgentBudget(ctx, a)
	if err != nil {
		return nil, err
	}
	return e.runtimeAgentBudgets.put(a.TaskID, a.ID, b, a.BudgetShare), nil
}

// releaseRuntimeAgentBudget drops the agent's entry. Its running children
// keep their own slices, which still charge it.
func (e *Engine) releaseRuntimeAgentBudget(taskID, id string) {
	e.runtimeAgentBudgets.remove(taskID, id)
}

// runtimeAgentEvent drops a terminal agent's budget entry, including one
// cancelled before its runner ever started, and announces the change to the
// task's chat.
func (e *Engine) runtimeAgentEvent(a protocol.RuntimeAgent) {
	if agentrt.IsTerminal(a.State) {
		e.releaseRuntimeAgentBudget(a.TaskID, a.ID)
	}
	e.publishRuntimeAgent(a.TaskID, a.ID)
}

// runtimeAgentOptions are the engine's agent runtime options.
func (e *Engine) runtimeAgentOptions() []agentrt.Option {
	return []agentrt.Option{
		agentrt.WithResolveOptionsFunc(e.runtimeResolveOptions),
		agentrt.WithAdmitted(e.admitRuntimeAgentBudget),
		agentrt.WithEvents(e.runtimeAgentEvent),
	}
}
