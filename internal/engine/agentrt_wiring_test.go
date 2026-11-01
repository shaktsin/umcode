package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

func runtimeV2On(c *config.Config) { c.Agents.RuntimeV2 = true }

// agentToolNames returns the registered agent.* tool names, excluding the
// workflow tools that are registered in both modes.
func agentToolNames(e *Engine) []string {
	workflow := map[string]bool{"agent.integrate": true, "agent.complete": true, "agent.review_report": true}
	var names []string
	for _, tool := range e.Tools.All() {
		if n := tool.Name(); strings.HasPrefix(n, "agent.") && !workflow[n] {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

func TestRuntimeV2FlagOffKeepsLegacyTools(t *testing.T) {
	e, _, _, _ := delegationFixture(t)
	if _, ok := e.Tools.Get("agent.followup"); !ok {
		t.Fatal("legacy agent.followup missing with runtime_v2 off")
	}
	if _, ok := e.Tools.Get("agent.message"); ok {
		t.Fatal("runtime agent.message registered with runtime_v2 off")
	}
	if e.AgentRuntime != nil {
		t.Fatal("agent runtime constructed with runtime_v2 off")
	}
	for _, n := range []string{"agent.integrate", "agent.complete", "agent.review_report"} {
		if _, ok := e.Tools.Get(n); !ok {
			t.Fatalf("workflow tool %s missing", n)
		}
	}
}

func TestRuntimeV2FlagOnRegistersRuntimeTools(t *testing.T) {
	e, _, _, _ := delegationFixtureWith(t, runtimeV2On)
	want := []string{"agent.cancel", "agent.list", "agent.message", "agent.propose_plan", "agent.spawn", "agent.status", "agent.suggest", "agent.wait"}
	if got := agentToolNames(e); !slices.Equal(got, want) {
		t.Fatalf("agent tools = %v, want %v", got, want)
	}
	if _, ok := e.Tools.Get("agent.followup"); ok {
		t.Fatal("legacy agent.followup registered with runtime_v2 on")
	}
	if e.AgentRuntime == nil {
		t.Fatal("agent runtime not constructed with runtime_v2 on")
	}
	for _, n := range []string{"agent.integrate", "agent.complete", "agent.review_report"} {
		if _, ok := e.Tools.Get(n); !ok {
			t.Fatalf("workflow tool %s missing", n)
		}
	}
}

// runtimeV2Admitted starts a delegated turn with runtime_v2 on and approves a
// one-entry plan for its task, returning the admitted lead context.
func runtimeV2Admitted(t *testing.T) (*Engine, protocol.Thread, protocol.Turn, protocol.AgentTask, context.Context) {
	t.Helper()
	e, th, root, p := delegationFixtureWith(t, runtimeV2On)
	activation, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The plan is approved inside the activated turn, which stays running.
	turn, _ := liveAdmittedTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken})
	task, err := e.Store.AgentTask(t.Context(), p.last())
	if err != nil {
		t.Fatal(err)
	}
	plan := protocol.AgentPlan{ID: "plan-key", ThreadID: th.ID, Limits: protocol.DefaultPlanLimits(),
		Agents: []protocol.PlanAgent{{Name: "worker", Purpose: "investigate", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}}}}
	if err = e.Store.SaveAgentPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.ApproveAgentPlan(t.Context(), plan.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	admitted := tools.WithScope(withDelegationTask(t.Context(), task), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
	return e, th, turn, task, admitted
}

func TestRuntimeV2SpawnKeyIncludesTurnAndOccurrence(t *testing.T) {
	e, th, turn, task, admitted := runtimeV2Admitted(t)
	later := protocol.Turn{ID: store.NewID("turn"), ThreadID: th.ID, Status: protocol.TurnCompleted, StartedAt: time.Now()}
	if err := e.Store.CreateTurn(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	turns := map[string]protocol.Turn{"turn-1": turn, "turn-2": later}
	spawn := func(turnID string, occurrence int) string {
		t.Helper()
		// Providers without call IDs reuse synthetic IDs ("call_0") every
		// model iteration of a turn.
		call := llm.ToolCall{ID: "call_0", Name: tools.ToWire("agent.spawn"), Args: json.RawMessage(`{"planEntry":"worker","assignment":{"prompt":"look around"}}`)}
		r := e.runTool(withToolOccurrence(admitted, occurrence), t.Context(), th, turns[turnID], call, nil)
		if r.IsError {
			t.Fatalf("spawn failed: %s", r.Output)
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(r.Output), &out); err != nil || out.ID == "" {
			t.Fatalf("spawn output %q: %v", r.Output, err)
		}
		return out.ID
	}
	first := spawn("turn-1", 1)
	second := spawn("turn-1", 2)
	if second == first {
		t.Fatal("second iteration's spawn with a reused provider call ID replayed the first agent")
	}
	if retry := spawn("turn-1", 1); retry != first {
		t.Fatalf("genuine retry (same turn, call ID, occurrence) created %s, want replay of %s", retry, first)
	}
	if other := spawn("turn-2", 1); other == first || other == second {
		t.Fatal("spawn in another turn replayed an earlier agent")
	}
	all, err := e.Store.RuntimeAgents(t.Context(), task.ID)
	if err != nil || len(all) != 3 {
		t.Fatalf("agents = %d err=%v", len(all), err)
	}
}

func TestRuntimeCallerResolvesAgentID(t *testing.T) {
	ctx := t.Context()
	if id, ok := runtimeCaller(withRuntimeAgent(ctx, "a1")); !ok || id != "a1" {
		t.Fatalf("agent ctx: id=%q ok=%v, want a1 true", id, ok)
	}
	if id, ok := runtimeCaller(ctx); !ok || id != "" {
		t.Fatalf("lead ctx: id=%q ok=%v, want empty true", id, ok)
	}
	// A runtime agent's real turn context carries its execution and the ID.
	own := withRuntimeAgent(withChildExecution(WithDelegationChild(ctx), &childExecution{runtime: &runtimeExecution{agent: protocol.RuntimeAgent{ID: "a1"}}}), "a1")
	if id, ok := runtimeCaller(own); !ok || id != "a1" {
		t.Fatalf("own execution ctx: id=%q ok=%v", id, ok)
	}
	// Anything legacy or ambiguous fails closed.
	for name, child := range map[string]context.Context{
		"delegation child":     WithDelegationChild(ctx),
		"legacy child":         withChildExecution(ctx, &childExecution{}),
		"id of another agent":  withRuntimeAgent(withChildExecution(WithDelegationChild(ctx), &childExecution{runtime: &runtimeExecution{agent: protocol.RuntimeAgent{ID: "a2"}}}), "a1"),
		"id on a legacy child": withRuntimeAgent(withChildExecution(WithDelegationChild(ctx), &childExecution{}), "a1"),
	} {
		if id, ok := runtimeCaller(child); ok {
			t.Fatalf("%s resolved as caller %q", name, id)
		}
	}
}

// agentToolCtx is a lead context admitted for env's task, carrying agentID as
// the calling agent when it is not empty.
func (env runtimeRunnerEnv) agentToolCtx(t *testing.T, agentID string) context.Context {
	t.Helper()
	proj, err := env.e.Projects.Get(t.Context(), env.th.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(withDelegationTask(t.Context(), env.task), &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: proj.Root})
	if agentID != "" {
		// What the agent's own turn carries: its execution, task and ID, and
		// its own checkout as the working folder.
		a, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, agentID)
		if err != nil {
			t.Fatal(err)
		}
		checkout := t.TempDir()
		x := &childExecution{task: env.task, workspace: ChildWorkspace{Root: checkout}, runtime: &runtimeExecution{agent: a}}
		ctx = withRuntimeAgent(withChildExecution(WithDelegationChild(tools.WithScope(t.Context(), &tools.Scope{ProjectID: env.th.ProjectID, Root: checkout})), x), agentID)
	}
	return ctx
}

func callAgentTool(t *testing.T, e *Engine, ctx context.Context, name, callID, args string) (string, error) {
	t.Helper()
	tool, ok := e.Tools.Get(name)
	if !ok {
		t.Fatalf("missing %s", name)
	}
	return tool.Call(tools.WithAgentCallID(ctx, callID), json.RawMessage(args))
}

func blockUntilCancelled(started chan<- struct{}) func(int, context.Context, llm.Request) []llm.Event {
	return func(_ int, ctx context.Context, _ llm.Request) []llm.Event {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return []llm.Event{{Type: llm.EventError, Err: ctx.Err()}}
	}
}

func TestChildSpawnIsIntersectedWithOwnCaps(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	started := make(chan struct{}, 4)
	env.p.script = blockUntilCancelled(started)
	env.approvePlan(t, protocol.DefaultPlanLimits(),
		protocol.PlanAgent{Name: "parent", Purpose: "coordinate", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, SpawnMax: 1}},
		protocol.PlanAgent{Name: "worker", Purpose: "write", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellNone}})
	// The lead spawns a1: read-only with SpawnMax 1.
	out, err := callAgentTool(t, env.e, env.agentToolCtx(t, ""), "agent.spawn", "lead-1", `{"planEntry":"parent","assignment":{"prompt":"coordinate"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var a1 struct{ ID string }
	if err = json.Unmarshal([]byte(out), &a1); err != nil || a1.ID == "" {
		t.Fatalf("spawn: %q %v", out, err)
	}
	// a1 asks for a read-write child: it gets read, plus a rejection, even
	// though the lead itself could grant read-write.
	out, err = callAgentTool(t, env.e, env.agentToolCtx(t, a1.ID), "agent.spawn", "a1-1",
		`{"planEntry":"worker","assignment":{"prompt":"edit"},"capabilities":{"files":"read-write","shell":"none","network":false,"spawnMax":0}}`)
	if err != nil {
		t.Fatal(err)
	}
	var child struct {
		ID       string
		Granted  protocol.AgentCapabilities
		Rejected []protocol.CapabilityRejection
	}
	if err = json.Unmarshal([]byte(out), &child); err != nil {
		t.Fatal(err)
	}
	if child.Granted.Files != protocol.FilesRead {
		t.Fatalf("granted files = %q, want read (the caller's own cap)", child.Granted.Files)
	}
	if !slices.ContainsFunc(child.Rejected, func(r protocol.CapabilityRejection) bool { return strings.Contains(r.Field, "files") }) {
		t.Fatalf("rejected = %+v, want a files rejection", child.Rejected)
	}
	stored, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, child.ID)
	if err != nil || stored.ParentAgentID != a1.ID {
		t.Fatalf("child parent = %q err %v, want %s", stored.ParentAgentID, err, a1.ID)
	}
	_ = env.e.AgentRuntime.Close(t.Context())
}

func TestAuthorizeDelegationToolForRuntimeAgentContext(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	a := env.newRuntimeAgent(t, "kid", readOnlyCaps, "")
	checkout := t.TempDir()
	x := &childExecution{task: env.task, workspace: ChildWorkspace{Root: checkout}, runtime: &runtimeExecution{agent: a}}
	ctx := withRuntimeAgent(withChildExecution(WithDelegationChild(tools.WithScope(t.Context(), &tools.Scope{ThreadID: "internal", ProjectID: env.th.ProjectID, Root: checkout})), x), a.ID)
	task, err := env.e.authorizeDelegationTool(ctx)
	if err != nil || task.ID != env.task.ID {
		t.Fatalf("runtime agent authorization: task %q err %v", task.ID, err)
	}
	// A foreign or altered execution is refused.
	foreign := *x
	foreign.task.ThreadID = "someone-else"
	if _, err = env.e.authorizeDelegationTool(withRuntimeAgent(withChildExecution(WithDelegationChild(tools.WithScope(t.Context(), &tools.Scope{Root: checkout})), &foreign), a.ID)); err == nil {
		t.Fatal("foreign runtime execution authorized")
	}
	// A legacy child never is.
	legacy := &childExecution{task: env.task, workspace: ChildWorkspace{Root: checkout}}
	if _, err = env.e.authorizeDelegationTool(withChildExecution(WithDelegationChild(tools.WithScope(t.Context(), &tools.Scope{Root: checkout})), legacy)); err == nil {
		t.Fatal("legacy child authorized")
	}
}

func agentStates(t *testing.T, e *Engine, taskID string) map[string]protocol.AgentState {
	t.Helper()
	all, err := e.Store.RuntimeAgents(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]protocol.AgentState{}
	for _, a := range all {
		out[a.Name] = a.State
	}
	return out
}

func waitForSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// stopFixture runs one running and one queued agent (maxConcurrent 1) whose
// provider calls block until their context ends.
func stopFixture(t *testing.T) (runtimeRunnerEnv, chan struct{}) {
	t.Helper()
	env := runtimeRunnerFixture(t, runtimeV2On)
	started := make(chan struct{}, 4)
	env.p.script = blockUntilCancelled(started)
	limits := protocol.DefaultPlanLimits()
	limits.MaxConcurrent = 1
	env.approvePlan(t, limits, protocol.PlanAgent{Name: "worker", Purpose: "investigate", Capabilities: readOnlyCaps})
	ctx := env.agentToolCtx(t, "")
	for _, call := range []string{"first", "second"} {
		if _, err := callAgentTool(t, env.e, ctx, "agent.spawn", call, `{"name":"`+call+`","planEntry":"worker","assignment":{"prompt":"look"}}`); err != nil {
			t.Fatal(err)
		}
	}
	waitForSignal(t, started, "the first agent's provider call")
	return env, started
}

func requireAllCancelled(t *testing.T, env runtimeRunnerEnv) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		states := agentStates(t, env.e, env.task.ID)
		done := len(states) == 2
		for _, st := range states {
			if st != protocol.AgentStateCancelled {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agents = %v, want all cancelled", states)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The provider call and the agent turn ended.
	deadline = time.Now().Add(10 * time.Second)
	for {
		env.e.mu.Lock()
		live := len(env.e.activeTurns)
		env.e.mu.Unlock()
		if live == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("an agent turn is still running after cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCancelDelegationCancelsRuntimeAgents(t *testing.T) {
	env, _ := stopFixture(t)
	if states := agentStates(t, env.e, env.task.ID); states["first"] != protocol.AgentStateRunning || states["second"] != protocol.AgentStateQueued {
		t.Fatalf("setup states = %v", states)
	}
	if _, err := env.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: env.th.ID, TaskID: env.task.ID}); err != nil {
		t.Fatal(err)
	}
	requireAllCancelled(t, env)
}

func TestStopTurnCancelsRuntimeAgents(t *testing.T) {
	env, _ := stopFixture(t)
	at := &activeTurn{taskID: env.task.ID, cancel: func() {}}
	env.e.mu.Lock()
	env.e.activeTurns["stop-turn"] = at
	env.e.mu.Unlock()
	if err := env.e.InterruptTurn("stop-turn"); err != nil {
		t.Fatal(err)
	}
	env.e.mu.Lock()
	delete(env.e.activeTurns, "stop-turn")
	env.e.mu.Unlock()
	requireAllCancelled(t, env)
}

func TestCancelDelegationWithoutRuntimeIsUnchanged(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	if e.AgentRuntime != nil {
		t.Fatal("runtime constructed with the flag off")
	}
	if _, err := e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: th.ID, TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Store.AgentTask(t.Context(), task.ID)
	if err != nil || got.State != protocol.AgentStateCancelled {
		t.Fatalf("task = %q err %v", got.State, err)
	}
}

type exitRecorder struct {
	started chan struct{}
	exited  chan struct{}
}

func (r *exitRecorder) Run(ctx context.Context, _ protocol.RuntimeAgent) (protocol.AgentResult, error) {
	close(r.started)
	<-ctx.Done()
	time.Sleep(100 * time.Millisecond) // a runner needs a moment to wind down
	close(r.exited)
	return protocol.AgentResult{}, ctx.Err()
}

func TestShutdownClosesRuntimeBeforeReturning(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	env.approvePlan(t, protocol.DefaultPlanLimits(), protocol.PlanAgent{Name: "worker", Purpose: "investigate", Capabilities: readOnlyCaps})
	r := &exitRecorder{started: make(chan struct{}), exited: make(chan struct{})}
	env.e.AgentRuntime = agentrt.NewRuntime(env.e.Store, r, env.e.baseCtx)
	res, err := env.e.AgentRuntime.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "sd", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, r.started, "the runner to start")
	env.e.Shutdown(t.Context())
	select {
	case <-r.exited:
	default:
		t.Fatal("Shutdown returned before the runtime agent's runner exited")
	}
	got, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, res.Agent.ID)
	if err != nil || got.State != protocol.AgentStateCancelled {
		t.Fatalf("agent after shutdown: %q err %v, want cancelled", got.State, err)
	}
	if _, err = env.e.AgentRuntime.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "after", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "late"}}); !errors.Is(err, agentrt.ErrClosed) {
		t.Fatalf("spawn after shutdown: %v", err)
	}
}

func TestEngineStartReconcilesRuntimeAgentsWithFlagOff(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	st := e.Store
	turn := protocol.Turn{ID: store.NewID("turn"), ThreadID: th.ID, Status: protocol.TurnCompleted, StartedAt: time.Now()}
	if err := st.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(t.Context(), `INSERT INTO agent_tasks (id,thread_id,root_turn_id,activation,state,base_revision,base_root,project_id,created_at,updated_at,parent_root) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		"task-old", th.ID, turn.ID, protocol.AgentActivationExplicitUser, protocol.AgentStateRunning, "base", "", th.ProjectID, store.Now(), store.Now(), ""); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateRuntimeAgent(t.Context(), protocol.RuntimeAgent{TaskID: "task-old", Name: "left-running"}, "call-old")
	if err != nil {
		t.Fatal(err)
	}
	a.State = protocol.AgentStateRunning
	if err := st.UpdateRuntimeAgent(t.Context(), a, a.Version); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider = "claude"
	cfg.LLM.Model = "deterministic"
	reg := llm.NewRegistry()
	reg.Register(&delegationProvider{})
	restarted, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Shutdown(context.Background()) })
	if restarted.AgentRuntime != nil {
		t.Fatal("runtime constructed with runtime_v2 off")
	}
	got, err := st.RuntimeAgent(t.Context(), "task-old", a.ID)
	if err != nil || got.State != protocol.AgentStateInterrupted {
		t.Fatalf("state=%s err=%v; want interrupted after restart with the flag off", got.State, err)
	}
}

