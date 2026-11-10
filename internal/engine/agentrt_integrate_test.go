package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// integrateOut is the part of agent.integrate's result the tests read.
type integrateOut struct {
	HeadTree     string
	AppliedCount int
	Amendments   int
	Revisions    []store.AgentRevision
	Verified     bool
	Settled      bool
	Revision     *store.AgentRevision
	Message      string
	Options      []string
	Checks       []protocol.AgentWorkflowCheck
	Verification string
	TaskState    protocol.AgentState
	// CleanupWarning is set when completion retained agent workspaces.
	CleanupWarning string
}

func (env rvEnv) integrate(t *testing.T, ctx context.Context, args string) (integrateOut, error) {
	t.Helper()
	raw, err := callAgentTool(t, env.e, ctx, "agent.integrate", "integrate-"+args, args)
	var out integrateOut
	if err == nil {
		if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
			t.Fatalf("agent.integrate output %q: %v", raw, jerr)
		}
	}
	return out, err
}

func (env rvEnv) mustIntegrate(t *testing.T, args string) integrateOut {
	t.Helper()
	out, err := env.integrate(t, env.ctx, args)
	if err != nil {
		t.Fatalf("agent.integrate %s: %v", args, err)
	}
	return out
}

// passChecks records passing results for both planned checks of the head,
// through the real turn-loop bracket.
func (env rvEnv) passChecks(t *testing.T) {
	t.Helper()
	env.runner.set(runResults("passed", "passed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
}

func (env rvEnv) revision(t *testing.T, agentID string) store.AgentRevision {
	t.Helper()
	r, err := env.e.Store.AgentRevision(t.Context(), env.task.ID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (env rvEnv) taskState(t *testing.T) protocol.AgentState {
	t.Helper()
	got, err := env.e.Store.AgentTask(t.Context(), env.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.State
}

// liveAgent stores a runtime agent with caps in state running.
func (env rvEnv) liveAgent(t *testing.T, name string, caps protocol.AgentCapabilities) protocol.RuntimeAgent {
	t.Helper()
	ctx, s := t.Context(), env.e.Store
	a, err := s.CreateRuntimeAgent(ctx, protocol.RuntimeAgent{TaskID: env.task.ID, Name: name, Purpose: "work",
		Assignment: protocol.AgentAssignment{Prompt: "work"}, Capabilities: caps}, "call-"+name)
	if err != nil {
		t.Fatal(err)
	}
	a.State = protocol.AgentStateRunning
	if err = s.UpdateRuntimeAgent(ctx, a, a.Version); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	return a
}

// agentCtx is what a runtime agent's own turn carries.
func (env rvEnv) agentCtx(t *testing.T, a protocol.RuntimeAgent) context.Context {
	t.Helper()
	checkout := t.TempDir()
	x := &childExecution{task: env.task, workspace: ChildWorkspace{Root: checkout}, runtime: &runtimeExecution{agent: a}}
	return withRuntimeAgent(withChildExecution(WithDelegationChild(tools.WithScope(t.Context(), &tools.Scope{ProjectID: env.task.ProjectID, Root: checkout})), x), a.ID)
}

func (env rvEnv) parentStatus(t *testing.T) string {
	t.Helper()
	return delegationGit(t, env.root, "status", "--porcelain")
}

func TestIntegrateToolLeadOnly(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	strong := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true, SpawnMax: 3}
	if slices.Contains(agentrt.AllowedTools(strong), "agent.integrate") {
		t.Fatal("capabilities grant agent.integrate")
	}
	coordinator := env.liveAgent(t, "coordinator", strong)
	for name, ctx := range map[string]context.Context{
		"runtime agent with spawnMax": env.agentCtx(t, coordinator),
		"the writer itself":           env.agentCtx(t, a),
		"legacy child":                withChildExecution(WithDelegationChild(env.ctx), &childExecution{task: env.task}),
	} {
		for _, args := range []string{`{"action":"accept","agent":"` + a.ID + `"}`, `{"action":"status"}`, `{"action":"complete"}`} {
			if _, err := env.integrate(t, ctx, args); err == nil || !strings.Contains(err.Error(), "lead") {
				t.Fatalf("%s: %s err = %v, want a lead-only refusal", name, args, err)
			}
		}
	}
	if r := env.revision(t, a.ID); r.State != store.RevisionSubmitted {
		t.Fatalf("revision = %s after refused calls", r.State)
	}
	if st := env.parentStatus(t); st != "" {
		t.Fatalf("parent changed: %s", st)
	}
	// The lead itself may (once the coordinator, which has no workspace,
	// stopped).
	coordinator.State = protocol.AgentStateCancelled
	if err := env.e.Store.UpdateRuntimeAgent(t.Context(), coordinator, coordinator.Version); err != nil {
		t.Fatal(err)
	}
	if out := env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`); out.Revision == nil || out.Revision.State != store.RevisionApplied {
		t.Fatalf("lead accept = %+v", out)
	}
}

func TestIntegrateToolNeedsWritableTools(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	scope := *tools.ScopeFrom(env.ctx)
	scope.ReadOnly = true
	readOnly := tools.WithScope(env.ctx, &scope)
	for _, args := range []string{`{"action":"accept","agent":"` + a.ID + `"}`, `{"action":"drop","agent":"` + a.ID + `","reason":"no"}`, `{"action":"status"}`, `{"action":"complete"}`} {
		if _, err := env.integrate(t, readOnly, args); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("%s in read-only mode: err = %v", args, err)
		}
	}
	if r := env.revision(t, a.ID); r.State != store.RevisionSubmitted {
		t.Fatalf("revision = %s after read-only calls", r.State)
	}
}

// The accept rule is strict submission order: an earlier revision must be
// applied or dropped (or not integrable) before a later one is accepted.
func TestIntegrateAcceptsInSubmissionOrderOnly(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	c := env.writer(t, "c", "c.txt", "from c\n")
	if _, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+b.ID+`"}`); err == nil || !strings.Contains(err.Error(), "submission order") || !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("accept b before a: err = %v", err)
	}
	if r := env.revision(t, b.ID); r.State != store.RevisionSubmitted {
		t.Fatalf("b = %s after the refusal", r.State)
	}
	if st := env.parentStatus(t); st != "" {
		t.Fatalf("parent changed by a refused accept: %s", st)
	}
	out := env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	if out.Revision.State != store.RevisionApplied || len(out.Checks) != 2 || !strings.HasPrefix(out.Verification, "planned") {
		t.Fatalf("accept a = %+v", out)
	}
	env.passChecks(t)
	// b was submitted before c: c waits for b.
	if _, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+c.ID+`"}`); err == nil || !strings.Contains(err.Error(), "submission order") {
		t.Fatalf("accept c before b: err = %v", err)
	}
	if out = env.mustIntegrate(t, `{"action":"drop","agent":"`+b.ID+`","reason":"superseded"}`); out.Revision.State != store.RevisionDropped || out.Revision.Reason != "superseded" {
		t.Fatalf("drop b = %+v", out)
	}
	if out = env.mustIntegrate(t, `{"action":"accept","agent":"`+c.ID+`"}`); out.Revision.State != store.RevisionApplied || out.AppliedCount != 2 {
		t.Fatalf("accept c after dropping b = %+v", out)
	}
}

func TestIntegrateDropNeedsReason(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	if _, err := env.integrate(t, env.ctx, `{"action":"drop","agent":"`+a.ID+`"}`); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("drop without reason: err = %v", err)
	}
	if _, err := env.integrate(t, env.ctx, `{"action":"accept"}`); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("accept without agent: err = %v", err)
	}
	if _, err := env.integrate(t, env.ctx, `{"action":"merge","agent":"`+a.ID+`"}`); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestCompleteRequiresAllRevisionsSettled(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	if _, err := env.integrate(t, env.ctx, `{"action":"complete"}`); err == nil || !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("complete with a submitted revision: err = %v", err)
	}
	if st := env.taskState(t); st == protocol.AgentStateCompleted {
		t.Fatal("task completed with an unsettled revision")
	}
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	if out := env.mustIntegrate(t, `{"action":"complete"}`); out.TaskState != protocol.AgentStateCompleted || !out.Settled || !out.Verified {
		t.Fatalf("complete = %+v", out)
	}
	if st := env.taskState(t); st != protocol.AgentStateCompleted {
		t.Fatalf("task = %s", st)
	}
}

