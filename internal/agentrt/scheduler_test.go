package agentrt

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

var _ AgentStore = (*store.Store)(nil)

func runtimeFixture(t *testing.T) (*store.Store, protocol.AgentTask) {
	t.Helper()
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	tr := protocol.Turn{ID: "root", ThreadID: th.ID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), tr); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "task", ThreadID: th.ID, RootTurnID: tr.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRevision: "base"}
	if err = s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	return s, task
}

type runOutcome struct {
	res protocol.AgentResult
	err error
}

// fakeRunner blocks each Run until released by agent name or until its
// context is cancelled.
type fakeRunner struct {
	mu      sync.Mutex
	release map[string]chan runOutcome
	calls   []string
	started chan string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{release: map[string]chan runOutcome{}, started: make(chan string, 64)}
}

func (f *fakeRunner) ch(name string) chan runOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.release[name]
	if !ok {
		c = make(chan runOutcome, 1)
		f.release[name] = c
	}
	return c
}

func (f *fakeRunner) Run(ctx context.Context, a protocol.RuntimeAgent) (protocol.AgentResult, error) {
	c := f.ch(a.Name)
	f.mu.Lock()
	f.calls = append(f.calls, a.Name)
	f.mu.Unlock()
	f.started <- a.Name
	select {
	case o := <-c:
		return o.res, o.err
	case <-ctx.Done():
		return protocol.AgentResult{Summary: "partial"}, ctx.Err()
	}
}

func (f *fakeRunner) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeRunner) finish(name string, res protocol.AgentResult, err error) {
	f.ch(name) <- runOutcome{res, err}
}

func waitStarted(t *testing.T, f *fakeRunner, want ...string) {
	t.Helper()
	got := map[string]bool{}
	deadline := time.After(10 * time.Second)
	for len(got) < len(want) {
		select {
		case n := <-f.started:
			got[n] = true
		case <-deadline:
			t.Fatalf("timed out waiting for %v; started %v", want, got)
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("expected %q started; got %v", w, got)
		}
	}
}

