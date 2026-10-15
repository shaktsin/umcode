package delegation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

type Service interface {
	Spawn(context.Context, string, string, protocol.AgentRole, protocol.AgentAssignment) (protocol.AgentChild, error)
	Status(context.Context, string, string) (protocol.AgentChild, error)
	Wait(context.Context, string, []string) ([]protocol.AgentChild, error)
	Followup(context.Context, string, string, string, string) (protocol.AgentChild, error)
	Cancel(context.Context, string, string) error
}
type Runner interface {
	Run(context.Context, protocol.AgentTask, protocol.AgentChild) (protocol.AgentResult, error)
}

// EventSink must return promptly and must not reenter Coordinator. Events contain
// immutable snapshots. Versions and child IDs let clients ignore older updates.
type EventSink interface{ Publish(string, string, any) }
type CoordinatorOption func(*Coordinator)

func WithRunner(r Runner) CoordinatorOption       { return func(c *Coordinator) { c.runner = r } }
func WithEventSink(s EventSink) CoordinatorOption { return func(c *Coordinator) { c.events = s } }

type BusyError struct{ ChildID string }

func (e *BusyError) Error() string {
	return fmt.Sprintf("agent %s is still active; wait or cancel it before starting another worker", e.ChildID)
}

type lifecycleStore interface {
	CreateAgentChild(context.Context, protocol.AgentChild, string) (protocol.AgentChild, error)
	AgentChildForCall(context.Context, string, string) (protocol.AgentChild, error)
	AgentFollowup(context.Context, string, string, string) (protocol.AgentFollowup, error)
	EnqueueAgentFollowup(context.Context, string, string, string, string) (protocol.AgentChild, error)
	PendingAgentFollowups(context.Context, string, string) ([]protocol.AgentFollowup, error)
	AcknowledgeAgentFollowup(context.Context, string, string, string) error
}
type childRun struct {
	childID           string
	cancel            context.CancelFunc
	exited            bool
	cancelRequested   bool
	taskCancel        bool
	taskCancelDurable bool
	snapshot          protocol.AgentChild
	pending           *childOutcome
	finalErr          error
}
type childOutcome struct {
	result    protocol.AgentResult
	err       error
	cancelled bool
}

// FinalizationError reports a retained outcome that still needs durable saving.
type FinalizationError struct {
	ChildID string
	Cause   error
}

func (e *FinalizationError) Error() string {
	return fmt.Sprintf("agent %s outcome retained; retry cancellation after storage recovers: %v", e.ChildID, e.Cause)
}
func (e *FinalizationError) Unwrap() error { return e.Cause }

// FollowupQueue exposes durable messages to the injected runner. Call Pending at
// provider/tool boundaries only, then Acknowledge after adding the message to
// execution context. A crash before acknowledgement can redeliver a message.
type FollowupQueue interface {
	Pending(context.Context) ([]protocol.AgentFollowup, error)
	Acknowledge(context.Context, string) error
}
type queueKey struct{}
type followupQueue struct {
	s               lifecycleStore
	taskID, childID string
}

func (q *followupQueue) Pending(ctx context.Context) ([]protocol.AgentFollowup, error) {
	return q.s.PendingAgentFollowups(ctx, q.taskID, q.childID)
}
func (q *followupQueue) Acknowledge(ctx context.Context, callID string) error {
	return q.s.AcknowledgeAgentFollowup(ctx, q.taskID, q.childID, callID)
}
func FollowupsFromContext(ctx context.Context) FollowupQueue {
	q, _ := ctx.Value(queueKey{}).(FollowupQueue)
	return q
}

// WithChildCancellation registers the actual engine turn cancellation handle.
// Task5 must call RegisterChildCancellation when startTurn reconstructs context.
type cancellationKey struct{}
type childCancellation struct {
	mu        sync.Mutex
	cancelled bool
	handles   []context.CancelFunc
}

