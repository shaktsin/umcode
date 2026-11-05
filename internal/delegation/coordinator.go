package delegation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

type TaskStore interface {
	AgentTask(context.Context, string) (protocol.AgentTask, error)
	AgentTaskBudget(context.Context, string, protocol.ExecutionLimits) (protocol.ExecutionLimits, protocol.UsageTotals, error)
	UpdateAgentTaskBudget(context.Context, string, protocol.UsageTotals) error
	UpdateAgentTaskState(context.Context, string, string, string, protocol.AgentState, protocol.AgentState) error
}

type taskRuntime struct {
	ctx      context.Context
	cancel   context.CancelFunc
	budget   *Budget
	admitted protocol.AgentTask
}

// Coordinator is the registry of live delegation tasks: it owns each task's
// lifetime context and shared budget, its cancellation and the sequence of
// delegation/updated events. It schedules nothing; the agent runtime runs the
// agents. Task contexts inherit engine shutdown, never an RPC.
type Coordinator struct {
	mu        sync.Mutex
	base      context.Context
	store     TaskStore
	tasks     map[string]*taskRuntime
	cancelled map[string]bool
	// seqMu guards sequence, the per-process counter every delegation/updated
	// event of this engine is stamped with (NextSequence).
	seqMu    sync.Mutex
	sequence int64
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
	c.tasks[task.ID] = &taskRuntime{ctx: owned, cancel: cancel, budget: b, admitted: task}
	return owned, b, nil
}

// CancelTask first closes live execution, then durably marks the task
// cancelled. Errors are surfaced; a failed write never reopens admission.
// Callers cancel the task's runtime agents first (cancelRuntimeAgents), so
// they end cancelled rather than failed by the task closing under them; the
// runtime keeps their execution rows in step.
func (c *Coordinator) CancelTask(ctx context.Context, taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Live cancellation must survive even an unavailable store.
	c.cancelled[taskID] = true
	if rt := c.tasks[taskID]; rt != nil {
		rt.cancel()
	}
	task, err := c.store.AgentTask(ctx, taskID)
	if err != nil {
		return err
	}
	if !active(task.State) {
		return nil
	}
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
		return fmt.Errorf("cancel task: %w", err)
	}
	return nil
}

// NextSequence returns the next delegation/updated sequence number: strictly
// increasing within the process. Microsecond stamps (safe as JavaScript
// integers) also order delayed deliveries across engine restarts. Runtime
// agent and integration events share it.
func (c *Coordinator) NextSequence() int64 {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	next := time.Now().UnixMicro()
	if next <= c.sequence {
		next = c.sequence + 1
	}
	c.sequence = next
	return next
}

// LiveBudget is a read-only view of an already-admitted owner.
func (c *Coordinator) LiveBudget(taskID string) (protocol.UsageTotals, string, bool) {
	c.mu.Lock()
	rt := c.tasks[taskID]
	c.mu.Unlock()
	if rt == nil {
		return protocol.UsageTotals{}, "", false
	}
	return rt.budget.Usage(), rt.budget.StopReason(), true
}
