package engine

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
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
