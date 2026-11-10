package agentrt

import (
	"context"
	"fmt"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Runner executes one runtime agent until it finishes or ctx is cancelled.
type Runner interface {
	Run(ctx context.Context, a protocol.RuntimeAgent) (protocol.AgentResult, error)
}

// Scheduler promotes queued runtime agents to running per task, up to the
// task's maxConcurrent, in creation (submission) order. The store is the
// source of truth for agent state: every transition reloads the agent and
// is applied under optimistic versioning, and terminal states are never
// overwritten.
type Scheduler struct {
	store    AgentStore
	runner   Runner
	base     context.Context
	onChange func(protocol.RuntimeAgent)

	mu    sync.Mutex
	tasks map[string]*taskSched
	wg    sync.WaitGroup
}

type runningAgent struct {
	agent           protocol.RuntimeAgent
	cancel          context.CancelFunc
	cancelRequested bool
}

type taskSched struct {
	max     int
	queue   []protocol.RuntimeAgent // queued agents, oldest first
	running map[string]*runningAgent
	changed chan struct{}
}

// maxTransitionAttempts bounds reload-and-retry on version conflicts.
const maxTransitionAttempts = 10

// NewScheduler returns a scheduler whose agents run under contexts derived
// from base. Cancelling base stops all running agents and starts no more.
// onChange, when non-nil, is called after each persisted transition, outside
// the scheduler lock.
func NewScheduler(s AgentStore, r Runner, base context.Context, onChange func(protocol.RuntimeAgent)) *Scheduler {
	return &Scheduler{store: s, runner: r, base: base, onChange: onChange, tasks: map[string]*taskSched{}}
}

func isTerminal(st protocol.AgentState) bool {
	switch st {
	case protocol.AgentStateCompleted, protocol.AgentStateFailed, protocol.AgentStateCancelled, protocol.AgentStateInterrupted:
		return true
	}
	return false
}

// task returns the task's scheduling state; s.mu must be held.
func (s *Scheduler) task(taskID string) *taskSched {
	t, ok := s.tasks[taskID]
	if !ok {
		t = &taskSched{max: 1, running: map[string]*runningAgent{}, changed: make(chan struct{})}
		s.tasks[taskID] = t
	}
	return t
}

// Submit schedules an agent already stored as queued: it starts now if the
// task has a free slot and otherwise waits in the task's queue. maxConcurrent
// is recorded per task; the latest value wins.
func (s *Scheduler) Submit(ctx context.Context, a protocol.RuntimeAgent, maxConcurrent int) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.task(a.TaskID)
	t.max = maxConcurrent
	if _, ok := t.running[a.ID]; ok {
		return
	}
	for _, q := range t.queue {
		if q.ID == a.ID {
			s.promoteLocked(t)
			return
		}
	}
	t.queue = append(t.queue, a)
	s.promoteLocked(t)
}

// promoteLocked starts queued agents while the task has free slots; s.mu
// must be held. Agents removed from the queue by Cancel are never started.
func (s *Scheduler) promoteLocked(t *taskSched) {
	for len(t.running) < t.max && len(t.queue) > 0 && s.base.Err() == nil {
		a := t.queue[0]
		t.queue = t.queue[1:]
		ctx, cancel := context.WithCancel(s.base)
		t.running[a.ID] = &runningAgent{agent: a, cancel: cancel}
		s.wg.Add(1)
		go s.run(ctx, cancel, a)
	}
}

// run drives one promoted agent: queued -> running -> terminal, then frees
// its slot and promotes the next queued agent of the task.
func (s *Scheduler) run(ctx context.Context, cancel context.CancelFunc, a protocol.RuntimeAgent) {
	defer s.wg.Done()
	defer cancel()
	defer s.release(a)
	persist := context.WithoutCancel(s.base)

	cur, ok := s.transition(persist, a.TaskID, a.ID, func(cur *protocol.RuntimeAgent) bool {
		// Cancelled or already moved on elsewhere: do not start. A cancel
		// request persists `cancelled` itself; a stopped base leaves it queued.
		if cur.State != protocol.AgentStateQueued || ctx.Err() != nil {
			return false
		}
		cur.State = protocol.AgentStateRunning
		return true
	})
	if !ok {
		return
	}
	res, runErr := s.runner.Run(ctx, cur)

	s.transition(persist, a.TaskID, a.ID, func(cur *protocol.RuntimeAgent) bool {
		if isTerminal(cur.State) {
			return false // e.g. Cancel already persisted `cancelled`
		}
		switch {
		case s.cancelRequested(a.TaskID, a.ID):
			cur.State = protocol.AgentStateCancelled
			cur.Result = res // retain partial results
		case s.base.Err() != nil:
			cur.State = protocol.AgentStateInterrupted
			cur.Result = res
		case runErr != nil:
			cur.State = protocol.AgentStateFailed
			cur.Result = res
			cur.Result.Blocker = runErr.Error()
		default:
			cur.State = protocol.AgentStateCompleted
			cur.Result = res
		}
		return true
	})
}

