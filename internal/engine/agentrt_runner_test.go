package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/sandbox"
	"github.com/shaktsin/umcode/internal/tools"
)

// runtimeScriptProvider is a fake LLM provider for runtime agent turns. It
// records each request, the runtime agent ID on the request context, and the
// agent's persisted model fields at call time.
type runtimeScriptProvider struct {
	mu       sync.Mutex
	requests []llm.Request
	agents   []string
	script   func(n int, ctx context.Context, req llm.Request) []llm.Event
}

func (*runtimeScriptProvider) ID() string { return "claude" }
func (*runtimeScriptProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return nil, nil
}
func (p *runtimeScriptProvider) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	id, _ := runtimeAgentFrom(ctx)
	p.agents = append(p.agents, id)
	n := len(p.requests)
	p.mu.Unlock()
	events := []llm.Event{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventDone}}
	if p.script != nil {
		events = p.script(n, ctx, req)
	}
	ch := make(chan llm.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}
func (p *runtimeScriptProvider) snapshot() ([]llm.Request, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.requests...), append([]string(nil), p.agents...)
}

func textReply(text string) []llm.Event {
	return []llm.Event{{Type: llm.EventTextDelta, Text: text}, {Type: llm.EventDone}}
}
func toolReply(id, name, args string) []llm.Event {
	return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: id, Name: tools.ToWire(name), Args: json.RawMessage(args)}}, {Type: llm.EventDone}}
}

type runtimeRunnerEnv struct {
	e      *Engine
	th     protocol.Thread
	parent string
	task   protocol.AgentTask
	p      *runtimeScriptProvider
}

func runtimeRunnerFixture(t *testing.T) runtimeRunnerEnv {
	t.Helper()
	e, th, root, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	p := &runtimeScriptProvider{}
	e.LLMs.Register(p)
	return runtimeRunnerEnv{e: e, th: th, parent: root, task: task, p: p}
}

// newRuntimeAgent stores a runtime agent and moves it to running, as the
// scheduler does before it calls the runner.
func (env runtimeRunnerEnv) newRuntimeAgent(t *testing.T, name string, caps protocol.AgentCapabilities, model string) protocol.RuntimeAgent {
	t.Helper()
	ctx := t.Context()
	a, err := env.e.Store.CreateRuntimeAgent(ctx, protocol.RuntimeAgent{TaskID: env.task.ID, Name: name, Purpose: "Investigate the parser module",
		Assignment: protocol.AgentAssignment{Prompt: "Look at the parser and report", AcceptanceChecks: []string{"report findings"}}, Capabilities: caps, ModelRequested: model, BudgetShare: 0.5}, "call-"+name)
	if err != nil {
		t.Fatal(err)
	}
	a.State = protocol.AgentStateRunning
	if err = env.e.Store.UpdateRuntimeAgent(ctx, a, a.Version); err != nil {
		t.Fatal(err)
	}
	a, err = env.e.Store.RuntimeAgent(ctx, env.task.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (env runtimeRunnerEnv) run(t *testing.T, a protocol.RuntimeAgent) (protocol.AgentResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	return runtimeRunner{engine: env.e}.Run(ctx, a)
}

func (env runtimeRunnerEnv) workspaceRoot(t *testing.T, agentID string) string {
	t.Helper()
	a, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, agentID)
	if err != nil || a.WorkspaceID == "" {
		t.Fatalf("agent workspace not recorded: %+v %v", a, err)
	}
	w, err := env.e.Store.AgentWorkspace(t.Context(), a.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	return w.Root
}

var readOnlyCaps = protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}
var writeCaps = protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellNone}

