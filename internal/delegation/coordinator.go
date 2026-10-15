package delegation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

type TaskStore interface {
	AgentTask(context.Context, string) (protocol.AgentTask, error)
	AgentTaskBudget(context.Context, string, protocol.ExecutionLimits) (protocol.ExecutionLimits, protocol.UsageTotals, error)
	UpdateAgentTaskBudget(context.Context, string, protocol.UsageTotals) error
	UpdateAgentTaskState(context.Context, string, string, string, protocol.AgentState, protocol.AgentState) error
	AgentChildren(context.Context, string) ([]protocol.AgentChild, error)
	AgentChild(context.Context, string, string) (protocol.AgentChild, error)
	UpdateAgentChild(context.Context, protocol.AgentChild, int64) error
}

type taskRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	budget *Budget
}

// Coordinator owns task lifetimes and accounting only. Runner/Service own child
// scheduling and must retain active slots until cancellation is durably saved
// and the runner has exited. Task contexts inherit engine shutdown, never an RPC.
type Coordinator struct {
	mu        sync.Mutex
	base      context.Context
	store     TaskStore
	tasks     map[string]*taskRuntime
	cancelled map[string]bool
}

func NewCoordinator(base context.Context, s TaskStore) *Coordinator {
	return &Coordinator{base: base, store: s, tasks: map[string]*taskRuntime{}, cancelled: map[string]bool{}}
}
func active(state protocol.AgentState) bool {
	return state == protocol.AgentStateQueued || state == protocol.AgentStateRunning || state == protocol.AgentStateWaitingApproval
}

// Task revalidates durable admission and returns the same owner for follow-ups.
// It does not execute work or resume anything on construction/restart.
func (c *Coordinator) Task(ctx context.Context, admitted protocol.AgentTask, limits protocol.ExecutionLimits) (context.Context, *Budget, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.base.Err(); err != nil {
		return nil, nil, err
	}
	if c.cancelled[admitted.ID] {
		return nil, nil, context.Canceled
	}
	task, err := c.store.AgentTask(ctx, admitted.ID)
	if err != nil {
		return nil, nil, err
	}
	if task.ThreadID != admitted.ThreadID || task.ProjectID != admitted.ProjectID || !active(task.State) {
		return nil, nil, errors.New("delegation task is foreign or no longer active")
	}
	if rt := c.tasks[task.ID]; rt != nil {
		return rt.ctx, rt.budget, nil
	}
	limits, usage, err := c.store.AgentTaskBudget(ctx, task.ID, limits)
	if err != nil {
		return nil, nil, err
	}
	b := NewBudget(limits, usage, func(actual protocol.UsageTotals) error {
		return c.store.UpdateAgentTaskBudget(context.WithoutCancel(c.base), task.ID, actual)
	}, WithStartedAt(task.CreatedAt))
	var owned context.Context
	var cancel context.CancelFunc
	if limits.MaxDurationMinutes > 0 {
		owned, cancel = context.WithDeadline(WithBudget(c.base, b), b.deadline)
	} else {
		owned, cancel = context.WithCancel(WithBudget(c.base, b))
	}
	b.owner = owned
	c.tasks[task.ID] = &taskRuntime{ctx: owned, cancel: cancel, budget: b}
	return owned, b, nil
}

// CancelTask first closes live execution, then durably terminalizes the task and
// active children. Errors are surfaced; a failed write never reopens admission.
// Calling code must not free a child slot until its runner exits and this write
// succeeds. Partial results/workspace references are retained by scoped CAS.
func (c *Coordinator) CancelTask(ctx context.Context, taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Live cancellation must survive even an unavailable lifecycle store.
	c.cancelled[taskID] = true
	if rt := c.tasks[taskID]; rt != nil {
		rt.cancel()
	}
	task, err := c.store.AgentTask(ctx, taskID)
	if err != nil {
		return err
	}
	var errs []error
	if active(task.State) {
		for i := 0; i < 3; i++ {
			err = c.store.UpdateAgentTaskState(ctx, task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCancelled)
			if !errors.Is(err, store.ErrAgentConflict) {
				break
			}
			reloaded, reloadErr := c.store.AgentTask(ctx, taskID)
			if reloadErr != nil {
				err = reloadErr
				break
			}
			task = reloaded
			if !active(task.State) {
				err = nil
				break
			}
			// Keep ErrAgentConflict until a write succeeds or a reload proves
			// terminal state. A successful active-record read is not cancellation.
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("cancel task: %w", err))
		}
	}
	children, err := c.store.AgentChildren(ctx, taskID)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, child := range children {
		if !active(child.State) {
			continue
		}
		for i := 0; i < 3; i++ {
			child.State = protocol.AgentStateCancelled
			err = c.store.UpdateAgentChild(ctx, child, child.Version)
			if !errors.Is(err, store.ErrAgentConflict) {
				break
			}
			reloaded, reloadErr := c.store.AgentChild(ctx, taskID, child.ID)
			if reloadErr != nil {
				err = reloadErr
				break
			}
			child = reloaded
			if !active(child.State) {
				err = nil
				break
			}
			// An active reload must not erase the failed CAS on the last retry.
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("cancel child %s: %w", child.ID, err))
		}
	}
	return errors.Join(errs...)
}