func (s *Scheduler) cancelRequested(taskID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		if r, ok := t.running[id]; ok {
			return r.cancelRequested
		}
	}
	return false
}

func (s *Scheduler) release(a protocol.RuntimeAgent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.task(a.TaskID)
	delete(t.running, a.ID)
	s.promoteLocked(t)
}

// transition reloads the agent, lets decide mutate it (returning false to
// leave it alone) and persists it at the reloaded version, retrying on a
// concurrent update. It returns the persisted agent and whether a write
// happened.
func (s *Scheduler) transition(ctx context.Context, taskID, id string, decide func(*protocol.RuntimeAgent) bool) (protocol.RuntimeAgent, bool) {
	for attempt := 0; attempt < maxTransitionAttempts; attempt++ {
		cur, err := s.store.RuntimeAgent(ctx, taskID, id)
		if err != nil {
			return protocol.RuntimeAgent{}, false
		}
		version := cur.Version
		if !decide(&cur) {
			return cur, false
		}
		if err := s.store.UpdateRuntimeAgent(ctx, cur, version); err != nil {
			if ctx.Err() != nil {
				return protocol.RuntimeAgent{}, false
			}
			continue // reload: a concurrent writer may have moved it on
		}
		cur.Version = version + 1
		s.changed(cur)
		return cur, true
	}
	return protocol.RuntimeAgent{}, false
}

// changed signals a persisted transition: it closes and replaces the task's
// Changed channel, then calls onChange outside the lock.
func (s *Scheduler) changed(a protocol.RuntimeAgent) {
	s.mu.Lock()
	t := s.task(a.TaskID)
	close(t.changed)
	t.changed = make(chan struct{})
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange(a)
	}
}

// Cancel cancels an agent and all of its descendants (by ParentAgentID,
// transitively). Queued agents leave the queue and are persisted as
// `cancelled` without ever running; running agents have their context
// cancelled and are persisted as `cancelled`. Terminal agents are left as
// they are, though their non-terminal descendants are still cancelled.
func (s *Scheduler) Cancel(ctx context.Context, taskID, agentID string) error {
	if _, err := s.store.RuntimeAgent(ctx, taskID, agentID); err != nil {
		return fmt.Errorf("cancel agent %s: %w", agentID, err)
	}
	stored, err := s.store.RuntimeAgents(ctx, taskID)
	if err != nil {
		return fmt.Errorf("cancel agent %s: %w", agentID, err)
	}

	s.mu.Lock()
	t := s.task(taskID)
	children := map[string][]string{}
	addEdge := func(a protocol.RuntimeAgent) {
		if a.ParentAgentID != "" {
			children[a.ParentAgentID] = append(children[a.ParentAgentID], a.ID)
		}
	}
	for _, a := range stored {
		addEdge(a)
	}
	// Agents submitted after the store snapshot are known in memory.
	for _, a := range t.queue {
		addEdge(a)
	}
	for _, r := range t.running {
		addEdge(r.agent)
	}
	targets := []string{agentID}
	inSet := map[string]bool{agentID: true}
	for i := 0; i < len(targets); i++ {
		for _, c := range children[targets[i]] {
			if !inSet[c] {
				inSet[c] = true
				targets = append(targets, c)
			}
		}
	}
	kept := t.queue[:0:0]
	for _, a := range t.queue {
		if !inSet[a.ID] {
			kept = append(kept, a)
		}
	}
	t.queue = kept
	for id := range inSet {
		if r, ok := t.running[id]; ok {
			r.cancelRequested = true
			r.cancel()
		}
	}
	s.mu.Unlock()

	var firstErr error
	for _, id := range targets {
		_, _ = s.transition(ctx, taskID, id, func(cur *protocol.RuntimeAgent) bool {
			if isTerminal(cur.State) {
				return false
			}
			cur.State = protocol.AgentStateCancelled
			return true
		})
		if firstErr == nil {
			if cur, err := s.store.RuntimeAgent(ctx, taskID, id); err != nil {
				firstErr = err
			} else if !isTerminal(cur.State) {
				firstErr = fmt.Errorf("cancel agent %s: still %s", id, cur.State)
			}
		}
	}
	return firstErr
}

// Changed returns a channel closed on the next persisted state change of an
// agent in the task. It is never nil.
func (s *Scheduler) Changed(taskID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task(taskID).changed
}

// Active reports how many of the task's agents hold a running slot.
func (s *Scheduler) Active(taskID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		return len(t.running)
	}
	return 0
}

// Wait blocks until every agent goroutine has exited.
func (s *Scheduler) Wait() {
	s.wg.Wait()
}