func budgetEntry(e *Engine, taskID string) bool {
	_, ok := e.runtimeBudgets.Load(taskID)
	return ok
}

func trackerEntry(e *Engine, taskID string) bool {
	_, ok := e.runtimeBudgetTrackers.Load(taskID)
	return ok
}

func trackerCount(e *Engine) int {
	n := 0
	e.runtimeBudgetTrackers.Range(func(any, any) bool { n++; return true })
	return n
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (env runtimeRunnerEnv) trackBudget(t *testing.T) {
	t.Helper()
	env.e.mu.Lock()
	limits := env.e.turnLimits
	env.e.mu.Unlock()
	_, shared, err := env.e.DelegationCoordinator.Task(context.WithoutCancel(t.Context()), env.task, limits)
	if err != nil {
		t.Fatal(err)
	}
	env.e.trackRuntimeBudget(env.task.ID, shared)
	if !budgetEntry(env.e, env.task.ID) {
		t.Fatal("budget not registered")
	}
}

func (env runtimeRunnerEnv) completeTask(t *testing.T) {
	t.Helper()
	cur, err := env.e.Store.AgentTask(t.Context(), env.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = env.e.Store.UpdateAgentTaskState(t.Context(), cur.ID, cur.ThreadID, cur.ProjectID, cur.State, protocol.AgentStateCompleted); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeBudgetForgottenAfterTaskCancel(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	env.trackBudget(t)
	if _, err := env.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: env.th.ID, TaskID: env.task.ID}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "budget entry removed after CancelDelegation", func() bool { return !budgetEntry(env.e, env.task.ID) })
}

func TestRuntimeBudgetForgottenAfterTaskCompletes(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	env.trackBudget(t)
	time.Sleep(3 * runtimeBudgetPoll)
	if !budgetEntry(env.e, env.task.ID) {
		t.Fatal("budget forgotten while the task is active")
	}
	env.completeTask(t)
	eventually(t, "budget entry removed after completion", func() bool { return !budgetEntry(env.e, env.task.ID) })
	eventually(t, "tracker removed with the budget", func() bool { return !trackerEntry(env.e, env.task.ID) })
}

func TestRuntimeBudgetKeptWhileAgentRuns(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	env.approvePlan(t, protocol.DefaultPlanLimits(), protocol.PlanAgent{Name: "worker", Purpose: "investigate", Capabilities: readOnlyCaps})
	r := &exitRecorder{started: make(chan struct{}), exited: make(chan struct{})}
	rt := agentrt.NewRuntime(env.e.Store, r, env.e.baseCtx)
	env.e.AgentRuntime = rt
	res, err := rt.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "keep", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, r.started, "the runner to start")
	env.trackBudget(t)
	env.completeTask(t) // the lead finishes while the agent still runs
	time.Sleep(5 * runtimeBudgetPoll)
	if !budgetEntry(env.e, env.task.ID) {
		t.Fatal("budget forgotten while a runtime agent is still running")
	}
	if err = rt.Cancel(t.Context(), env.task.ID, "", res.Agent.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "budget entry removed once the agent stopped", func() bool { return !budgetEntry(env.e, env.task.ID) })
}