func TestRunnerMapsFinalMessageToResult(t *testing.T) {
	t.Run("read-write", func(t *testing.T) {
		env := runtimeRunnerFixture(t)
		env.p.script = func(n int, _ context.Context, _ llm.Request) []llm.Event {
			if n == 1 {
				return toolReply("w1", "file.write", `{"path":"new.txt","content":"agent"}`)
			}
			return textReply("all done")
		}
		a := env.newRuntimeAgent(t, "writer", writeCaps, "")
		res, err := env.run(t, a)
		if err != nil {
			t.Fatalf("run: %v (result %+v)", err, res)
		}
		if res.Summary != "all done" || res.Blocker != "" || res.Review != nil {
			t.Fatalf("result = %+v", res)
		}
		if !slices.Equal(res.ChangedPaths, []string{"new.txt"}) {
			t.Fatalf("changed paths = %v", res.ChangedPaths)
		}
		if _, err := os.Stat(filepath.Join(env.parent, "new.txt")); err == nil {
			t.Fatal("agent write reached the parent checkout")
		}
		root := env.workspaceRoot(t, a.ID)
		if data, err := os.ReadFile(filepath.Join(root, "new.txt")); err != nil || string(data) != "agent" {
			t.Fatalf("isolated write missing: %q %v", data, err)
		}
		got, _ := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, a.ID)
		if got.Revision == "" || got.Revision == env.task.BaseRevision {
			t.Fatalf("revision not recorded: %+v", got)
		}
		_, agents := env.p.snapshot()
		for _, id := range agents {
			if id != a.ID {
				t.Fatalf("agent identity on turn context = %q, want %q", id, a.ID)
			}
		}
	})
	t.Run("read-only", func(t *testing.T) {
		env := runtimeRunnerFixture(t)
		env.p.script = func(int, context.Context, llm.Request) []llm.Event { return textReply("looked around") }
		a := env.newRuntimeAgent(t, "reader", readOnlyCaps, "")
		res, err := env.run(t, a)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if res.Summary != "looked around" || len(res.ChangedPaths) != 0 {
			t.Fatalf("result = %+v", res)
		}
		got, _ := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, a.ID)
		if got.WorkspaceID == "" || got.Revision != env.task.BaseRevision {
			t.Fatalf("read-only workspace = %q revision %q, want base %q", got.WorkspaceID, got.Revision, env.task.BaseRevision)
		}
		root := env.workspaceRoot(t, a.ID)
		if root == env.parent || strings.HasPrefix(root, env.parent+string(os.PathSeparator)) {
			t.Fatalf("read-only agent ran in the parent checkout %s", root)
		}
		reqs, _ := env.p.snapshot()
		if len(reqs) == 0 || !strings.Contains(reqs[0].System, "Investigate the parser module") || !strings.Contains(reqs[0].System, "You can read files but not change them.") {
			t.Fatalf("system prompt = %q", reqs[0].System)
		}
	})
}

func TestRunnerRecordsModelFallback(t *testing.T) {
	env := runtimeRunnerFixture(t)
	var atCall protocol.RuntimeAgent
	a := env.newRuntimeAgent(t, "fallback", readOnlyCaps, "missing-model")
	env.p.script = func(n int, _ context.Context, _ llm.Request) []llm.Event {
		if n == 1 {
			atCall, _ = env.e.Store.RuntimeAgent(context.Background(), env.task.ID, a.ID)
		}
		return textReply("ok")
	}
	if _, err := env.run(t, a); err != nil {
		t.Fatal(err)
	}
	if atCall.ModelActual != "claude/deterministic" || len(atCall.RouteTrail) == 0 {
		t.Fatalf("model fields not persisted before the first model call: %+v", atCall)
	}
	got, _ := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, a.ID)
	if got.ModelRequested != "missing-model" || got.ModelActual != "claude/deterministic" {
		t.Fatalf("requested %q actual %q", got.ModelRequested, got.ModelActual)
	}
	if !strings.Contains(strings.Join(got.RouteTrail, "\n"), "missing-model") {
		t.Fatalf("route trail does not explain the skipped model: %v", got.RouteTrail)
	}
	reqs, _ := env.p.snapshot()
	if len(reqs) == 0 || reqs[0].Model != "deterministic" {
		t.Fatalf("requests %+v", reqs)
	}
}

