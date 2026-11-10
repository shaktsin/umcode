package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/router"
	"github.com/shaktsin/umcode/internal/store"
)

// A chat that runs on a model pool must hand that pool to its agents. The
// automatic (prompt-first) plan used to freeze only the lead's concrete
// route, without the pool and without the keys that can serve it, so every
// agent route was filtered out and each spawn failed with "no model is
// available" no matter how healthy the pool was.

// childRoutes resolves a runtime agent's selection from a frozen scope the way
// the agent runner does (runtimeAgent): the first pool model it can use first,
// the rest as failover.
func childRoutes(t *testing.T, x *activationE2E, scope store.AgentModelScope) error {
	t.Helper()
	scope = enabledModelScope(scope)
	needs := agentNeeds{Tools: true}
	pool := x.e.runtimeModelPool(t.Context(), scope, needs)
	actual, _, err := chooseRuntimeModel("", pool, needs)
	if err != nil {
		return err
	}
	pick := candidateSet{explicit: true, poolID: scope.PoolID, strategy: protocol.PoolPriority}
	for _, m := range pool {
		if m.name() == actual {
			pick.provider, pick.model = m.Provider, m.Model
		}
	}
	for _, c := range scope.Candidates {
		pick.candidates = append(pick.candidates, c)
	}
	if len(pick.candidates) == 0 {
		pick.candidates = []protocol.ConfiguredModel{{Provider: pick.provider, Model: pick.model, Enabled: true}}
	}
	child := &childExecution{selection: pick, credentialID: scope.CredentialID, credentialIDs: scope.CredentialIDs,
		runtime: &runtimeExecution{needs: needs}}
	_, err = x.e.resolveSelection(withChildExecution(context.Background(), child), x.th, protocol.ModelSelection{}, "do your part", 0)
	return err
}

func TestPromptFirstPlanFreezesChatPoolForAgents(t *testing.T) {
	x := newActivationE2E(t, nil)
	x.setLead("Use two agents", e2eLeadStep{"agent.propose_plan", onePlan})
	x.turn(t, protocol.TurnStartParams{Text: "Use two agents",
		Override: protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "e2e-pool", Complexity: protocol.ComplexityHigh}})

	task, err := x.e.Store.ActiveAgentTask(t.Context(), x.th.ID, x.th.ProjectID)
	if err != nil {
		t.Fatalf("agent task did not start: %v", err)
	}
	scope, err := x.e.Store.AgentModelScope(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if scope.PoolID != "e2e-pool" || len(scope.Candidates) != 2 {
		t.Fatalf("frozen scope lost the chat pool: %+v", scope)
	}
	if len(scope.CredentialIDs) == 0 {
		t.Fatalf("frozen scope has no approved keys, so every agent route is dropped: %+v", scope)
	}
	if err := childRoutes(t, x, scope); err != nil {
		t.Fatalf("an agent of the task cannot route on the chat's pool: %v", err)
	}
}

// Tasks admitted before the fix froze no keys. They must keep working, not
// fail every spawn: an empty key list restricts nothing (as in
// runtimeModelPool).
func TestChildRoutesWithScopeFrozenWithoutKeys(t *testing.T) {
	x := newActivationE2E(t, nil)
	legacy := store.AgentModelScope{Provider: "claude", Model: "deterministic"}
	if err := childRoutes(t, x, legacy); err != nil {
		if errors.Is(err, router.ErrNoRoute) {
			t.Fatalf("legacy scope without keys leaves agents with no model: %v", err)
		}
		t.Fatal(err)
	}
}

// The agent an automatic plan starts runs before the lead's follow-up turn,
// which is what used to register the task's shared budget, so it could fail
// with "task budget unavailable". The budget must exist before that agent
// starts.
func TestPromptFirstPlanFirstAgentRunsWithTaskBudget(t *testing.T) {
	x := newActivationE2E(t, nil)
	x.setLead("Use two agents", e2eLeadStep{"agent.propose_plan", onePlan})
	x.setAgent("worker", fakeStep{Text: "worker finished"})
	x.turn(t, protocol.TurnStartParams{Text: "Use two agents",
		Override: protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "e2e-pool", Complexity: protocol.ComplexityHigh}})

	task, err := x.e.Store.ActiveAgentTask(t.Context(), x.th.ID, x.th.ProjectID)
	if err != nil {
		t.Fatalf("agent task did not start: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		agents, err := x.e.Store.RuntimeAgents(t.Context(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range agents {
			switch a.State {
			case protocol.AgentStateCompleted:
				return
			case protocol.AgentStateFailed:
				t.Fatalf("agent %s failed: %+v", a.Name, a.Result)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the automatically started agent never finished")
}

// An agent task is not capped unless models.agent_task_limits sets a cap:
// the task's shared budget has no limits and never reports a stop, however
// much the task has spent. A configured cap is applied.
func TestAgentTaskBudgetIsUncappedByDefault(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	task := plannedTask(t, e, th)
	_, shared, err := e.DelegationCoordinator.Task(t.Context(), task, e.agentTaskLimits())
	if err != nil {
		t.Fatal(err)
	}
	settle, err := shared.Reserve(t.Context(), "big", protocol.UsageTotals{})
	if err != nil {
		t.Fatal(err)
	}
	settle(protocol.UsageTotals{InputTokens: 900_000_000, CostUSD: 100_000})
	if reason := shared.StopReason(); reason != "" {
		t.Fatalf("an uncapped task stopped: %s", reason)
	}
	if limits, _, err := e.Store.ReadAgentTaskBudget(t.Context(), task.ID); err != nil || limits != (protocol.ExecutionLimits{}) {
		t.Fatalf("stored task limits = %+v, %v; want none", limits, err)
	}
}

func TestAgentTaskBudgetUsesConfiguredCap(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	e.Cfg.Models.AgentTaskLimits.MaxTokens = 50_000
	task := plannedTask(t, e, th)
	_, shared, err := e.DelegationCoordinator.Task(t.Context(), task, e.agentTaskLimits())
	if err != nil {
		t.Fatal(err)
	}
	settle, err := shared.Reserve(t.Context(), "big", protocol.UsageTotals{})
	if err != nil {
		t.Fatal(err)
	}
	settle(protocol.UsageTotals{InputTokens: 60_000})
	if shared.StopReason() == "" {
		t.Fatal("the configured task cap did not stop the task")
	}
}
