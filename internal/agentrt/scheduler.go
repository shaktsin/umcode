package agentrt

import (
	"context"
	"fmt"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Runner executes one runtime agent until it finishes or ctx is cancelled.
// A Runner must honour ctx: cancellation is delivered only through it, and a
// runner that ignores ctx keeps its concurrency slot until it returns.
type Runner interface {
	Run(ctx context.Context, a protocol.RuntimeAgent) (protocol.AgentResult, error)
}

// Scheduler promotes queued runtime agents to running per task, up to the
// task's maxConcurrent, in submission order. The store is the source of
// truth for agent state: every transition reloads the agent and is applied
// under optimistic versioning, and terminal states are never overwritten.
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
	// cancelled remembers every agent a Cancel targeted (terminal ones
	// included) so later-submitted descendants are cancelled, never run.
	cancelled map[string]bool
	changed   chan struct{}
}

const (
	// maxTransitionAttempts bounds reload-and-retry on version conflicts.
	maxTransitionAttempts = 10
	// maxAncestorWalk bounds the ParentAgentID walk in Submit.
	maxAncestorWalk = 64
)

// NewScheduler returns a scheduler whose agents run under contexts derived
// from base. base must live as long as the process: cancelling it stops all
// running agents (persisted as `interrupted` unless they completed) and
// starts no more; queued agents stay `queued` in the store and are
// reconciled to `interrupted` on the next start (ReconcileRuntimeAgents).
//
// onChange, when non-nil, is called after each persisted transition, outside
// the scheduler lock, from whichever goroutine made the write. Events carry
// the persisted Version and may arrive out of order across goroutines;
// consumers should drop events older than the latest Version they have seen
// for that agent.
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
		t = &taskSched{max: 1, running: map[string]*runningAgent{}, cancelled: map[string]bool{}, changed: make(chan struct{})}
		s.tasks[taskID] = t
	}
	return t
}

func cancelToState(cur *protocol.RuntimeAgent) bool {
	if isTerminal(cur.State) {
		return false
	}
	cur.State = protocol.AgentStateCancelled
	return true
}

// Submit schedules an agent already stored as queued: it starts now if the
// task has a free slot and otherwise waits in the task's queue. maxConcurrent
// is recorded per task; the latest value wins. An agent that is, or descends
// from, a cancelled agent is persisted as `cancelled` and never run.
func (s *Scheduler) Submit(ctx context.Context, a protocol.RuntimeAgent, maxConcurrent int) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	// Collect the ancestor chain from the store outside the lock; the
	// cancelled-set check below is atomic with Cancel's queue sweep.
	lineage := []string{a.ID}
	seen := map[string]bool{a.ID: true}
	for parent := a.ParentAgentID; parent != "" && !seen[parent] && len(lineage) < maxAncestorWalk; {
		seen[parent] = true
		lineage = append(lineage, parent)
		p, err := s.store.RuntimeAgent(ctx, a.TaskID, parent)
		if err != nil {
			break
		}
		parent = p.ParentAgentID
	}

	s.mu.Lock()
	t := s.task(a.TaskID)
	t.max = maxConcurrent
	cancelled := false
	for _, id := range lineage {
		if t.cancelled[id] {
			cancelled = true
			break
		}
	}
	if cancelled {
		t.cancelled[a.ID] = true
		s.mu.Unlock()
		s.transition(context.WithoutCancel(ctx), a.TaskID, a.ID, cancelToState, nil)
		return
	}
	defer s.mu.Unlock()
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
		ra := &runningAgent{agent: a, cancel: cancel}
		t.running[a.ID] = ra
		s.wg.Add(1)
		go s.run(ctx, ra)
	}
}

// releaseLocked frees ra's slot, if it still holds it, and promotes the next
// queued agent; s.mu must be held. The identity check keeps a stale runner
// from freeing the slot of a re-admitted agent with the same ID.
func (s *Scheduler) releaseLocked(t *taskSched, ra *runningAgent) {
	if t.running[ra.agent.ID] == ra {
		delete(t.running, ra.agent.ID)
	}
	s.promoteLocked(t)
}

