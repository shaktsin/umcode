package agentrt

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

var _ Service = (*Runtime)(nil)

// savePlan stores a plan for the fixture task, approving it unless approve is false.
func savePlan(t *testing.T, s *store.Store, task protocol.AgentTask, limits protocol.PlanLimits, approve bool) protocol.AgentPlan {
	t.Helper()
	p := protocol.AgentPlan{
		ID: "plan-1", ThreadID: task.ThreadID, TaskID: task.ID, Limits: limits, State: protocol.PlanProposed,
		Agents: []protocol.PlanAgent{{
			Name: "worker", Purpose: "work", Model: "m1",
			Capabilities: protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, SpawnMax: 2},
		}},
	}
	if err := s.SaveAgentPlan(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if approve {
		if err := s.ApproveAgentPlan(t.Context(), p.ID, task.ID); err != nil {
			t.Fatal(err)
		}
		p.State = protocol.PlanApproved
	}
	return p
}

func newTestRuntime(t *testing.T, s *store.Store, r Runner, opts ...Option) *Runtime {
	t.Helper()
	base, cancel := context.WithCancel(context.Background())
	rt := NewRuntime(s, r, base, opts...)
	t.Cleanup(func() { cancel(); rt.sched.Wait() })
	return rt
}

func spawnReq(task protocol.AgentTask, call, name string, share float64) SpawnRequest {
	return SpawnRequest{TaskID: task.ID, CallID: call, Name: name, PlanEntry: "worker", BudgetShare: share, Assignment: protocol.AgentAssignment{Prompt: "do it"}}
}

func waitRuntimeState(t *testing.T, rt *Runtime, taskID, id string, want protocol.AgentState) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		ch := rt.sched.Changed(taskID)
		a, err := rt.Status(context.Background(), taskID, "", id)
		if err != nil {
			t.Fatal(err)
		}
		if a.State == want {
			return
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("%s never reached %s (now %s)", a.Name, want, a.State)
		}
	}
}

func admissionField(t *testing.T, err error) string {
	t.Helper()
	var ae *AdmissionError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AdmissionError, got %v", err)
	}
	return ae.Field
}

func TestSpawnIsIdempotentByCallID(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)

	var wg sync.WaitGroup
	results := make([]SpawnResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = rt.Spawn(context.Background(), spawnReq(task, "call-1", "a", 0.25))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].Agent.ID == "" || results[0].Agent.ID != results[1].Agent.ID {
		t.Fatalf("ids differ: %q vs %q", results[0].Agent.ID, results[1].Agent.ID)
	}
	all, err := rt.List(t.Context(), task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 agent, got %d", len(all))
	}
	waitStarted(t, r, "a")
	select {
	case n := <-r.started:
		t.Fatalf("runner started twice (%s)", n)
	case <-time.After(300 * time.Millisecond):
	}
	if calls := r.Calls(); len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
}

func TestSpawnRefusesUnapprovedPlan(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), false)
	rt := newTestRuntime(t, s, newFakeRunner())
	_, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if f := admissionField(t, err); f != "plan" {
		t.Fatalf("field %q", f)
	}
	all, err := rt.List(t.Context(), task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("want no agents, got %d", len(all))
	}
}