func TestRuntimeBudgetOneTrackerPerTask(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	for i := 0; i < 3; i++ {
		env.trackBudget(t) // one per lead turn
	}
	if n := trackerCount(env.e); n != 1 {
		t.Fatalf("trackers = %d, want 1", n)
	}
	// After the task ends and the budget is forgotten, a fresh budget can be
	// tracked again.
	kept, _ := env.e.runtimeBudgets.Load(env.task.ID)
	env.completeTask(t)
	eventually(t, "tracker removed", func() bool { return !trackerEntry(env.e, env.task.ID) && !budgetEntry(env.e, env.task.ID) })
	env.e.trackRuntimeBudget(env.task.ID, kept.(*delegation.Budget))
	if !trackerEntry(env.e, env.task.ID) {
		t.Fatal("tracker did not register again after forget")
	}
	eventually(t, "second tracker also cleans up", func() bool { return !budgetEntry(env.e, env.task.ID) && !trackerEntry(env.e, env.task.ID) })
}

// A forgotten budget on an ACTIVE task fails closed without calling the model.
func TestRuntimeLateRunnerFailsClosedOnActiveTask(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	a := env.newRuntimeAgent(t, "late", readOnlyCaps, "")
	env.trackBudget(t)
	kept, _ := env.e.runtimeBudgets.Load(env.task.ID)
	env.e.forgetRuntimeBudget(env.task.ID, kept.(*delegation.Budget))
	if _, err := env.run(t, a); err == nil || !strings.Contains(err.Error(), "task budget unavailable") {
		t.Fatalf("late runner err = %v, want task budget unavailable", err)
	}
	if reqs, _ := env.p.snapshot(); len(reqs) != 0 {
		t.Fatal("provider called by a late runner")
	}
}