// run drives one promoted agent: queued -> running -> terminal, then frees
// its slot and promotes the next queued agent of the task.
func (s *Scheduler) run(ctx context.Context, ra *runningAgent) {
	a := ra.agent
	defer s.wg.Done()
	defer ra.cancel()
	defer func() { // no-op if the terminal write already released the slot
		s.mu.Lock()
		s.releaseLocked(s.task(a.TaskID), ra)
		s.mu.Unlock()
	}()
	persist := context.WithoutCancel(s.base)
	release := func(t *taskSched) { s.releaseLocked(t, ra) }

	cur, ok := s.transition(persist, a.TaskID, a.ID, func(cur *protocol.RuntimeAgent) bool {
		// Moved on elsewhere, cancel requested (Cancel persists `cancelled`
		// itself) or base stopped (leave it queued): do not start.
		if cur.State != protocol.AgentStateQueued || ctx.Err() != nil {
			return false
		}
		if s.isCancelled(a.TaskID, a.ID) {
			cur.State = protocol.AgentStateCancelled
			return true
		}
		cur.State = protocol.AgentStateRunning
		return true
	}, func(t *taskSched) {
		if t.cancelled[a.ID] {
			s.releaseLocked(t, ra)
		}
	})
	if !ok || cur.State != protocol.AgentStateRunning {
		return
	}
	var res protocol.AgentResult
	runErr := ctx.Err()
	if runErr == nil {
		res, runErr = s.runner.Run(ctx, cur)
	}

	s.transition(persist, a.TaskID, a.ID, func(cur *protocol.RuntimeAgent) bool {
		if isTerminal(cur.State) {
			return false // e.g. Cancel already persisted `cancelled`
		}
		cur.Result = res // a cancelled or interrupted agent keeps partial results
		switch {
		case s.cancelRequested(a.TaskID, ra):
			cur.State = protocol.AgentStateCancelled
		case runErr == nil:
			cur.State = protocol.AgentStateCompleted
		case s.base.Err() != nil:
			cur.State = protocol.AgentStateInterrupted
		default:
			cur.State = protocol.AgentStateFailed
			cur.Result.Blocker = runErr.Error()
		}
		return true
	}, release)
}

func (s *Scheduler) isCancelled(taskID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task(taskID).cancelled[id]
}

func (s *Scheduler) cancelRequested(taskID string, ra *runningAgent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ra.cancelRequested
}

// transition reloads the agent, lets decide mutate it (returning false to
// leave it alone) and persists it at the reloaded version. It retries only
// when the version moved underneath it (a concurrent writer); any other
// failure, such as a rejected transition, ends it. On success it runs
// onPersist under the lock before signalling the change, so slot changes are
// visible to anyone woken by Changed or onChange. It returns the persisted
// agent and whether a write happened.
func (s *Scheduler) transition(ctx context.Context, taskID, id string, decide func(*protocol.RuntimeAgent) bool, onPersist func(*taskSched)) (protocol.RuntimeAgent, bool) {
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
			now, rerr := s.store.RuntimeAgent(ctx, taskID, id)
			if rerr != nil || now.Version == version {
				return protocol.RuntimeAgent{}, false // not a version conflict
			}
			continue
		}
		cur.Version = version + 1
		s.changed(cur, onPersist)
		return cur, true
	}
	return protocol.RuntimeAgent{}, false
}

// changed signals a persisted transition: under the lock it applies
// onPersist and closes and replaces the task's Changed channel, then calls
// onChange outside the lock.
func (s *Scheduler) changed(a protocol.RuntimeAgent, onPersist func(*taskSched)) {
	s.mu.Lock()
	t := s.task(a.TaskID)
	if onPersist != nil {
		onPersist(t)
	}
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
// they are, though their non-terminal descendants are still cancelled. All
// targets are remembered, so descendants submitted later are cancelled too.
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
	for id := range inSet {
		t.cancelled[id] = true
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

	// A dying caller ctx must not leave a queue-removed agent queued in the store.
	persist := context.WithoutCancel(ctx)
	var firstErr error
	for _, id := range targets {
		s.transition(persist, taskID, id, cancelToState, nil)
		if firstErr == nil {
			if cur, err := s.store.RuntimeAgent(persist, taskID, id); err != nil {
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