func TestSpawnRefusesSupersededPlan(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	// A newer plan approved for another task of the thread supersedes plan-1.
	if err := s.CreateTurn(t.Context(), protocol.Turn{ID: "root2", ThreadID: task.ThreadID, Status: protocol.TurnCompleted, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO agent_tasks (id,thread_id,root_turn_id,activation,state,base_revision,base_root,project_id,created_at,updated_at,parent_root) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		"task2", task.ThreadID, "root2", protocol.AgentActivationExplicitUser, protocol.AgentStateCompleted, "base", "", "", store.Now(), store.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgentPlan(t.Context(), protocol.AgentPlan{ID: "plan-2", ThreadID: task.ThreadID, Agents: []protocol.PlanAgent{{Name: "worker"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveAgentPlan(t.Context(), "plan-2", "task2"); err != nil {
		t.Fatal(err)
	}
	rt := newTestRuntime(t, s, newFakeRunner())
	_, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if f := admissionField(t, err); f != "plan" {
		t.Fatalf("field %q", f)
	}
}

func TestSpawnReportsPartialGrant(t *testing.T) {
	s, task := runtimeFixture(t)
	lim := protocol.DefaultPlanLimits()
	lim.Network = false
	savePlan(t, s, task, lim, true)
	rt := newTestRuntime(t, s, newFakeRunner())
	req := spawnReq(task, "call-1", "a", 0.25)
	req.Capabilities = &protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, Network: true}
	res, err := rt.Spawn(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rejected) == 0 {
		t.Fatal("expected rejections")
	}
	if res.Granted.Network || res.Agent.Capabilities.Network {
		t.Fatalf("network granted: %+v", res.Granted)
	}
	if res.Agent.ID == "" {
		t.Fatal("agent not created")
	}
	if _, err := rt.Status(t.Context(), task.ID, "", res.Agent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFifthQuarterShareRefused(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	rt := newTestRuntime(t, s, newFakeRunner())
	var ids []string
	for i, name := range []string{"a", "b", "c", "d"} {
		res, err := rt.Spawn(t.Context(), spawnReq(task, "call-"+name, name, 0.25))
		if err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
		ids = append(ids, res.Agent.ID)
	}
	_, err := rt.Spawn(t.Context(), spawnReq(task, "call-e", "e", 0.25))
	if f := admissionField(t, err); f != "budget.share" {
		t.Fatalf("field %q", f)
	}
	if err := rt.Cancel(t.Context(), task.ID, "", ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Spawn(t.Context(), spawnReq(task, "call-e", "e", 0.25)); err != nil {
		t.Fatalf("fifth after cancel: %v", err)
	}
}

func TestSpawnQueuesAtCapacityWithReason(t *testing.T) {
	s, task := runtimeFixture(t)
	lim := protocol.DefaultPlanLimits()
	lim.MaxConcurrent = 1
	savePlan(t, s, task, lim, true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	if _, err := rt.Spawn(t.Context(), spawnReq(task, "call-a", "a", 0.25)); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "a")
	res, err := rt.Spawn(t.Context(), spawnReq(task, "call-b", "b", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	if res.Agent.State != protocol.AgentStateQueued {
		t.Fatalf("state %s", res.Agent.State)
	}
	if !strings.HasPrefix(res.QueuedReason, "capacity:") {
		t.Fatalf("reason %q", res.QueuedReason)
	}
	if res.QueuedReason != "capacity: 1/1 running" {
		t.Fatalf("reason %q", res.QueuedReason)
	}
}

func TestCancelParentWhileChildQueuedReturnsShare(t *testing.T) {
	s, task := runtimeFixture(t)
	lim := protocol.DefaultPlanLimits()
	lim.MaxConcurrent = 1
	savePlan(t, s, task, lim, true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	parent, err := rt.Spawn(t.Context(), spawnReq(task, "call-p", "parent", 0.5))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "parent")
	creq := spawnReq(task, "call-c", "child", 0.5)
	creq.CallerAgentID = parent.Agent.ID
	child, err := rt.Spawn(t.Context(), creq)
	if err != nil {
		t.Fatal(err)
	}
	if child.Agent.State != protocol.AgentStateQueued || child.Agent.Depth != 2 || child.Agent.ParentAgentID != parent.Agent.ID {
		t.Fatalf("child %+v", child.Agent)
	}
	// The lead has 0.5 free; a full share must be refused until the parent goes.
	if _, err := rt.Spawn(t.Context(), spawnReq(task, "call-n", "next", 1.0)); admissionField(t, err) != "budget.share" {
		t.Fatalf("expected share refusal, got %v", err)
	}
	if err := rt.Cancel(t.Context(), task.ID, "", parent.Agent.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.Agent.ID, child.Agent.ID} {
		waitRuntimeState(t, rt, task.ID, id, protocol.AgentStateCancelled)
	}
	if _, err := rt.Spawn(t.Context(), spawnReq(task, "call-n", "next", 1.0)); err != nil {
		t.Fatalf("spawn at freed share: %v", err)
	}
	waitStarted(t, r, "next")
	for _, c := range r.Calls() {
		if c == "child" {
			t.Fatalf("child ran: %v", r.Calls())
		}
	}
}

func TestWaitAnyAndAll(t *testing.T) {
	s, task := runtimeFixture(t)
	lim := protocol.DefaultPlanLimits()
	lim.MaxConcurrent = 2
	savePlan(t, s, task, lim, true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	a, err := rt.Spawn(t.Context(), spawnReq(task, "call-a", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	b, err := rt.Spawn(t.Context(), spawnReq(task, "call-b", "b", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "a", "b")
	ids := []string{a.Agent.ID, b.Agent.ID}

	if _, err := rt.Wait(t.Context(), task.ID, "", nil, WaitAny); err == nil {
		t.Fatal("empty ids accepted")
	}
	if _, err := rt.Wait(t.Context(), task.ID, "", []string{"nope"}, WaitAny); err == nil {
		t.Fatal("unknown id accepted")
	}
	short, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	if _, err := rt.Wait(short, task.ID, "", ids, WaitAny); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	cancel()

	type out struct {
		agents []protocol.RuntimeAgent
		err    error
	}
	anyDone := make(chan out, 1)
	allDone := make(chan out, 1)
	go func() { ag, err := rt.Wait(context.Background(), task.ID, "", ids, WaitAny); anyDone <- out{ag, err} }()
	go func() { ag, err := rt.Wait(context.Background(), task.ID, "", ids, WaitAll); allDone <- out{ag, err} }()
	select {
	case o := <-anyDone:
		t.Fatalf("wait(any) returned early: %+v", o)
	case <-time.After(200 * time.Millisecond):
	}
	r.finish("a", protocol.AgentResult{Summary: "ok"}, nil)
	select {
	case o := <-anyDone:
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.agents[0].State != protocol.AgentStateCompleted {
			t.Fatalf("states %s %s", o.agents[0].State, o.agents[1].State)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait(any) never returned")
	}
	select {
	case o := <-allDone:
		t.Fatalf("wait(all) returned before both: %+v", o)
	case <-time.After(300 * time.Millisecond):
	}
	r.finish("b", protocol.AgentResult{Summary: "ok"}, nil)
	select {
	case o := <-allDone:
		if o.err != nil {
			t.Fatal(o.err)
		}
		for _, ag := range o.agents {
			if ag.State != protocol.AgentStateCompleted {
				t.Fatalf("%s is %s", ag.Name, ag.State)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait(all) never returned")
	}
}

func TestMessageDeliversToMailbox(t *testing.T) {
	s, task := runtimeFixture(t)
	lim := protocol.DefaultPlanLimits()
	lim.MaxConcurrent = 3
	savePlan(t, s, task, lim, true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	var agents []protocol.RuntimeAgent
	for _, name := range []string{"a", "b", "c"} {
		res, err := rt.Spawn(t.Context(), spawnReq(task, "call-"+name, name, 0.25))
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, res.Agent)
	}
	a, b, c := agents[0], agents[1], agents[2]
	m, err := rt.Message(t.Context(), task.ID, "", b.ID, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingAgentMessages(t.Context(), task.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != m.ID || pending[0].Body != "hello" || pending[0].FromAgentID != "" {
		t.Fatalf("pending %+v", pending)
	}
	if _, err := rt.Message(t.Context(), task.ID, "", a.ID, "from lead", ""); err != nil {
		t.Fatalf("lead message: %v", err)
	}
	if _, err := rt.Message(t.Context(), task.ID, a.ID, "nope", "x", ""); err == nil {
		t.Fatal("unknown target accepted")
	}
	waitStarted(t, r, "a", "b", "c")
	r.finish("c", protocol.AgentResult{Summary: "done"}, nil)
	waitRuntimeState(t, rt, task.ID, c.ID, protocol.AgentStateCompleted)
	if _, err := rt.Message(t.Context(), task.ID, "", c.ID, "too late", ""); err == nil {
		t.Fatal("message to completed agent accepted")
	}
}

func TestRecoverInterruptsActiveAgents(t *testing.T) {
	s, task := runtimeFixture(t)
	running := createAgent(t, s, task, "running", "")
	queued := createAgent(t, s, task, "queued", "")
	running.State = protocol.AgentStateRunning
	if err := s.UpdateRuntimeAgent(t.Context(), running, running.Version); err != nil {
		t.Fatal(err)
	}
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	if err := rt.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range []protocol.RuntimeAgent{running, queued} {
		if got := stateOf(t, s, a); got.State != protocol.AgentStateInterrupted {
			t.Fatalf("%s is %s", a.Name, got.State)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if calls := r.Calls(); len(calls) != 0 {
		t.Fatalf("runner invoked: %v", calls)
	}
}

// failingCallStore injects an error from RuntimeAgentForCall and records
// whether admission-time lookups happened.
type failingCallStore struct {
	AgentStore
	callErr      error
	planLookedUp bool
}

func (f *failingCallStore) RuntimeAgentForCall(ctx context.Context, taskID, callID string) (protocol.RuntimeAgent, error) {
	return protocol.RuntimeAgent{}, f.callErr
}

func (f *failingCallStore) ApprovedAgentPlanForTask(ctx context.Context, taskID string) (protocol.AgentPlan, error) {
	f.planLookedUp = true
	return f.AgentStore.ApprovedAgentPlanForTask(ctx, taskID)
}

func TestSpawnReturnsStoreErrorFromCallLookup(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	boom := errors.New("disk on fire")
	fs := &failingCallStore{AgentStore: s, callErr: boom}
	base, cancel := context.WithCancel(context.Background())
	rt := NewRuntime(fs, newFakeRunner(), base)
	t.Cleanup(func() { cancel(); rt.sched.Wait() })
	_, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped store error, got %v", err)
	}
	var ae *AdmissionError
	if errors.As(err, &ae) {
		t.Fatalf("store error reported as admission refusal: %v", err)
	}
	if fs.planLookedUp {
		t.Fatal("fell through to admission")
	}
	all, err := s.RuntimeAgents(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("agent stored: %d", len(all))
	}
}

func TestSpawnRefusesUnknownAndTerminalCaller(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	done, err := rt.Spawn(t.Context(), spawnReq(task, "call-d", "done", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "done")
	r.finish("done", protocol.AgentResult{Summary: "ok"}, nil)
	waitRuntimeState(t, rt, task.ID, done.Agent.ID, protocol.AgentStateCompleted)

	for _, caller := range []string{"no-such-agent", done.Agent.ID} {
		req := spawnReq(task, "call-x-"+caller, "x", 0.1)
		req.CallerAgentID = caller
		_, err := rt.Spawn(t.Context(), req)
		if f := admissionField(t, err); f != "caller" {
			t.Fatalf("caller %s: field %q", caller, f)
		}
	}
	all, err := rt.List(t.Context(), task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("want only the completed agent, got %d", len(all))
	}
}

func TestSpawnReplayReturnsSameGrant(t *testing.T) {
	s, task := runtimeFixture(t)
	lim := protocol.DefaultPlanLimits()
	lim.Network = false
	savePlan(t, s, task, lim, true)
	rt := newTestRuntime(t, s, newFakeRunner())
	req := spawnReq(task, "call-1", "a", 0.25)
	req.Capabilities = &protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, Network: true}
	first, err := rt.Spawn(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := rt.Spawn(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Agent.ID != first.Agent.ID || again.Granted != first.Granted {
		t.Fatalf("replay differs: %+v vs %+v", again.Granted, first.Granted)
	}
	if len(first.Rejected) == 0 || !slices.Equal(again.Rejected, first.Rejected) {
		t.Fatalf("replay rejected %v, first %v", again.Rejected, first.Rejected)
	}
	stored, err := s.RuntimeAgent(t.Context(), task.ID, first.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PlanEntry != "worker" || !slices.Equal(stored.Rejected, first.Rejected) {
		t.Fatalf("stored plan entry %q rejected %v", stored.PlanEntry, stored.Rejected)
	}
}

func TestSpawnReplayWithDifferentRequestIsRefused(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	first, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "a")
	otherAssignment := spawnReq(task, "call-1", "a", 0.25)
	otherAssignment.Assignment.Prompt = "something else"
	otherEntry := spawnReq(task, "call-1", "a", 0.25)
	otherEntry.PlanEntry = "reviewer"
	otherCaller := spawnReq(task, "call-1", "a", 0.25)
	otherCaller.CallerAgentID = first.Agent.ID
	for name, req := range map[string]SpawnRequest{"assignment": otherAssignment, "planEntry": otherEntry, "caller": otherCaller} {
		if _, err := rt.Spawn(t.Context(), req); !errors.Is(err, ErrSpawnReplayMismatch) {
			t.Fatalf("%s: want ErrSpawnReplayMismatch, got %v", name, err)
		}
	}
	// An identical retry still replays.
	again, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if err != nil || again.Agent.ID != first.Agent.ID {
		t.Fatalf("identical replay: %+v %v", again.Agent, err)
	}
	if all, _ := rt.List(t.Context(), task.ID, ""); len(all) != 1 {
		t.Fatalf("agents = %d", len(all))
	}
}

func TestMessageIsIdempotentByCallID(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	a, err := rt.Spawn(t.Context(), spawnReq(task, "call-a", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	first, err := rt.Message(t.Context(), task.ID, "", a.Agent.ID, "hello", "msg-call-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := rt.Message(t.Context(), task.ID, "", a.Agent.ID, "hello", "msg-call-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || again.ID != first.ID {
		t.Fatalf("retry queued a new message: %q vs %q", first.ID, again.ID)
	}
	pending, err := s.PendingAgentMessages(t.Context(), task.ID, a.Agent.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestSpawnQueuedReasonOnlyWhenQueued(t *testing.T) {
	s, task := runtimeFixture(t)
	lim := protocol.DefaultPlanLimits()
	lim.MaxConcurrent = 1
	savePlan(t, s, task, lim, true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	first, err := rt.Spawn(t.Context(), spawnReq(task, "call-a", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	if first.QueuedReason != "" {
		t.Fatalf("first spawn holds a slot but reason %q", first.QueuedReason)
	}
	second, err := rt.Spawn(t.Context(), spawnReq(task, "call-b", "b", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(second.QueuedReason, "capacity:") {
		t.Fatalf("second reason %q", second.QueuedReason)
	}
}

func TestCallerScopedTargeting(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	spawn := func(call, name, caller string) string {
		t.Helper()
		req := spawnReq(task, call, name, 0.2)
		req.CallerAgentID = caller
		res, err := rt.Spawn(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		return res.Agent.ID
	}
	a := spawn("call-a", "a", "")
	b := spawn("call-b", "b", "")
	waitStarted(t, r, "a", "b")
	a1 := spawn("call-a1", "a1", a)
	waitStarted(t, r, "a1")
	ctx := t.Context()

	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrOutOfScope) {
			t.Fatalf("%s: want ErrOutOfScope, got %v", what, err)
		}
	}
	refused("sibling cancels sibling", rt.Cancel(ctx, task.ID, b, a))
	refused("child cancels parent", rt.Cancel(ctx, task.ID, a1, a))
	_, err := rt.Status(ctx, task.ID, a, b)
	refused("status of sibling", err)
	_, err = rt.Wait(ctx, task.ID, a, []string{a1, b}, WaitAny)
	refused("wait including sibling", err)
	_, err = rt.Message(ctx, task.ID, a, b, "hi", "")
	refused("message to sibling", err)
	_, err = rt.Message(ctx, task.ID, a1, a, "hi", "")
	refused("message to parent", err)
	if _, err := rt.Status(ctx, task.ID, "ghost", a); err == nil || errors.Is(err, ErrOutOfScope) {
		t.Fatalf("unknown caller: %v", err)
	}
	if got := stateOf(t, s, protocol.RuntimeAgent{TaskID: task.ID, ID: a}); got.State != protocol.AgentStateRunning {
		t.Fatalf("refused cancel changed a: %s", got.State)
	}

	if _, err := rt.Status(ctx, task.ID, a, a1); err != nil {
		t.Fatalf("parent status of child: %v", err)
	}
	if _, err := rt.Status(ctx, task.ID, a, a); err != nil {
		t.Fatalf("self status: %v", err)
	}
	if _, err := rt.Message(ctx, task.ID, a, a1, "hi", ""); err != nil {
		t.Fatalf("parent message to child: %v", err)
	}
	mine, err := rt.List(ctx, task.ID, a)
	if err != nil || len(mine) != 1 || mine[0].ID != a1 {
		t.Fatalf("non-lead list = %+v %v", mine, err)
	}
	if leaf, err := rt.List(ctx, task.ID, a1); err != nil || len(leaf) != 0 {
		t.Fatalf("leaf list = %+v %v", leaf, err)
	}
	all, err := rt.List(ctx, task.ID, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("lead list = %d %v", len(all), err)
	}
	if err := rt.Cancel(ctx, task.ID, a, a1); err != nil {
		t.Fatalf("parent cancels child: %v", err)
	}
	waitRuntimeState(t, rt, task.ID, a1, protocol.AgentStateCancelled)
	if err := rt.Cancel(ctx, task.ID, "", b); err != nil {
		t.Fatalf("lead cancels b: %v", err)
	}
	waitRuntimeState(t, rt, task.ID, b, protocol.AgentStateCancelled)
}

func TestLeadCapabilitiesBoundLeadSpawns(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	lead := RootCapabilities()
	lead.SpawnMax = 1
	lead.Files = protocol.FilesRead
	rt := newTestRuntime(t, s, newFakeRunner(), WithLeadCapabilities(lead))
	first, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	if first.Granted.Files != protocol.FilesRead {
		t.Fatalf("granted files %q exceed the lead's read", first.Granted.Files)
	}
	_, err = rt.Spawn(t.Context(), spawnReq(task, "call-2", "b", 0.25))
	if f := admissionField(t, err); f != "spawn" {
		t.Fatalf("field %q", f)
	}
}

func TestSpawnAfterCloseFails(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	rt := newTestRuntime(t, s, newFakeRunner())
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err := rt.Spawn(t.Context(), spawnReq(task, "late", "late", 0.25))
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("spawn after close: %v, want ErrClosed", err)
	}
	if all, _ := rt.List(t.Context(), task.ID, ""); len(all) != 0 {
		t.Fatalf("agent created after close: %d", len(all))
	}
}

func TestCloseCancelsRunningAndQueuedAgents(t *testing.T) {
	s, task := runtimeFixture(t)
	limits := protocol.DefaultPlanLimits()
	limits.MaxConcurrent = 1
	savePlan(t, s, task, limits, true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	run, err := rt.Spawn(t.Context(), spawnReq(task, "c1", "running", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "running")
	queued, err := rt.Spawn(t.Context(), spawnReq(task, "c2", "queued", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	if queued.QueuedReason == "" {
		t.Fatal("second agent was not queued")
	}
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{run.Agent.ID, queued.Agent.ID} {
		a, err := rt.Status(t.Context(), task.ID, "", id)
		if err != nil || a.State != protocol.AgentStateCancelled {
			t.Fatalf("agent %s: state %q err %v, want cancelled", id, a.State, err)
		}
	}
	if calls := r.Calls(); len(calls) != 1 {
		t.Fatalf("queued agent ran after close: %v", calls)
	}
	if rt.sched.Active(task.ID) != 0 {
		t.Fatal("running slot still held after close")
	}
}

// slowRunner ignores cancellation for a while, then returns.
type slowRunner struct {
	started chan struct{}
	release chan struct{}
	exited  chan struct{}
}

func (r *slowRunner) Run(ctx context.Context, _ protocol.RuntimeAgent) (protocol.AgentResult, error) {
	close(r.started)
	<-r.release
	close(r.exited)
	return protocol.AgentResult{}, ctx.Err()
}

func TestCloseWaitsAndIsIdempotent(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := &slowRunner{started: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{})}
	rt := newTestRuntime(t, s, r)
	if _, err := rt.Spawn(t.Context(), spawnReq(task, "c1", "slow", 0.25)); err != nil {
		t.Fatal(err)
	}
	<-r.started

	// A bounded Close returns the context error while the runner is stuck.
	short, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := rt.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close with stuck runner: %v, want deadline exceeded", err)
	}
	// It waits for the runner: release it shortly and Close must only return after.
	go func() { time.Sleep(100 * time.Millisecond); close(r.release) }()
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.exited:
	default:
		t.Fatal("Close returned before the runner exited")
	}
	// Idempotent: a further Close is a no-op.
	if err := rt.Close(t.Context()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestUpdateWakesWaitersAndEmitsEvent(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	var mu sync.Mutex
	var seen []protocol.RuntimeAgent
	rt := newTestRuntime(t, s, r, WithEvents(func(a protocol.RuntimeAgent) { mu.Lock(); seen = append(seen, a); mu.Unlock() }))
	res, err := rt.Spawn(t.Context(), spawnReq(task, "c1", "u", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "u")
	// Wait for a state no scheduler transition produces: only the runner-side
	// update below changes the agent's model, and the waiter must notice.
	woke := make(chan protocol.RuntimeAgent, 1)
	go func() {
		for {
			ch := rt.sched.Changed(task.ID)
			a, _ := rt.Status(context.Background(), task.ID, "", res.Agent.ID)
			if a.ModelActual == "p/m" {
				woke <- a
				return
			}
			<-ch
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if err := rt.Update(t.Context(), task.ID, res.Agent.ID, func(a *protocol.RuntimeAgent) bool { a.ModelActual = "p/m"; return true }); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-woke:
		if a.ModelActual != "p/m" {
			t.Fatalf("woke with %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner-side update did not wake waiters")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[len(seen)-1].ModelActual != "p/m" {
		t.Fatalf("events did not receive the update: %+v", seen)
	}
	// No-op mutate writes nothing and emits nothing.
	n := len(seen)
	mu.Unlock()
	if err := rt.Update(t.Context(), task.ID, res.Agent.ID, func(*protocol.RuntimeAgent) bool { return false }); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(seen) != n {
		t.Fatal("skipped update emitted an event")
	}
}

// Pruning the scheduler's per-task state never changes what Spawn, Wait and
// List answer: replaying a cancelled spawn returns the stored cancelled agent
// without running it, and Wait on terminal agents leaves no state behind.
func TestRuntimePruningKeepsReplayAndWaitSemantics(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	rt := newTestRuntime(t, s, r)
	first, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "a")
	if err = rt.Cancel(t.Context(), task.ID, "", first.Agent.ID); err != nil {
		t.Fatal(err)
	}
	rt.sched.Wait()
	got, err := rt.Wait(t.Context(), task.ID, "", []string{first.Agent.ID}, WaitAll)
	if err != nil || got[0].State != protocol.AgentStateCancelled {
		t.Fatalf("wait: %+v %v", got, err)
	}
	if n := rt.sched.taskCount(); n != 0 {
		t.Fatalf("scheduler keeps %d task entries after Wait on terminal agents", n)
	}
	replay, err := rt.Spawn(t.Context(), spawnReq(task, "call-1", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Agent.ID != first.Agent.ID || replay.Agent.State != protocol.AgentStateCancelled || replay.QueuedReason != "" {
		t.Fatalf("replay of a cancelled spawn = %+v", replay)
	}
	rt.sched.Wait()
	if calls := r.Calls(); len(calls) != 1 {
		t.Fatalf("replay ran the agent again: %v", calls)
	}
	if n := rt.sched.taskCount(); n != 0 {
		t.Fatalf("entries after replay = %d", n)
	}
}

// The resolve options are computed per task at admission time, so two tasks
// (projects) can get different shell offers from one runtime.
func TestRuntimeResolveOptionsAreComputedPerTask(t *testing.T) {
	s, task := runtimeFixture(t)
	p := savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	_ = p
	var seen []string
	supported := false
	rt := newTestRuntime(t, s, newFakeRunner(), WithResolveOptionsFunc(func(_ context.Context, taskID string) ResolveOptions {
		seen = append(seen, taskID)
		if supported {
			return ResolveOptions{ShellReadOnlySupported: true}
		}
		return ResolveOptions{ShellUnavailable: "no sandbox available"}
	}))
	req := spawnReq(task, "c1", "a", 0.25)
	req.Capabilities = &protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadOnly}
	first, err := rt.Spawn(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Granted.Shell != protocol.ShellNone || len(first.Rejected) == 0 {
		t.Fatalf("unavailable shell: granted %+v rejected %+v", first.Granted, first.Rejected)
	}
	supported = true
	req.CallID = "c2"
	second, err := rt.Spawn(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Granted.Shell != protocol.ShellReadOnly {
		t.Fatalf("supported shell: granted %+v", second.Granted)
	}
	if len(seen) != 2 || seen[0] != task.ID || seen[1] != task.ID {
		t.Fatalf("provider saw %v, want one call per admission for the task", seen)
	}
	// A replay never re-resolves.
	if _, err = rt.Spawn(t.Context(), req); err != nil || len(seen) != 2 {
		t.Fatalf("replay called the provider: %v %v", seen, err)
	}
}

// WithAdmitted sees each newly created agent once, before its runner starts,
// with the spawn caller's context; a replay does not call it.
func TestAdmittedHookRunsBeforeRunnerOncePerAgent(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, task, protocol.DefaultPlanLimits(), true)
	r := newFakeRunner()
	type key struct{}
	var mu sync.Mutex
	var seen []string
	rt := newTestRuntime(t, s, r, WithAdmitted(func(ctx context.Context, a protocol.RuntimeAgent) {
		if ctx.Value(key{}) != "lead" {
			t.Errorf("hook did not get the caller's context")
		}
		if calls := r.Calls(); len(calls) != 0 {
			t.Errorf("runner started before the hook: %v", calls)
		}
		mu.Lock()
		seen = append(seen, a.ID)
		mu.Unlock()
	}))
	ctx := context.WithValue(t.Context(), key{}, "lead")
	res, err := rt.Spawn(ctx, spawnReq(task, "c1", "a", 0.25))
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, r, "a")
	if _, err = rt.Spawn(ctx, spawnReq(task, "c1", "a", 0.25)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != res.Agent.ID {
		t.Fatalf("hook saw %v, want once for %s", seen, res.Agent.ID)
	}
}