// Forgetting compares the budget pointer: a fresh budget registered for the
// task meanwhile is never clobbered, and keeps a tracker.
func TestForgetRuntimeBudgetKeepsFreshlyRegisteredBudget(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	stale := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 100}, protocol.UsageTotals{}, nil)
	fresh := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 200}, protocol.UsageTotals{}, nil)
	env.e.rememberRuntimeBudget(env.task.ID, fresh)
	env.e.forgetRuntimeBudget(env.task.ID, stale)
	if got, ok := env.e.runtimeBudgets.Load(env.task.ID); !ok || got.(*delegation.Budget) != fresh {
		t.Fatalf("fresh budget clobbered by a stale forget: %v %v", got, ok)
	}
	if !trackerEntry(env.e, env.task.ID) {
		t.Fatal("the fresh budget has no tracker")
	}
	env.e.forgetRuntimeBudget(env.task.ID, fresh)
	if budgetEntry(env.e, env.task.ID) {
		t.Fatal("matching forget did not remove the budget")
	}
}

func TestRuntimeCloseBudget(t *testing.T) {
	with := func(d time.Duration) context.Context {
		ctx, cancel := context.WithTimeout(t.Context(), d)
		t.Cleanup(cancel)
		return ctx
	}
	if got := runtimeCloseBudget(t.Context()); got != runtimeCloseTimeout {
		t.Errorf("no deadline = %v, want %v", got, runtimeCloseTimeout)
	}
	if got := runtimeCloseBudget(with(time.Hour)); got != runtimeCloseTimeout {
		t.Errorf("long deadline = %v, want the %v cap", got, runtimeCloseTimeout)
	}
	if got := runtimeCloseBudget(with(10 * time.Second)); got < 4*time.Second || got > 5*time.Second {
		t.Errorf("10s remaining = %v, want half", got)
	}
	if got := runtimeCloseBudget(with(1500 * time.Millisecond)); got != time.Second {
		t.Errorf("1.5s remaining = %v, want the 1s floor", got)
	}
}