func (h *childCancellation) cancel() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelled = true
	for _, f := range h.handles {
		f()
	}
}
func RegisterChildCancellation(ctx context.Context, cancel context.CancelFunc) error {
	h, _ := ctx.Value(cancellationKey{}).(*childCancellation)
	if h == nil {
		return errors.New("missing child cancellation owner")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		cancel()
		return context.Canceled
	}
	h.handles = append(h.handles, cancel)
	return nil
}
func (c *Coordinator) lifecycleStore() (lifecycleStore, error) {
	s, ok := c.store.(lifecycleStore)
	if !ok {
		return nil, errors.New("delegation lifecycle storage unavailable")
	}
	return s, nil
}
func (c *Coordinator) notify(task protocol.AgentTask, child protocol.AgentChild) {
	c.notifyError(task, child, nil)
}
func (c *Coordinator) notifyError(task protocol.AgentTask, child protocol.AgentChild, err error) {
	if ch := c.changed[task.ID]; ch != nil {
		close(ch)
	}
	c.changed[task.ID] = make(chan struct{})
	if c.events != nil {
		event := protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: task.ID, Child: cloneChild(child)}
		if err != nil {
			event.Error = err.Error()
		}
		c.events.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, event)
	}
}
func cloneChild(child protocol.AgentChild) protocol.AgentChild {
	child.Assignment.AcceptanceChecks = append([]string(nil), child.Assignment.AcceptanceChecks...)
	child.Assignment.ContextRefs = append([]string(nil), child.Assignment.ContextRefs...)
	child.Result.Findings = append([]protocol.AgentFinding(nil), child.Result.Findings...)
	child.Result.Checks = append([]protocol.AgentCheckEvidence(nil), child.Result.Checks...)
	child.Result.ChangedPaths = append([]string(nil), child.Result.ChangedPaths...)
	return child
}
func (c *Coordinator) liveTask(ctx context.Context, taskID string) (protocol.AgentTask, context.Context, error) {
	task, err := c.store.AgentTask(ctx, taskID)
	if err != nil {
		return task, nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rt := c.tasks[taskID]
	if !active(task.State) || c.cancelled[taskID] || rt == nil {
		return task, nil, errors.New("delegation task is not admitted and active")
	}
	if task.ThreadID != rt.admitted.ThreadID || task.ProjectID != rt.admitted.ProjectID || task.RootTurnID != rt.admitted.RootTurnID || task.BaseRoot != rt.admitted.BaseRoot || task.BaseRevision != rt.admitted.BaseRevision || task.Activation != rt.admitted.Activation {
		return task, nil, errors.New("delegation task ownership changed")
	}
	if err = rt.ctx.Err(); err != nil {
		return task, nil, err
	}
	if err = rt.budget.Err(); err != nil {
		return task, nil, err
	}
	if why := rt.budget.StopReason(); why != "" {
		return task, nil, errors.New(why)
	}
	return task, rt.ctx, nil
}
func (c *Coordinator) busy(ctx context.Context, taskID string) error {
	if slot := c.slots[taskID]; slot != nil {
		return &BusyError{ChildID: slot.childID}
	}
	children, err := c.store.AgentChildren(ctx, taskID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if active(child.State) {
			return &BusyError{ChildID: child.ID}
		}
	}
	return nil
}
func (c *Coordinator) Spawn(ctx context.Context, taskID, callID string, role protocol.AgentRole, assignment protocol.AgentAssignment) (protocol.AgentChild, error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	s, err := c.lifecycleStore()
	if err != nil {
		return protocol.AgentChild{}, err
	}
	child, err := s.AgentChildForCall(ctx, taskID, callID)
	if err == nil {
		return cloneChild(child), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return child, err
	}
	task, owned, err := c.liveTask(ctx, taskID)
	if err != nil {
		return child, err
	}
	if c.runner == nil {
		return child, errors.New("delegation runner unavailable")
	}
	if err = c.busy(ctx, taskID); err != nil {
		return child, err
	}
	child, err = s.CreateAgentChild(ctx, protocol.AgentChild{TaskID: taskID, Role: role, Assignment: assignment}, callID)
	if err != nil {
		return child, err
	}
	c.notify(task, child)
	c.launch(task, child, owned, s)
	return cloneChild(child), nil
}
func (c *Coordinator) launch(task protocol.AgentTask, child protocol.AgentChild, owned context.Context, s lifecycleStore) {
	ctx, cancel := context.WithCancel(owned)
	handles := &childCancellation{}
	ctx = context.WithValue(ctx, queueKey{}, &followupQueue{s: s, taskID: task.ID, childID: child.ID})
	ctx = context.WithValue(ctx, cancellationKey{}, handles)
	slot := &childRun{childID: child.ID, snapshot: cloneChild(child), cancel: func() { cancel(); handles.cancel() }}
	c.slots[task.ID] = slot
	go c.run(ctx, task, child, slot, s)
}
func (c *Coordinator) run(ctx context.Context, task protocol.AgentTask, child protocol.AgentChild, slot *childRun, s lifecycleStore) {
	defer slot.cancel()
	c.lifecycleMu.Lock()
	saved, err := c.store.AgentChild(context.WithoutCancel(c.base), task.ID, child.ID)
	if err == nil && saved.State == protocol.AgentStateQueued && ctx.Err() == nil {
		saved.State = protocol.AgentStateRunning
		err = c.store.UpdateAgentChild(context.WithoutCancel(c.base), saved, saved.Version)
		if err == nil {
			saved, err = c.store.AgentChild(context.WithoutCancel(c.base), task.ID, child.ID)
		}
	}
	if err == nil {
		slot.snapshot = cloneChild(saved)
		c.notify(task, saved)
	}
	c.lifecycleMu.Unlock()
	var result protocol.AgentResult
	if err == nil && ctx.Err() == nil && saved.State == protocol.AgentStateRunning {
		result, err = c.runner.Run(ctx, task, cloneChild(saved))
	} else if err == nil {
		err = ctx.Err()
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	slot.exited = true
	slot.pending = &childOutcome{result: cloneChild(protocol.AgentChild{Result: result}).Result, err: err, cancelled: ctx.Err() != nil}
	if _, finalErr := c.finalize(context.WithoutCancel(c.base), task, slot, s); finalErr != nil {
		return
	}
	if slot.taskCancel && !slot.taskCancelDurable {
		return
	}
	delete(c.slots, task.ID)
}

// finalize runs under lifecycleMu. The pending outcome remains owned by the
// occupied slot until both its CAS write and confirmation read succeed.
func (c *Coordinator) finalize(ctx context.Context, task protocol.AgentTask, slot *childRun, s lifecycleStore) (protocol.AgentChild, error) {
	fail := func(cause error) (protocol.AgentChild, error) {
		slot.finalErr = &FinalizationError{ChildID: slot.childID, Cause: cause}
		snapshot := cloneChild(slot.snapshot)
		if slot.pending != nil && hasResult(slot.pending.result) {
			snapshot.Result = cloneChild(protocol.AgentChild{Result: slot.pending.result}).Result
		}
		c.notifyError(task, snapshot, slot.finalErr)
		return snapshot, slot.finalErr
	}
	current, err := c.store.AgentChild(ctx, task.ID, slot.childID)
	if err != nil {
		return fail(err)
	}
	slot.snapshot = cloneChild(current)
	outcome := slot.pending
	if outcome != nil {
		if hasResult(outcome.result) {
			current.Result = cloneChild(protocol.AgentChild{Result: outcome.result}).Result
		}
		if outcome.err != nil && !outcome.cancelled && current.Result.Blocker == "" {
			current.Result.Blocker = outcome.err.Error()
		}
		if active(current.State) {
			if outcome.cancelled || slot.cancelRequested {
				current.State = protocol.AgentStateCancelled
			} else if outcome.err != nil {
				current.State = protocol.AgentStateFailed
				if current.Result.Blocker == "" {
					current.Result.Blocker = outcome.err.Error()
				}
			} else {
				pending, queueErr := s.PendingAgentFollowups(ctx, task.ID, slot.childID)
				if queueErr != nil {
					return fail(queueErr)
				}
				if len(pending) > 0 {
					current.State = protocol.AgentStateFailed
					current.Result.Blocker = "undelivered correction retained; explicitly follow up after inspecting pending messages"
				} else {
					current.State = protocol.AgentStateCompleted
				}
			}
		}
		if err = c.store.UpdateAgentChild(ctx, current, current.Version); err != nil {
			return fail(err)
		}
		current, err = c.store.AgentChild(ctx, task.ID, slot.childID)
		if err != nil {
			return fail(err)
		}
	}
	slot.snapshot = cloneChild(current)
	slot.pending = nil
	slot.finalErr = nil
	c.notify(task, current)
	return cloneChild(current), nil
}
func (c *Coordinator) statusLocked(ctx context.Context, taskID, childID string) (protocol.AgentChild, error) {
	if slot := c.slots[taskID]; slot != nil && slot.childID == childID && slot.finalErr != nil {
		child := cloneChild(slot.snapshot)
		if slot.pending != nil && hasResult(slot.pending.result) {
			child.Result = cloneChild(protocol.AgentChild{Result: slot.pending.result}).Result
		}
		return child, slot.finalErr
	}
	child, err := c.store.AgentChild(ctx, taskID, childID)
	return cloneChild(child), err
}
func (c *Coordinator) Status(ctx context.Context, taskID, childID string) (protocol.AgentChild, error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.statusLocked(ctx, taskID, childID)
}
func (c *Coordinator) List(ctx context.Context, taskID string) ([]protocol.AgentChild, error) {
	return c.store.AgentChildren(ctx, taskID)
}
func (c *Coordinator) Wait(ctx context.Context, taskID string, childIDs []string) ([]protocol.AgentChild, error) {
	if len(childIDs) == 0 {
		return nil, errors.New("at least one child ID is required")
	}
	for {
		c.lifecycleMu.Lock()
		if c.changed[taskID] == nil {
			c.changed[taskID] = make(chan struct{})
		}
		changed := c.changed[taskID]
		out := make([]protocol.AgentChild, 0, len(childIDs))
		done := false
		for _, id := range childIDs {
			child, err := c.statusLocked(ctx, taskID, id)
			if err != nil {
				c.lifecycleMu.Unlock()
				return nil, err
			}
			out = append(out, child)
			done = done || !active(child.State)
		}
		c.mu.Lock()
		var taskDone <-chan struct{}
		if rt := c.tasks[taskID]; rt != nil {
			taskDone = rt.ctx.Done()
		}
		c.mu.Unlock()
		c.lifecycleMu.Unlock()
		if done {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-taskDone:
			out = nil
			for _, id := range childIDs {
				child, err := c.Status(context.WithoutCancel(ctx), taskID, id)
				if err != nil {
					return nil, err
				}
				out = append(out, child)
			}
			return out, context.Canceled
		case <-changed:
		}
	}
}
func (c *Coordinator) Followup(ctx context.Context, taskID, childID, callID, message string) (protocol.AgentChild, error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	s, err := c.lifecycleStore()
	if err != nil {
		return protocol.AgentChild{}, err
	}
	child, err := c.store.AgentChild(ctx, taskID, childID)
	if err != nil {
		return child, err
	}
	if _, err = s.AgentFollowup(ctx, taskID, childID, callID); err == nil {
		return cloneChild(child), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return child, err
	}
	task, owned, err := c.liveTask(ctx, taskID)
	if err != nil {
		return child, err
	}
	if c.runner == nil {
		return child, errors.New("delegation runner unavailable")
	}
	slot := c.slots[taskID]
	resume := !active(child.State)
	if resume {
		if err = c.busy(ctx, taskID); err != nil {
			return child, err
		}
	} else if slot == nil || slot.childID != childID || slot.cancelRequested {
		return child, errors.New("child is not running; recovery requires an explicit new task")
	}
	if strings.TrimSpace(message) == "" {
		return child, errors.New("follow-up message required")
	}
	child, err = s.EnqueueAgentFollowup(ctx, taskID, childID, callID, message)
	if err != nil {
		return child, err
	}
	c.notify(task, child)
	if resume {
		c.launch(task, child, owned, s)
	}
	return cloneChild(child), nil
}
func (c *Coordinator) Cancel(ctx context.Context, taskID, childID string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	slot := c.slots[taskID]
	if slot != nil && slot.childID == childID {
		slot.cancelRequested = true
		slot.cancel()
	}
	task, err := c.store.AgentTask(ctx, taskID)
	if err != nil {
		return err
	}
	child, err := c.store.AgentChild(ctx, taskID, childID)
	if err != nil {
		return err
	}
	for i := 0; i < 3 && active(child.State); i++ {
		child.State = protocol.AgentStateCancelled
		err = c.store.UpdateAgentChild(ctx, child, child.Version)
		if err == nil {
			child, err = c.store.AgentChild(ctx, taskID, childID)
			break
		}
		if !errors.Is(err, store.ErrAgentConflict) {
			break
		}
		reloaded, reloadErr := c.store.AgentChild(ctx, taskID, childID)
		if reloadErr != nil {
			return reloadErr
		}
		child = reloaded
		if !active(child.State) {
			err = nil
			break
		}
	}
	if err != nil {
		return err
	}
	if slot != nil && slot.childID == childID && slot.exited && slot.pending != nil {
		s, storeErr := c.lifecycleStore()
		if storeErr != nil {
			return storeErr
		}
		child, err = c.finalize(ctx, task, slot, s)
		if err != nil {
			return err
		}
	}
	if slot != nil && slot.childID == childID && slot.exited && (!slot.taskCancel || slot.taskCancelDurable) {
		delete(c.slots, taskID)
	}
	c.notify(task, child)
	return nil
}

var _ Service = (*Coordinator)(nil)

// InheritRunnerContext preserves the durable queue and actual-turn cancellation
// owner when the engine reconstructs its execution context. It grants no parent
// delegation authority. Task5 must still apply its child role/policy marker.
func InheritRunnerContext(ctx, from context.Context) context.Context {
	for _, key := range []any{queueKey{}, cancellationKey{}} {
		if value := from.Value(key); value != nil {
			ctx = context.WithValue(ctx, key, value)
		}
	}
	return ctx
}

func hasResult(r protocol.AgentResult) bool {
	return r.Summary != "" || r.Blocker != "" || len(r.Findings) > 0 || len(r.Checks) > 0 || len(r.ChangedPaths) > 0
}
