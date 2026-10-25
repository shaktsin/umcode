package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
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
	want := []string{"agent.cancel", "agent.list", "agent.message", "agent.spawn", "agent.status", "agent.wait"}
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

func TestRuntimeV2SpawnFailsClearlyWithoutExecutionAdapter(t *testing.T) {
	e, th, root, p := delegationFixtureWith(t, runtimeV2On)
	activation, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken})
	task, err := e.Store.AgentTask(t.Context(), p.last())
	if err != nil {
		t.Fatal(err)
	}
	plan := protocol.AgentPlan{ID: "plan-rtv2", ThreadID: th.ID, State: protocol.PlanProposed, Limits: protocol.DefaultPlanLimits(),
		Agents: []protocol.PlanAgent{{Name: "worker", Purpose: "investigate", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}}}}
	if err = e.Store.SaveAgentPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.ApproveAgentPlan(t.Context(), plan.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	tool, ok := e.Tools.Get("agent.spawn")
	if !ok {
		t.Fatal("missing agent.spawn")
	}
	admitted := tools.WithScope(withDelegationTask(t.Context(), task), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
	out, err := tool.Call(tools.WithAgentCallID(admitted, "call-rtv2"), json.RawMessage(`{"planEntry":"worker","assignment":{"prompt":"look around"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var spawned struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal([]byte(out), &spawned); err != nil || spawned.ID == "" {
		t.Fatalf("spawn result %q: %v", out, err)
	}
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		a, err := e.Store.RuntimeAgent(t.Context(), task.ID, spawned.ID)
		if err != nil {
			t.Fatal(err)
		}
		if a.State == protocol.AgentStateFailed {
			if !strings.Contains(a.Result.Blocker, "execution adapter pending") {
				t.Fatalf("blocker = %q", a.Result.Blocker)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("agent did not fail: state %q", a.State)
		case <-tick.C:
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
	turn := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken})
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

func TestRuntimeCallerFailsClosedForChildContexts(t *testing.T) {
	ctx := t.Context()
	if id, ok := runtimeCaller(ctx); !ok || id != "" {
		t.Fatalf("lead ctx: id=%q ok=%v", id, ok)
	}
	for name, child := range map[string]context.Context{
		"delegation child": WithDelegationChild(ctx),
		"child execution":  withChildExecution(ctx, &childExecution{}),
	} {
		if id, ok := runtimeCaller(child); ok {
			t.Fatalf("%s resolved as caller %q", name, id)
		}
	}
}