// hangingRunner ignores its context, like a runner stuck in a syscall.
type hangingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *hangingRunner) Run(context.Context, protocol.RuntimeAgent) (protocol.AgentResult, error) {
	close(r.started)
	<-r.release
	return protocol.AgentResult{}, nil
}

// A runner that never stops must not starve the rest of Shutdown: Close gets
// at most half of the caller's deadline, so Plugins and MCP still close.
func TestShutdownReachesPluginsAndMCPWhenRunnerHangs(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	env.approvePlan(t, protocol.DefaultPlanLimits(), protocol.PlanAgent{Name: "worker", Purpose: "investigate", Capabilities: readOnlyCaps})
	r := &hangingRunner{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(r.release) })
	env.e.AgentRuntime = agentrt.NewRuntime(env.e.Store, r, env.e.baseCtx)
	if _, err := env.e.AgentRuntime.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "hang", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "look"}}); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, r.started, "the runner to start")
	const budget = 4 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	start := time.Now()
	env.e.Shutdown(ctx)
	if elapsed := time.Since(start); elapsed > budget-time.Second {
		t.Fatalf("Shutdown used %v of %v: a hung runner starved the rest of it", elapsed, budget)
	}
	if _, err := env.e.Plugins.Acquire(t.Context(), env.th.ProjectID); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("plugins were not closed by Shutdown: %v", err)
	}
}

