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
	ctx      context.Context
	cancel   context.CancelFunc
	budget   *Budget
	admitted protocol.AgentTask
}

// Coordinator owns task lifetimes, accounting, and child scheduling. Active
// slots remain occupied until execution exits and cancellation is durable.
// Task contexts inherit engine shutdown, never an RPC.
type Coordinator struct {
	mu          sync.Mutex
	base        context.Context
	store       TaskStore
	tasks       map[string]*taskRuntime
	cancelled   map[string]bool
	lifecycleMu sync.Mutex
	runner      Runner
	events      EventSink
	slots       map[string]*childRun
	changed     map[string]chan struct{}
}

func NewCoordinator(base context.Context, s TaskStore, options ...CoordinatorOption) *Coordinator {
	c := &Coordinator{base: base, store: s, tasks: map[string]*taskRuntime{}, cancelled: map[string]bool{}, slots: map[string]*childRun{}, changed: map[string]chan struct{}{}}
	for _, option := range options {
		option(c)
	}
	return c
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
	c.tasks[task.ID] = &taskRuntime{ctx: owned, cancel: cancel, budget: b, admitted: task}
	return owned, b, nil
}

// CancelTask first closes live execution, then durably terminalizes the task and
// active children. Errors are surfaced; a failed write never reopens admission.
// Calling code must not free a child slot until its runner exits and this write
// succeeds. Partial results/workspace references are retained by scoped CAS.
func (c *Coordinator) CancelTask(ctx context.Context, taskID string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	slot := c.slots[taskID]
	if slot != nil {
		slot.cancelRequested = true
		slot.taskCancel = true
		slot.cancel()
	}
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
	if slot != nil && slot.exited && slot.pending != nil {
		s, storeErr := c.lifecycleStore()
		if storeErr != nil {
			errs = append(errs, storeErr)
		} else if _, finalErr := c.finalize(ctx, task, slot, s); finalErr != nil {
			errs = append(errs, finalErr)
		}
	}
	cancelErr := errors.Join(errs...)
	if slot != nil && cancelErr == nil {
		slot.taskCancelDurable = true
		if slot.exited {
			delete(c.slots, taskID)
		}
	}
	for _, child := range children {
		// finalize already published the retained outcome and actionable error.
		// A durable snapshot cannot clear that error until recovery succeeds.
		if slot != nil && slot.childID == child.ID && slot.finalErr != nil {
			continue
		}
		if latest, readErr := c.store.AgentChild(ctx, taskID, child.ID); readErr == nil {
			c.notify(task, latest)
		}
	}
	return cancelErr
}