func TestCompleteRequiresChecksOnFinalHead(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	env.mustIntegrate(t, `{"action":"accept","agent":"`+b.ID+`"}`)
	// The checks passed for a's head, not for the final head.
	if _, err := env.integrate(t, env.ctx, `{"action":"complete"}`); err == nil || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("complete with an unverified final head: err = %v", err)
	}
	env.runner.set(runResults("passed", "failed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	if _, err := env.integrate(t, env.ctx, `{"action":"complete"}`); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("complete with a failed check: err = %v", err)
	}
	env.passChecks(t)
	if out := env.mustIntegrate(t, `{"action":"complete"}`); out.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v", out)
	}
}

func TestCompleteRefusesWithUnfinishedAgents(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	busy := env.liveAgent(t, "busy", readOnlyCaps)
	if _, err := env.integrate(t, env.ctx, `{"action":"complete"}`); err == nil || !strings.Contains(err.Error(), busy.ID) {
		t.Fatalf("complete with a running agent: err = %v", err)
	}
	if st := env.taskState(t); st == protocol.AgentStateCompleted {
		t.Fatal("task completed with a running agent")
	}
}

func TestCompleteWithCancelledPartialWriter(t *testing.T) {
	env := runtimeVerificationEnv(t)
	ctx, s, svc := t.Context(), env.e.Store, env.e.DelegationWorkspaceService
	a := env.writer(t, "a", "a.txt", "from a\n")
	// c started writing, then was cancelled: its partial edit stays in its
	// checkout and it has no revision.
	c := env.liveAgent(t, "c", protocol.AgentCapabilities{Files: protocol.FilesReadWrite})
	if _, err := s.EnsureRuntimeAgentExecution(ctx, c); err != nil {
		t.Fatal(err)
	}
	cref, err := svc.PrepareRuntime(ctx, env.task, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cref.Root, "partial.txt"), []byte("half\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err = s.RuntimeAgent(ctx, env.task.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	c.State = protocol.AgentStateCancelled
	if err = s.UpdateRuntimeAgent(ctx, c, c.Version); err != nil {
		t.Fatal(err)
	}
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	out := env.mustIntegrate(t, `{"action":"complete"}`)
	if out.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v", out)
	}
	if data, err := os.ReadFile(filepath.Join(cref.Root, "partial.txt")); err != nil || string(data) != "half\n" {
		t.Fatalf("cancelled writer's workspace not retained: %q %v", data, err)
	}
	cw, err := s.AgentWorkspace(ctx, cref.ID)
	if err != nil || cw.Cleaned {
		t.Fatalf("cancelled writer's workspace = %+v %v", cw, err)
	}
	aw, err := s.AgentWorkspace(ctx, env.revision(t, a.ID).WorkspaceID)
	if err != nil || !aw.Cleaned {
		t.Fatalf("applied and verified workspace not cleaned: %+v %v", aw, err)
	}
}