func createAgent(t *testing.T, s *store.Store, task protocol.AgentTask, name, parent string) protocol.RuntimeAgent {
	t.Helper()
	a, err := s.CreateRuntimeAgent(t.Context(), protocol.RuntimeAgent{TaskID: task.ID, Name: name, Purpose: "p", ParentAgentID: parent, Assignment: protocol.AgentAssignment{Prompt: "do it"}}, "call-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func stateOf(t *testing.T, s *store.Store, a protocol.RuntimeAgent) protocol.RuntimeAgent {
	t.Helper()
	got, err := s.RuntimeAgent(context.Background(), a.TaskID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func newTestScheduler(t *testing.T, s *store.Store, r Runner, onChange func(protocol.RuntimeAgent)) *Scheduler {
	t.Helper()
	base, cancel := context.WithCancel(context.Background())
	sch := NewScheduler(s, r, base, onChange)
	t.Cleanup(func() { cancel(); sch.Wait() })
	return sch
}

func TestSchedulerQueuesBeyondMaxConcurrent(t *testing.T) {
	s, task := runtimeFixture(t)
	r := newFakeRunner()
	sch := newTestScheduler(t, s, r, nil)
	a1, a2, a3 := createAgent(t, s, task, "a1", ""), createAgent(t, s, task, "a2", ""), createAgent(t, s, task, "a3", "")
	for _, a := range []protocol.RuntimeAgent{a1, a2, a3} {
		sch.Submit(t.Context(), a, 2)
	}
	waitStarted(t, r, "a1", "a2")
	if st := stateOf(t, s, a1).State; st != protocol.AgentStateRunning {
		t.Fatalf("a1 state %s", st)
	}
	if st := stateOf(t, s, a2).State; st != protocol.AgentStateRunning {
		t.Fatalf("a2 state %s", st)
	}
	if st := stateOf(t, s, a3).State; st != protocol.AgentStateQueued {
		t.Fatalf("a3 state %s", st)
	}
	if n := sch.Active(task.ID); n != 2 {
		t.Fatalf("active=%d", n)
	}
	r.finish("a1", protocol.AgentResult{Summary: "done a1"}, nil)
	waitStarted(t, r, "a3")
	if st := stateOf(t, s, a3).State; st != protocol.AgentStateRunning {
		t.Fatalf("a3 state %s", st)
	}
	got := stateOf(t, s, a1)
	if got.State != protocol.AgentStateCompleted || got.Result.Summary != "done a1" {
		t.Fatalf("a1 = %s %+v", got.State, got.Result)
	}
	r.finish("a2", protocol.AgentResult{}, nil)
	r.finish("a3", protocol.AgentResult{}, nil)
	sch.Wait()
	if n := sch.Active(task.ID); n != 0 {
		t.Fatalf("active=%d", n)
	}
}

func TestSchedulerPersistsFailure(t *testing.T) {
	s, task := runtimeFixture(t)
	r := newFakeRunner()
	var mu sync.Mutex
	var seen []protocol.AgentState
	sch := newTestScheduler(t, s, r, func(a protocol.RuntimeAgent) {
		mu.Lock()
		seen = append(seen, a.State)
		mu.Unlock()
	})
	a := createAgent(t, s, task, "a", "")
	sch.Submit(t.Context(), a, 1)
	waitStarted(t, r, "a")
	r.finish("a", protocol.AgentResult{}, errors.New("model exploded"))
	sch.Wait()
	got := stateOf(t, s, a)
	if got.State != protocol.AgentStateFailed || !strings.Contains(got.Result.Blocker, "model exploded") {
		t.Fatalf("got %s %+v", got.State, got.Result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != protocol.AgentStateRunning || seen[1] != protocol.AgentStateFailed {
		t.Fatalf("onChange saw %v", seen)
	}
}

func TestSchedulerCancelCascadesToDescendants(t *testing.T) {
	s, task := runtimeFixture(t)
	r := newFakeRunner()
	sch := newTestScheduler(t, s, r, nil)
	parent := createAgent(t, s, task, "parent", "")
	child := createAgent(t, s, task, "child", parent.ID)
	grand := createAgent(t, s, task, "grand", child.ID)
	other := createAgent(t, s, task, "other", "")
	sch.Submit(t.Context(), parent, 2)
	sch.Submit(t.Context(), grand, 2)
	waitStarted(t, r, "parent", "grand")
	sch.Submit(t.Context(), child, 2)
	sch.Submit(t.Context(), other, 2)
	if st := stateOf(t, s, child).State; st != protocol.AgentStateQueued {
		t.Fatalf("child state %s", st)
	}
	if err := sch.Cancel(t.Context(), task.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []protocol.RuntimeAgent{parent, child, grand} {
		if st := stateOf(t, s, a).State; st != protocol.AgentStateCancelled {
			t.Fatalf("%s state %s after cancel", a.Name, st)
		}
	}
	// The freed slots promote the unrelated queued agent, never the cancelled child.
	waitStarted(t, r, "other")
	r.finish("other", protocol.AgentResult{}, nil)
	sch.Wait()
	for _, a := range []protocol.RuntimeAgent{parent, child, grand} {
		if st := stateOf(t, s, a).State; st != protocol.AgentStateCancelled {
			t.Fatalf("%s state %s after quiescence", a.Name, st)
		}
	}
	if st := stateOf(t, s, other).State; st != protocol.AgentStateCompleted {
		t.Fatalf("other state %s", st)
	}
	calls := r.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls %v", calls)
	}
	for _, c := range calls {
		if c == "child" {
			t.Fatalf("cancelled child ran: %v", calls)
		}
	}
	// Cancelling a terminal agent is a no-op.
	if err := sch.Cancel(t.Context(), task.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if st := stateOf(t, s, other).State; st != protocol.AgentStateCompleted {
		t.Fatalf("other state %s after no-op cancel", st)
	}
}

func TestSchedulerCancelledParentQueuedChildNeverRuns(t *testing.T) {
	s, task := runtimeFixture(t)
	r := newFakeRunner()
	sch := newTestScheduler(t, s, r, nil)
	parent := createAgent(t, s, task, "parent", "")
	child := createAgent(t, s, task, "child", parent.ID)
	sch.Submit(t.Context(), parent, 1)
	sch.Submit(t.Context(), child, 1)
	waitStarted(t, r, "parent")
	if err := sch.Cancel(t.Context(), task.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	sch.Wait()
	if calls := r.Calls(); len(calls) != 1 || calls[0] != "parent" {
		t.Fatalf("calls %v", calls)
	}
	if st := stateOf(t, s, child).State; st != protocol.AgentStateCancelled {
		t.Fatalf("child state %s", st)
	}
	if n := sch.Active(task.ID); n != 0 {
		t.Fatalf("active=%d", n)
	}
}

func TestSchedulerChangedChannelFiresOnTransition(t *testing.T) {
	s, task := runtimeFixture(t)
	r := newFakeRunner()
	sch := newTestScheduler(t, s, r, nil)
	ch := sch.Changed(task.ID)
	if ch == nil {
		t.Fatal("nil channel")
	}
	select {
	case <-ch:
		t.Fatal("channel closed before any change")
	default:
	}
	a := createAgent(t, s, task, "a", "")
	sch.Submit(t.Context(), a, 1)
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("channel not closed on transition")
	}
	waitStarted(t, r, "a")
	next := sch.Changed(task.ID)
	select {
	case <-next:
		t.Fatal("replacement channel already closed")
	default:
	}
	r.finish("a", protocol.AgentResult{}, nil)
	select {
	case <-next:
	case <-time.After(10 * time.Second):
		t.Fatal("replacement channel not closed on completion")
	}
	sch.Wait()
}

func TestSchedulerCancelRacesRunnerFinish(t *testing.T) {
	s, task := runtimeFixture(t)
	r := newFakeRunner()
	var mu sync.Mutex
	history := map[string][]protocol.AgentState{}
	sch := newTestScheduler(t, s, r, func(a protocol.RuntimeAgent) {
		mu.Lock()
		history[a.ID] = append(history[a.ID], a.State)
		mu.Unlock()
	})
	const n = 15
	for i := 0; i < n; i++ {
		name := "race" + string(rune('a'+i))
		a := createAgent(t, s, task, name, "")
		sch.Submit(t.Context(), a, 1)
		waitStarted(t, r, name)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); r.finish(name, protocol.AgentResult{Summary: "ok"}, nil) }()
		var cerr error
		go func() { defer wg.Done(); cerr = sch.Cancel(t.Context(), task.ID, a.ID) }()
		wg.Wait()
		if cerr != nil {
			t.Fatal(cerr)
		}
		sch.Wait()
		final := stateOf(t, s, a).State
		if final != protocol.AgentStateCompleted && final != protocol.AgentStateCancelled {
			t.Fatalf("%s final state %s", name, final)
		}
		mu.Lock()
		h := history[a.ID]
		mu.Unlock()
		if len(h) != 2 || h[0] != protocol.AgentStateRunning || h[1] != final {
			t.Fatalf("%s history %v final %s", name, h, final)
		}
	}
}

func TestSchedulerBaseContextStopsEverything(t *testing.T) {
	s, task := runtimeFixture(t)
	r := newFakeRunner()
	base, cancel := context.WithCancel(context.Background())
	sch := NewScheduler(s, r, base, nil)
	a, b := createAgent(t, s, task, "a", ""), createAgent(t, s, task, "b", "")
	sch.Submit(t.Context(), a, 1)
	sch.Submit(t.Context(), b, 1)
	waitStarted(t, r, "a")
	cancel()
	done := make(chan struct{})
	go func() { sch.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scheduler goroutines did not stop")
	}
	if st := stateOf(t, s, a).State; st != protocol.AgentStateInterrupted {
		t.Fatalf("a state %s", st)
	}
	if st := stateOf(t, s, b).State; st != protocol.AgentStateQueued {
		t.Fatalf("b state %s", st)
	}
	if calls := r.Calls(); len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
}