// failingListStore makes the runtime's agent listing fail, so cancelling the
// task's runtime agents returns an error.
type failingListStore struct {
	agentrt.AgentStore
}

func (failingListStore) RuntimeAgents(context.Context, string) ([]protocol.RuntimeAgent, error) {
	return nil, errors.New("injected runtime list failure")
}

// S1: when the legacy task cancel succeeded but the runtime cancel failed,
// clients still hear about the cancelled task; the error is still returned.
func TestCancelDelegationPublishesDespiteRuntimeCancelError(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	e.AgentRuntime = agentrt.NewRuntime(failingListStore{e.Store}, instantRunner{}, e.baseCtx)
	sub := &childSubscriber{events: make(chan childEvent, 64)}
	e.Bus.Add(sub)
	defer e.Bus.Remove(sub.ID())
	_, err := e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: th.ID, TaskID: task.ID})
	if err == nil || !strings.Contains(err.Error(), "injected runtime list failure") {
		t.Fatalf("err = %v, want the runtime cancel error", err)
	}
	got, getErr := e.Store.AgentTask(t.Context(), task.ID)
	if getErr != nil || got.State != protocol.AgentStateCancelled {
		t.Fatalf("legacy cancel did not happen: %q %v", got.State, getErr)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.events:
			if ev.method == protocol.NotifyDelegationUpdated {
				if d := ev.params.(protocol.DelegationUpdated); d.TaskID == task.ID && d.Task != nil && d.Task.State == protocol.AgentStateCancelled {
					return
				}
			}
		case <-deadline:
			t.Fatal("no delegation update published for the cancelled task")
		}
	}
}