func TestChooseRuntimeModel(t *testing.T) {
	pool := []modelRoute{
		{Provider: "claude", Model: "a", Tools: false},
		{Provider: "claude", Model: "b", Unavailable: "cooling down"},
		{ID: "cfg-c", Provider: "claude", Model: "c", Tools: true},
		{Provider: "claude", Model: "d", Tools: true},
	}
	actual, trail, err := chooseRuntimeModel("claude/d", pool, agentNeeds{Tools: true})
	if err != nil || actual != "claude/d" || len(trail) != 1 {
		t.Fatalf("requested compatible: %q %v %v", actual, trail, err)
	}
	actual, trail, err = chooseRuntimeModel("a", pool, agentNeeds{Tools: true})
	if err != nil || actual != "claude/c" {
		t.Fatalf("fallback: %q %v %v", actual, trail, err)
	}
	joined := strings.Join(trail, "\n")
	if !strings.Contains(joined, "claude/a: cannot use tools") || !strings.Contains(joined, "claude/b: cooling down") {
		t.Fatalf("trail = %v", trail)
	}
	if actual, _, err = chooseRuntimeModel("cfg-c", pool, agentNeeds{Tools: true}); err != nil || actual != "claude/c" {
		t.Fatalf("configured ID: %q %v", actual, err)
	}
	if actual, _, err = chooseRuntimeModel("", pool, agentNeeds{}); err != nil || actual != "claude/a" {
		t.Fatalf("no tools needed: %q %v", actual, err)
	}
	_, trail, err = chooseRuntimeModel("x", pool[:2], agentNeeds{Tools: true})
	if err == nil || !strings.Contains(err.Error(), `"x"`) || len(trail) != 3 {
		t.Fatalf("none compatible: %v %v", trail, err)
	}
	if got := agentNeedsFor(protocol.AgentCapabilities{Files: protocol.FilesNone, Shell: protocol.ShellNone}); got.Tools {
		t.Fatal("no capability needs no tools")
	}
	if got := agentNeedsFor(readOnlyCaps); !got.Tools {
		t.Fatal("file access needs tools")
	}
}

func TestRunnerNoCompatibleModelFails(t *testing.T) {
	env := runtimeRunnerFixture(t)
	creds, err := env.e.Creds.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range creds {
		if err = env.e.Creds.Delete(t.Context(), c.ID); err != nil {
			t.Fatal(err)
		}
	}
	a := env.newRuntimeAgent(t, "nomodel", readOnlyCaps, "wanted-model")
	_, err = env.run(t, a)
	if err == nil || !strings.Contains(err.Error(), "wanted-model") || !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("err = %v", err)
	}
	if reqs, _ := env.p.snapshot(); len(reqs) != 0 {
		t.Fatalf("a model was called: %d", len(reqs))
	}
}

// runtimeFor starts an agent runtime over the real runner with an approved
// one-entry plan for env's task.
func (env runtimeRunnerEnv) runtimeFor(t *testing.T, base context.Context, caps protocol.AgentCapabilities) *agentrt.Runtime {
	t.Helper()
	plan := protocol.AgentPlan{ID: "plan-" + env.task.ID, ThreadID: env.th.ID, Limits: protocol.DefaultPlanLimits(),
		Agents: []protocol.PlanAgent{{Name: "worker", Purpose: "investigate", Capabilities: caps}}}
	if err := env.e.Store.SaveAgentPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if err := env.e.Store.ApproveAgentPlan(t.Context(), plan.ID, env.task.ID); err != nil {
		t.Fatal(err)
	}
	return agentrt.NewRuntime(env.e.Store, runtimeRunner{engine: env.e}, base)
}

func waitAgentTerminal(t *testing.T, rt *agentrt.Runtime, taskID, id string) protocol.RuntimeAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	got, err := rt.Wait(ctx, taskID, "", []string{id}, agentrt.WaitAll)
	if err != nil {
		t.Fatal(err)
	}
	return got[0]
}

func TestRunnerSliceExhaustionStopsAgent(t *testing.T) {
	env := runtimeRunnerFixture(t)
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 10, MaxCostUSD: 1, MaxDurationMinutes: 10}, protocol.UsageTotals{}, nil)
	rt := env.runtimeFor(t, delegation.WithBudget(t.Context(), shared), readOnlyCaps)
	spawned, err := rt.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "slice", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	got := waitAgentTerminal(t, rt, env.task.ID, spawned.Agent.ID)
	if got.State != protocol.AgentStateFailed || got.Result.Blocker != "budget slice exhausted" {
		t.Fatalf("state %q blocker %q", got.State, got.Result.Blocker)
	}
	if reqs, _ := env.p.snapshot(); len(reqs) != 0 {
		t.Fatalf("provider called past the slice: %d", len(reqs))
	}
}

