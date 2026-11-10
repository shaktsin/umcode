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

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// fakeStep is one scripted model call of the agent spawned from plan entry
// Agent. Steps are consumed in order per agent; an agent past its steps
// answers "done". A step calls Tool with Args, or answers Text, or (Block)
// holds the provider call open until the agent's context ends. Usage, when
// Reported, is the usage the step's reply reports.
type fakeStep struct {
	Agent      string
	Tool, Args string
	Text       string
	Block      bool
	Usage      llm.Usage
}

// runtimeE2E drives the real Runtime, scheduler, runtimeRunner and engine
// turn loop with only the LLM provider, the sandbox and the compute guest
// faked. The test plays the lead through the registered agent.* tools.
type runtimeE2E struct {
	runtimeRunnerEnv
	lead    context.Context
	entered chan string // plan entry names whose blocked step was reached

	mu    sync.Mutex
	calls map[string]int
}

const e2eBase = "package x\n\nfunc F() int { return 1 }\n"

// newRuntimeE2E builds an engine over a git project whose
// base holds x.go, an approved plan with scout {read, read-only} and fixer
// {read-write, read-write} (plus helper {read, none}), fake confinement
// signals so shells are offered, and the scripted model.
func newRuntimeE2E(t *testing.T, script ...fakeStep) *runtimeE2E {
	t.Helper()
	return newRuntimeE2EWith(t, nil, script...)
}

// newRuntimeE2EWith is newRuntimeE2E with base, when set, adding more files
// to the project root before the base commit.
func newRuntimeE2EWith(t *testing.T, base func(root string), script ...fakeStep) *runtimeE2E {
	t.Helper()
	return newRuntimeE2EOpts(t, runtimeE2EOpts{base: base}, script...)
}

// runtimeE2EOpts customizes newRuntimeE2EOpts: base adds files before the
// base commit, configure adjusts the config,
// limits and entries replace the default plan (maxConcurrent 2; scout,
// fixer, helper).
type runtimeE2EOpts struct {
	base      func(root string)
	configure func(*config.Config)
	limits    *protocol.PlanLimits
	entries   []protocol.PlanAgent
}

func newRuntimeE2EOpts(t *testing.T, o runtimeE2EOpts, script ...fakeStep) *runtimeE2E {
	t.Helper()
	e, th, root, _ := delegationFixtureWith(t, func(c *config.Config) {
		if o.configure != nil {
			o.configure(c)
		}
	})
	base := o.base
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte(e2eBase), 0o644); err != nil {
		t.Fatal(err)
	}
	if base != nil {
		base(root)
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "x.go")
	task := childExecutionTask(t, e, th)
	p := &runtimeScriptProvider{}
	e.LLMs.Register(p)
	(&shellSeams{}).install(e, fakeSandbox{}, nil)
	env := runtimeRunnerEnv{e: e, th: th, parent: root, task: task, p: p}
	limits := protocol.DefaultPlanLimits()
	limits.MaxConcurrent = 2
	if o.limits != nil {
		limits = *o.limits
	}
	entries := o.entries
	if entries == nil {
		entries = []protocol.PlanAgent{
			{Name: "scout", Purpose: "read the code", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadOnly}},
			{Name: "fixer", Purpose: "fix the code", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite}},
			{Name: "helper", Purpose: "wait around", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}},
		}
	}
	env.approvePlan(t, limits, entries...)
	x := &runtimeE2E{runtimeRunnerEnv: env, entered: make(chan string, 8), calls: map[string]int{}}
	x.lead = env.agentToolCtx(t, "")
	byAgent := map[string][]fakeStep{}
	for _, s := range script {
		byAgent[s.Agent] = append(byAgent[s.Agent], s)
	}
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
		steps := byAgent[a.PlanEntry]
		if idx >= len(steps) {
			return textReply("done")
		}
		var reply []llm.Event
		switch step := steps[idx]; {
		case step.Block:
			x.entered <- a.PlanEntry
			<-ctx.Done()
			return []llm.Event{{Type: llm.EventError, Err: ctx.Err()}}
		case step.Tool != "":
			reply = toolReply(fmt.Sprintf("%s-%d", a.PlanEntry, idx), step.Tool, step.Args)
		default:
			reply = textReply(step.Text)
		}
		if steps[idx].Usage.Reported {
			reply[len(reply)-1].Usage = steps[idx].Usage
		}
		return reply
	}
	return x
}

// spawn plays the lead's agent.spawn call and returns the new agent's ID.
func (x *runtimeE2E) spawn(t *testing.T, entry string) string {
	t.Helper()
	out, err := callAgentTool(t, x.e, x.lead, "agent.spawn", "spawn-"+entry, `{"name":"`+entry+`","planEntry":"`+entry+`","assignment":{"prompt":"do your part"}}`)
	if err != nil {
		t.Fatalf("spawn %s: %v", entry, err)
	}
	var res struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal([]byte(out), &res); err != nil || res.ID == "" {
		t.Fatalf("spawn %s output %q: %v", entry, out, err)
	}
	return res.ID
}