func TestCompleteTwiceIsIdempotent(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	first := env.mustIntegrate(t, `{"action":"complete"}`)
	second, err := env.integrate(t, env.ctx, `{"action":"complete"}`)
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if first.TaskState != protocol.AgentStateCompleted || second.TaskState != protocol.AgentStateCompleted || first.HeadTree != second.HeadTree || first.AppliedCount != second.AppliedCount || len(second.Revisions) != 1 {
		t.Fatalf("complete twice: %+v then %+v", first, second)
	}
}

func TestCompleteWithNothingIntegrated(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"drop","agent":"`+a.ID+`","reason":"not needed"}`)
	out := env.mustIntegrate(t, `{"action":"complete"}`)
	if out.TaskState != protocol.AgentStateCompleted || out.AppliedCount != 0 {
		t.Fatalf("complete = %+v", out)
	}
	if st := env.parentStatus(t); st != "" {
		t.Fatalf("parent changed: %s", st)
	}
}

func TestIntegrateReportsPlanPendingAndRetries(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.planner.set("not json")
	out := env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	if out.Revision.State != store.RevisionApplied || !strings.HasPrefix(out.Verification, "plan pending") || len(out.Checks) != 0 {
		t.Fatalf("accept with a failing planner = %+v", out)
	}
	env.planner.set(rvPlanTwo)
	out = env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	if out.AppliedCount != 1 || len(out.Checks) != 2 || !strings.HasPrefix(out.Verification, "planned") {
		t.Fatalf("retried accept = %+v", out)
	}
	// A further replay keeps the recorded results (no re-plan).
	env.passChecks(t)
	out = env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	if !out.Verified || out.AppliedCount != 1 {
		t.Fatalf("replay after verification = %+v", out)
	}
}

func TestIntegrateReportsNoChecksPlanned(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.planner.set(`{"checks":[],"visual_qa":""}`)
	out := env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	if !strings.HasPrefix(out.Verification, "no checks planned") {
		t.Fatalf("accept with an empty plan = %+v", out)
	}
}

func TestRuntimeVerificationUntrackedOutputExplained(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	target, err := env.e.beginDelegationVerification(env.ctx, "verification.run")
	if err != nil || target == nil {
		t.Fatalf("begin = %v %v", target, err)
	}
	// The command wrote build output into the project checkout.
	if err = os.WriteFile(filepath.Join(env.root, "build.out"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = env.e.finishDelegationVerification(env.ctx, "verification.run", target, runResults("passed", "passed"))
	if err == nil || !strings.Contains(err.Error(), "verification output left untracked files in the project checkout") || !strings.Contains(err.Error(), "build.out") {
		t.Fatalf("finish with untracked output: %v", err)
	}
}

// recordingSub collects every notification without blocking the bus.
type recordingSub struct {
	mu     sync.Mutex
	events []childEvent
}

func (*recordingSub) ID() string        { return "integrate-recorder" }
func (*recordingSub) IsAdmin() bool     { return true }
func (*recordingSub) Wants(string) bool { return true }
func (s *recordingSub) Notify(method string, params any) {
	s.mu.Lock()
	s.events = append(s.events, childEvent{method, params})
	s.mu.Unlock()
}
func (s *recordingSub) completedUpdate(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if d, ok := ev.params.(protocol.DelegationUpdated); ok && ev.method == protocol.NotifyDelegationUpdated && d.TaskID == taskID && d.Task != nil && d.Task.State == protocol.AgentStateCompleted {
			return true
		}
	}
	return false
}

const twoWritersBase = "package a\n\nfunc A() int { return 0 }\n"

func TestEndToEndTwoWritersOneConflict(t *testing.T) {
	const fromAlpha = "package a\n\nfunc A() int { return 1 }\n"
	const fromBeta = "package a\n\nfunc A() int { return 2 }\n"
	e, th, root, _ := delegationFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(twoWritersBase), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "a.go")
	baseHead := delegationGit(t, root, "rev-parse", "HEAD")
	task := childExecutionTask(t, e, th)
	p := &runtimeScriptProvider{}
	e.LLMs.Register(p)
	env := runtimeRunnerEnv{e: e, th: th, parent: root, task: task, p: p}
	writer := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellNone}
	env.approvePlan(t, protocol.DefaultPlanLimits(),
		protocol.PlanAgent{Name: "alpha", Purpose: "change A", Capabilities: writer},
		protocol.PlanAgent{Name: "beta", Purpose: "change A differently", Capabilities: writer})
	planner := &stubVerificationTool{name: "verification.plan", out: rvPlanTwo}
	runner := &stubVerificationTool{name: "verification.run"}
	e.Tools.Add(planner)
	e.Tools.Add(runner)
	x := &runtimeE2E{runtimeRunnerEnv: env, entered: make(chan string, 8), calls: map[string]int{}}
	x.lead = env.agentToolCtx(t, "")
	scripts := map[string][]string{"alpha": {fromAlpha}, "beta": {fromBeta}}
	p.script = func(_ int, ctx context.Context, _ llm.Request) []llm.Event {
		id, _ := runtimeAgentFrom(ctx)
		a, err := e.Store.RuntimeAgent(context.WithoutCancel(ctx), task.ID, id)
		if err != nil {
			return []llm.Event{{Type: llm.EventError, Err: err}}
		}
		x.mu.Lock()
		idx := x.calls[a.PlanEntry]
		x.calls[a.PlanEntry]++
		x.mu.Unlock()
		if idx == 0 {
			return toolReply(fmt.Sprintf("%s-0", a.PlanEntry), "file.write", `{"path":"a.go","content":`+jsonString(scripts[a.PlanEntry][0])+`}`)
		}
		return textReply(a.PlanEntry + " changed A")
	}
	sub := &recordingSub{}
	e.Bus.Add(sub)
	defer e.Bus.Remove(sub.ID())
	call := func(args string) (integrateOut, error) {
		raw, err := callAgentTool(t, e, x.lead, "agent.integrate", "e2e-"+args, args)
		var out integrateOut
		if err == nil {
			if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
				t.Fatalf("output %q: %v", raw, jerr)
			}
		}
		return out, err
	}
	wait := func(id string) protocol.RuntimeAgent {
		ctx, cancel := context.WithTimeout(x.lead, 60*time.Second)
		defer cancel()
		if _, err := callAgentTool(t, e, ctx, "agent.wait", "wait-"+id, `{"ids":["`+id+`"],"mode":"all"}`); err != nil {
			t.Fatal(err)
		}
		a := x.agent(t, id)
		if a.State != protocol.AgentStateCompleted {
			t.Fatalf("%s ended %s: %+v", a.Name, a.State, a.Result)
		}
		return a
	}

	// Both writers are spawned before the first accept; alpha submits first.
	alpha := wait(x.spawn(t, "alpha"))
	beta := wait(x.spawn(t, "beta"))
	for _, a := range []protocol.RuntimeAgent{alpha, beta} {
		r, err := e.Store.AgentRevision(t.Context(), task.ID, a.ID)
		if err != nil || r.State != store.RevisionSubmitted || !slices.Equal(r.ChangedPaths, []string{"a.go"}) {
			t.Fatalf("%s revision = %+v %v", a.Name, r, err)
		}
	}

	out, err := call(`{"action":"accept","agent":"` + alpha.ID + `"}`)
	if err != nil || out.Revision.State != store.RevisionApplied || len(out.Checks) != 2 {
		t.Fatalf("accept alpha = %+v %v", out, err)
	}
	runner.set(runResults("passed", "passed"))
	target, err := e.beginDelegationVerification(x.lead, "verification.run")
	if err != nil || target == nil {
		t.Fatalf("begin verification = %v %v", target, err)
	}
	if err = e.finishDelegationVerification(x.lead, "verification.run", target, runner.out); err != nil {
		t.Fatal(err)
	}

	before := delegationGit(t, root, "write-tree")
	out, err = call(`{"action":"accept","agent":"` + beta.ID + `"}`)
	if err != nil {
		t.Fatalf("accept beta: %v", err)
	}
	wantConflict := `Conflict in a.go. Nothing was applied. Options: agent.integrate {action:"drop", agent, reason}; or fix the files in the project checkout, stage them, then agent.integrate {action:"resolved", agent}. A new writer agent works on the task base, not on the integrated state, so it would hit the same conflict: to redo the work on top of the integrated state, finish this task (drop or resolve, verify, complete) and start a new delegated task.`
	if out.Message != wantConflict || out.Revision.State != store.RevisionConflict || !slices.Equal(out.Options, []string{"drop", "resolved"}) {
		t.Fatalf("accept beta = %+v", out)
	}
	if after := delegationGit(t, root, "write-tree"); after != before {
		t.Fatalf("parent index changed by the conflict: %s -> %s", before, after)
	}
	if out, err = call(`{"action":"drop","agent":"` + beta.ID + `","reason":"conflicts with alpha; alpha's change is the one we want"}`); err != nil || out.Revision.State != store.RevisionDropped {
		t.Fatalf("drop beta = %+v %v", out, err)
	}
	if out, err = call(`{"action":"complete"}`); err != nil || out.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v %v", out, err)
	}

	if head := delegationGit(t, root, "rev-parse", "HEAD"); head != baseHead {
		t.Fatalf("parent HEAD moved: %s -> %s", baseHead, head)
	}
	if staged := delegationGit(t, root, "show", ":a.go"); staged != strings.TrimSpace(fromAlpha) {
		t.Fatalf("staged a.go = %q", staged)
	}
	if data, err := os.ReadFile(filepath.Join(root, "a.go")); err != nil || string(data) != fromAlpha {
		t.Fatalf("parent a.go = %q %v", data, err)
	}
	if st := delegationGit(t, root, "status", "--porcelain"); st != "M  a.go" {
		t.Fatalf("parent status = %q", st)
	}
	got, err := e.Store.AgentTask(t.Context(), task.ID)
	if err != nil || got.State != protocol.AgentStateCompleted {
		t.Fatalf("task = %+v %v", got, err)
	}
	if !sub.completedUpdate(task.ID) {
		t.Fatal("no delegation update published for the completed task")
	}
	// beta's dropped work is retained in its checkout.
	if data, err := os.ReadFile(filepath.Join(x.workspaceRoot(t, beta.ID), "a.go")); err != nil || string(data) != fromBeta {
		t.Fatalf("beta checkout a.go = %q %v", data, err)
	}
}

func TestRunnerRecordsFinishedWriterRevision(t *testing.T) {
	env := runtimeRunnerFixture(t)
	env.p.script = func(n int, ctx context.Context, _ llm.Request) []llm.Event {
		id, _ := runtimeAgentFrom(ctx)
		if a, _ := env.e.Store.RuntimeAgent(context.WithoutCancel(ctx), env.task.ID, id); a.Name == "writer" && n == 1 {
			return toolReply("w1", "file.write", `{"path":"new.txt","content":"agent"}`)
		}
		return textReply("done")
	}
	a := env.newRuntimeAgent(t, "writer", writeCaps, "")
	if res, err := env.run(t, a); err != nil {
		t.Fatalf("run: %v (%+v)", err, res)
	}
	r, err := env.e.Store.AgentRevision(t.Context(), env.task.ID, a.ID)
	if err != nil || r.State != store.RevisionSubmitted || !slices.Equal(r.ChangedPaths, []string{"new.txt"}) {
		t.Fatalf("writer revision = %+v %v", r, err)
	}
	// A writer without changes and a reader record nothing, and do not fail.
	for _, caps := range []protocol.AgentCapabilities{writeCaps, readOnlyCaps} {
		b := env.newRuntimeAgent(t, "idle-"+string(caps.Files), caps, "")
		if res, err := env.run(t, b); err != nil {
			t.Fatalf("run %s: %v (%+v)", b.Name, err, res)
		}
		if r, err := env.e.Store.AgentRevision(t.Context(), env.task.ID, b.ID); err == nil {
			t.Fatalf("%s recorded a revision: %+v", b.Name, r)
		}
	}
}

// failingRecorder is the real resolver whose revision recording fails.
type failingRecorder struct{ managedChildWorkspaces }

func (failingRecorder) RecordRuntimeRevision(context.Context, protocol.AgentTask, protocol.RuntimeAgent, ChildWorkspace) error {
	return fmt.Errorf("injected record failure: %w", store.ErrAgentTransition)
}

func TestRunnerFailsWriterWhenRevisionNotRecorded(t *testing.T) {
	env := runtimeRunnerFixture(t)
	env.e.RuntimeWorkspaces = failingRecorder{managedChildWorkspaces{service: env.e.DelegationWorkspaceService}}
	env.p.script = func(n int, _ context.Context, _ llm.Request) []llm.Event {
		if n == 1 {
			return toolReply("w1", "file.write", `{"path":"new.txt","content":"agent"}`)
		}
		return textReply("done")
	}
	a := env.newRuntimeAgent(t, "writer", writeCaps, "")
	res, err := env.run(t, a)
	if err == nil || !strings.Contains(res.Blocker, "revision not recorded") || !strings.Contains(res.Blocker, "one revision per agent") {
		t.Fatalf("run = %+v %v, want a blocker", res, err)
	}
}