func TestRunnerCancellationEndsPromptly(t *testing.T) {
	env := runtimeRunnerFixture(t)
	entered := make(chan struct{}, 1)
	env.p.script = func(_ int, ctx context.Context, _ llm.Request) []llm.Event {
		entered <- struct{}{}
		<-ctx.Done()
		return []llm.Event{{Type: llm.EventError, Err: ctx.Err()}}
	}
	rt := env.runtimeFor(t, t.Context(), readOnlyCaps)
	spawned, err := rt.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "cancel", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("provider not entered")
	}
	start := time.Now()
	if err = rt.Cancel(t.Context(), env.task.ID, "", spawned.Agent.ID); err != nil {
		t.Fatal(err)
	}
	got := waitAgentTerminal(t, rt, env.task.ID, spawned.Agent.ID)
	if got.State != protocol.AgentStateCancelled {
		t.Fatalf("state = %q", got.State)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		env.e.mu.Lock()
		running := len(env.e.activeTurns)
		env.e.mu.Unlock()
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent turn stayed alive after cancellation")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancellation was not prompt")
	}
}

func TestRunnerReadOnlyAgentCannotWrite(t *testing.T) {
	env := runtimeRunnerFixture(t)
	env.p.script = func(n int, _ context.Context, _ llm.Request) []llm.Event {
		if n == 1 {
			// file.write is not offered to this agent; a hallucinated call must
			// still be refused at execution.
			return toolReply("w1", "file.write", `{"path":"target.txt","content":"changed"}`)
		}
		return textReply("tried")
	}
	a := env.newRuntimeAgent(t, "reader", readOnlyCaps, "")
	res, err := env.run(t, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ChangedPaths) != 0 {
		t.Fatalf("changed paths = %v", res.ChangedPaths)
	}
	root := env.workspaceRoot(t, a.ID)
	for _, dir := range []string{root, env.parent} {
		if _, err := os.Stat(filepath.Join(dir, "target.txt")); err == nil {
			t.Fatalf("read-only agent wrote %s", dir)
		}
	}
	if out, _ := exec.Command("git", "-C", root, "status", "--porcelain").Output(); len(out) != 0 {
		t.Fatalf("read-only checkout changed: %s", out)
	}
	reqs, _ := env.p.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	var refused bool
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == "w1" && m.IsError && strings.Contains(m.Result, "not granted by this agent's capabilities") {
			refused = true
		}
	}
	if !refused {
		t.Fatal("disallowed tool call was not refused to the model")
	}
}

func TestRunnerExposesOnlyGrantedTools(t *testing.T) {
	env := runtimeRunnerFixture(t)
	a := env.newRuntimeAgent(t, "reader", protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}, "")
	if _, err := env.run(t, a); err != nil {
		t.Fatal(err)
	}
	reqs, _ := env.p.snapshot()
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	var names []string
	for _, spec := range reqs[0].Tools {
		names = append(names, tools.FromWire(spec.Name))
	}
	for _, want := range []string{"file.read", "file.list", "file.search"} {
		if !slices.Contains(names, want) {
			t.Fatalf("granted tool %s not exposed: %v", want, names)
		}
	}
	for _, name := range names {
		if !runtimeToolAllowed(a.Capabilities, name) {
			t.Fatalf("tool %s exposed beyond capabilities: %v", name, names)
		}
	}
}