func (x *runtimeE2E) agent(t *testing.T, id string) protocol.RuntimeAgent {
	t.Helper()
	a, err := x.e.Store.RuntimeAgent(t.Context(), x.task.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func requestToolNames(req llm.Request) []string {
	var names []string
	for _, spec := range req.Tools {
		names = append(names, tools.FromWire(spec.Name))
	}
	return names
}

func TestE2ELeadSpawnsReaderAndWriter(t *testing.T) {
	const fixed = "package x\n\nfunc F() int { return 2 }\n"
	x := newRuntimeE2E(t,
		fakeStep{Agent: "scout", Tool: "file.read", Args: `{"path":"x.go"}`},
		fakeStep{Agent: "scout", Text: "scout report: F returns 1"},
		fakeStep{Agent: "fixer", Tool: "file.write", Args: `{"path":"x.go","content":` + jsonString(fixed) + `}`},
		fakeStep{Agent: "fixer", Text: "fixer report: F now returns 2"},
	)
	scoutID, fixerID := x.spawn(t, "scout"), x.spawn(t, "fixer")

	ctx, cancel := context.WithTimeout(x.lead, 60*time.Second)
	defer cancel()
	if _, err := callAgentTool(t, x.e, ctx, "agent.wait", "wait", `{"ids":["`+scoutID+`","`+fixerID+`"],"mode":"all"}`); err != nil {
		t.Fatal(err)
	}

	scout, fixer := x.agent(t, scoutID), x.agent(t, fixerID)
	for _, a := range []protocol.RuntimeAgent{scout, fixer} {
		if a.State != protocol.AgentStateCompleted {
			t.Fatalf("%s is %s (%+v)", a.Name, a.State, a.Result)
		}
	}
	if scout.Capabilities.Shell != protocol.ShellReadOnly || fixer.Capabilities.Shell != protocol.ShellReadWrite || len(scout.Rejected) != 0 {
		t.Fatalf("shell grants through confinement resolution: scout %+v (rejected %+v) fixer %+v", scout.Capabilities, scout.Rejected, fixer.Capabilities)
	}
	if len(scout.Result.ChangedPaths) != 0 || !strings.Contains(scout.Result.Summary, "F returns 1") {
		t.Fatalf("scout result = %+v", scout.Result)
	}
	if !slices.Equal(fixer.Result.ChangedPaths, []string{"x.go"}) || !strings.Contains(fixer.Result.Summary, "returns 2") {
		t.Fatalf("fixer result = %+v", fixer.Result)
	}

	// The fixer's edit lives in its own checkout; the project root is untouched.
	if data, err := os.ReadFile(filepath.Join(x.workspaceRoot(t, fixerID), "x.go")); err != nil || string(data) != fixed {
		t.Fatalf("fixer checkout x.go = %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(x.parent, "x.go")); err != nil || string(data) != e2eBase {
		t.Fatalf("project root x.go changed: %q %v", data, err)
	}
	if out := delegationGit(t, x.parent, "status", "--porcelain"); out != "" {
		t.Fatalf("project root not clean: %s", out)
	}

	var total float64
	for _, a := range []protocol.RuntimeAgent{scout, fixer} {
		total += a.BudgetShare
	}
	if total > 1.0+1e-9 || total <= 0 {
		t.Fatalf("budget shares sum to %v", total)
	}

	// Each agent saw only the tools its capabilities allow, shells included.
	reqs, ids := x.p.snapshot()
	seen := map[string][]string{}
	for i, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = requestToolNames(reqs[i])
		}
	}
	if got := seen[scoutID]; !slices.Contains(got, "shell.run") || !slices.Contains(got, "file.read") || slices.Contains(got, "file.write") || slices.Contains(got, "file.edit") {
		t.Fatalf("scout tools = %v", got)
	}
	if got := seen[fixerID]; !slices.Contains(got, "shell.run") || !slices.Contains(got, "file.write") {
		t.Fatalf("fixer tools = %v", got)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestE2EStopCancelsEverything(t *testing.T) {
	x := newRuntimeE2E(t,
		fakeStep{Agent: "scout", Block: true},
		fakeStep{Agent: "fixer", Block: true},
	)
	ids := []string{x.spawn(t, "scout"), x.spawn(t, "fixer"), x.spawn(t, "helper")} // maxConcurrent 2: the helper queues
	for range 2 {
		select {
		case <-x.entered:
		case <-time.After(30 * time.Second):
			t.Fatal("agents never reached the model")
		}
	}
	if st := x.agent(t, ids[2]).State; st != protocol.AgentStateQueued {
		t.Fatalf("helper = %s, want queued behind the two running agents", st)
	}

	start := time.Now()
	if _, err := x.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: x.th.ID, TaskID: x.task.ID}); err != nil {
		t.Fatal(err)
	}
	for {
		var live []string
		for _, id := range ids {
			if a := x.agent(t, id); !isTerminalAgent(a.State) {
				live = append(live, a.Name+"="+string(a.State))
			}
		}
		if len(live) == 0 {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("agents still live 2s after the lead cancelled: %v", live)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, id := range ids {
		if a := x.agent(t, id); a.State != protocol.AgentStateCancelled {
			t.Fatalf("%s ended %s, want cancelled", a.Name, a.State)
		}
	}
	// Nothing keeps running: no agent turn is left and the helper never ran.
	deadline := time.Now().Add(2 * time.Second)
	for {
		x.e.mu.Lock()
		live := len(x.e.activeTurns)
		x.e.mu.Unlock()
		if live == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d agent turns still active", live)
		}
		time.Sleep(10 * time.Millisecond)
	}
	x.mu.Lock()
	ranHelper := x.calls["helper"]
	x.mu.Unlock()
	if ranHelper != 0 {
		t.Fatal("the queued helper reached the model")
	}
}

// waitAgentState polls the store until agent id reaches want.
func waitAgentState(t *testing.T, e *Engine, taskID, id string, want protocol.AgentState) protocol.RuntimeAgent {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		a, err := e.Store.RuntimeAgent(t.Context(), taskID, id)
		if err != nil {
			t.Fatal(err)
		}
		if a.State == want {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent %s is %s (%+v), want %s", a.Name, a.State, a.Result, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isTerminalAgent(st protocol.AgentState) bool {
	switch st {
	case protocol.AgentStateCompleted, protocol.AgentStateFailed, protocol.AgentStateCancelled, protocol.AgentStateInterrupted:
		return true
	}
	return false
}