func TestRunnerDeliversPendingMessages(t *testing.T) {
	env := runtimeRunnerFixture(t)
	a := env.newRuntimeAgent(t, "listener", readOnlyCaps, "")
	if _, err := env.e.Store.EnqueueAgentMessage(t.Context(), protocol.AgentMessage{TaskID: env.task.ID, ToAgentID: a.ID, Body: "FIRST_MAILBOX_NOTE"}); err != nil {
		t.Fatal(err)
	}
	env.p.script = func(n int, _ context.Context, _ llm.Request) []llm.Event {
		if n == 1 {
			// Arrives while the agent works; delivered at the next boundary.
			if _, err := env.e.Store.EnqueueAgentMessage(context.Background(), protocol.AgentMessage{TaskID: env.task.ID, ToAgentID: a.ID, Body: "SECOND_MAILBOX_NOTE"}); err != nil {
				t.Error(err)
			}
		}
		return textReply("noted")
	}
	if _, err := env.run(t, a); err != nil {
		t.Fatal(err)
	}
	reqs, _ := env.p.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	count := func(req llm.Request, body string) int {
		encoded, _ := json.Marshal(req.Messages)
		return strings.Count(string(encoded), body)
	}
	if count(reqs[0], "FIRST_MAILBOX_NOTE") != 1 || count(reqs[0], "SECOND_MAILBOX_NOTE") != 0 {
		t.Fatalf("first turn input: %+v", reqs[0].Messages)
	}
	if count(reqs[1], "FIRST_MAILBOX_NOTE") != 1 || count(reqs[1], "SECOND_MAILBOX_NOTE") != 1 {
		t.Fatalf("second turn input re-delivered or missed a message: %+v", reqs[1].Messages)
	}
	pending, err := env.e.Store.PendingAgentMessages(t.Context(), env.task.ID, a.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("unacknowledged %+v %v", pending, err)
	}
}

type fakeSandbox struct{}

func (fakeSandbox) Name() string                         { return "fake" }
func (fakeSandbox) Wrap(*exec.Cmd, sandbox.Policy) error { return nil }

func TestRuntimeAgentConfinementUsesRealSignals(t *testing.T) {
	e := &Engine{}
	noVM := func() error {
		return errors.New("isolated compute is unavailable: this Linux host has no /dev/kvm device")
	}
	e.agentVMCheck, e.agentSandbox = noVM, func() sandbox.Sandbox { return fakeSandbox{} }
	if got := e.runtimeConfinement(&tools.Scope{UseCompute: true}); got != tools.ConfNone {
		t.Fatalf("toggle on, VM unavailable = %q (never fall back to the OS sandbox)", got)
	}
	if got := e.runtimeConfinement(&tools.Scope{}); got != tools.ConfOSSandbox {
		t.Fatalf("toggle off with sandbox = %q", got)
	}
	e.agentVMCheck = func() error { return nil }
	if got := e.runtimeConfinement(&tools.Scope{UseCompute: true}); got != tools.ConfMicroVM {
		t.Fatalf("toggle on, VM available = %q", got)
	}
	e.agentSandbox = func() sandbox.Sandbox { return nil }
	if got := e.runtimeConfinement(&tools.Scope{}); got != tools.ConfNone {
		t.Fatalf("no sandbox = %q", got)
	}
}

func TestRuntimeAgentScopeConfinedOnlyInIsolatedCheckout(t *testing.T) {
	e := &Engine{agentVMCheck: func() error { return errors.New("no vm") }, agentSandbox: func() sandbox.Sandbox { return fakeSandbox{} }}
	caps := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite}
	x := &childExecution{workspace: ChildWorkspace{ID: "w", Root: "/iso"}, runtime: &runtimeExecution{agent: protocol.RuntimeAgent{Capabilities: caps}, isolated: true, modeFn: func() string { return "auto_all" }}}
	s := e.runtimeAgentScope(&tools.Scope{Root: "/iso", ApprovalMode: "auto_all"}, x)
	if !s.Confined || s.ReadOnly || s.ShellReadOnly || s.ApprovalMode != "" || s.TrustedAutonomy() {
		t.Fatalf("isolated writer scope = %+v", s)
	}
	if s := e.runtimeAgentScope(&tools.Scope{Root: "/parent"}, x); s.Confined {
		t.Fatal("writer confined outside its own checkout")
	}
	x.runtime.isolated = false
	if s := e.runtimeAgentScope(&tools.Scope{Root: "/iso"}, x); s.Confined {
		t.Fatal("writer confined in a non-isolated checkout")
	}
	x.runtime.agent.Capabilities = protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadWrite}
	if s := e.runtimeAgentScope(&tools.Scope{Root: "/iso"}, x); !s.ShellReadOnly || !s.ReadOnly {
		t.Fatalf("files read + shell read-write must run a read-only shell: %+v", s)
	}
}

func TestRuntimeScopeDropsCopiedLeadMode(t *testing.T) {
	base := &tools.Scope{Root: "/w", ApprovalMode: "auto_workspace"}
	s := runtimeScope(base, protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadWrite}, tools.ConfOSSandbox, nil)
	if s.ApprovalMode != "" || s.TrustedAutonomy() {
		t.Fatalf("stale lead mode copied: %q", s.ApprovalMode)
	}
	if !s.ShellReadOnly {
		t.Fatal("shell read-write without files read-write must be read-only")
	}
	rw := runtimeScope(base, protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadOnly}, tools.ConfOSSandbox, nil)
	if !rw.ShellReadOnly {
		t.Fatal("shell read-only must stay read-only")
	}
}

func TestRuntimeAgentModeReadsLeadThreadLive(t *testing.T) {
	env := runtimeRunnerFixture(t)
	mode := env.e.leadApprovalMode(env.task)
	if got := normalizedApprovalMode(mode()); got != normalizedApprovalMode("") {
		t.Fatalf("initial mode = %q", got)
	}
	if err := env.e.Store.UpdateThread(t.Context(), env.th.ID, map[string]any{"approval_mode": "auto_workspace"}); err != nil {
		t.Fatal(err)
	}
	if got := mode(); got != "auto_workspace" {
		t.Fatalf("mode after lead change = %q", got)
	}
}

type parentRootWorkspaces struct{ root string }

func (w parentRootWorkspaces) ResolveRuntime(context.Context, protocol.AgentTask, protocol.RuntimeAgent, bool) (ChildWorkspace, error) {
	return ChildWorkspace{ID: "not-isolated", Root: w.root}, nil
}
func (w parentRootWorkspaces) FinalizeRuntime(_ context.Context, _ protocol.AgentTask, _ protocol.RuntimeAgent, ws ChildWorkspace) (ChildWorkspace, error) {
	return ws, nil
}

func TestRunnerRefusesWriterOutsideIsolatedCheckout(t *testing.T) {
	env := runtimeRunnerFixture(t)
	env.e.RuntimeWorkspaces = parentRootWorkspaces{env.parent}
	a := env.newRuntimeAgent(t, "writer", writeCaps, "")
	_, err := env.run(t, a)
	if err == nil || !strings.Contains(err.Error(), "isolated") {
		t.Fatalf("err = %v", err)
	}
	if reqs, _ := env.p.snapshot(); len(reqs) != 0 {
		t.Fatal("writer ran outside an isolated checkout")
	}
}

func TestRuntimeAgentPromptHasNoRoleNames(t *testing.T) {
	a := protocol.RuntimeAgent{ID: "agt", Name: "reviewer-bot", Purpose: "Check the parser for bugs", Assignment: protocol.AgentAssignment{Prompt: "Read parser.go", AcceptanceChecks: []string{"list issues"}},
		Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadOnly}}
	p := runtimeAgentPrompt(a, "/ws")
	for _, want := range []string{"Check the parser for bugs", "Read parser.go", "list issues", "You can read files but not change them.", "You can run commands in a read-only sandbox."} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	low := strings.ToLower(p)
	for _, banned := range []string{"reviewer", "implementer", "review_report"} {
		if strings.Contains(low, banned) {
			t.Fatalf("prompt contains %q:\n%s", banned, p)
		}
	}
	w := runtimeAgentPrompt(protocol.RuntimeAgent{Purpose: "x", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellNone, Network: true}}, "/ws")
	for _, want := range []string{"You can read and change files", "You cannot run commands.", "network"} {
		if !strings.Contains(w, want) {
			t.Fatalf("writer prompt missing %q:\n%s", want, w)
		}
	}
}
