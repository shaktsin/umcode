package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/protocol"
)

// spendTokens settles n tokens of actual usage on b (and so on every budget
// it was sliced from).
func spendTokens(t *testing.T, b *delegation.Budget, id string, n int64) error {
	t.Helper()
	settle, err := b.Reserve(context.Background(), id, protocol.UsageTotals{InputTokens: n})
	if err != nil {
		return err
	}
	settle(protocol.UsageTotals{InputTokens: n})
	return nil
}

func tokensLeft(b *delegation.Budget) int64 { t, _ := b.Remaining(); return t }

// I3 scenario A: a nested agent slices its parent agent's slice, with its
// share relative to the parent's share, so the subtree never spends more
// than the parent's slice and the child's spend is charged to the parent.
func TestNestedAgentSlicesParentAgentBudget(t *testing.T) {
	e := &Engine{}
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 1000}, protocol.UsageTotals{}, nil)
	lead := delegation.WithBudget(context.Background(), shared)
	a := protocol.RuntimeAgent{ID: "A", TaskID: "t", BudgetShare: 0.25}
	e.admitRuntimeAgentBudget(lead, a)
	sa, err := e.runtimeAgentBudget(lead, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := tokensLeft(sa); got != 250 {
		t.Fatalf("A slice = %d, want 250", got)
	}
	// G asks for 0.25 of the task: all of A's 0.25.
	g := protocol.RuntimeAgent{ID: "G", TaskID: "t", ParentAgentID: "A", BudgetShare: 0.25}
	agentCtx := delegation.WithBudget(context.Background(), sa)
	e.admitRuntimeAgentBudget(agentCtx, g)
	sg, err := e.runtimeAgentBudget(agentCtx, g)
	if err != nil {
		t.Fatal(err)
	}
	if got := tokensLeft(sg); got != 250 {
		t.Fatalf("G slice = %d, want all of A's 250", got)
	}
	if err = spendTokens(t, sg, "g1", 200); err != nil {
		t.Fatal(err)
	}
	if got := tokensLeft(sa); got != 50 {
		t.Fatalf("A's slice after G spent 200 = %d, want 50 (G charged to A)", got)
	}
	if got := tokensLeft(shared); got != 800 {
		t.Fatalf("task after G spent 200 = %d, want 800", got)
	}
	// A's slice exhaustion stops G even though the task has plenty left.
	if err = spendTokens(t, sa, "a1", 50); err != nil {
		t.Fatal(err)
	}
	if err = spendTokens(t, sg, "g2", 10); err == nil {
		t.Fatal("G spent past its parent's exhausted slice")
	}
	if sg.StopReason() == "" {
		t.Fatal("G not stopped by its parent's exhausted slice")
	}

	// A half-share grandchild of a quarter-share parent gets half its slice.
	fresh := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 800}, protocol.UsageTotals{}, nil)
	sa2 := fresh.Slice(0.25) // 200
	e.runtimeAgentBudgets.put("t", "A2", sa2, 0.25)
	h := protocol.RuntimeAgent{ID: "H", TaskID: "t", ParentAgentID: "A2", BudgetShare: 0.125}
	sh, err := e.runtimeAgentBudget(lead, h)
	if err != nil {
		t.Fatal(err)
	}
	if got := tokensLeft(sh); got != 100 {
		t.Fatalf("H slice = %d, want half of A2's 200", got)
	}
}

// I3 scenario B: shares of the lead's agents are fractions of the task budget
// remaining when they are spawned (reserved at spawn), not of what earlier
// siblings left over. Lead spawns A (0.5) and B (0.5); with maxConcurrent=1
// B runs after A spent its whole half and still gets half the original.
func TestSequentialLeadAgentsGetTheirShareOfTheOriginalRemaining(t *testing.T) {
	e := &Engine{}
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 1000}, protocol.UsageTotals{}, nil)
	lead := delegation.WithBudget(context.Background(), shared)
	a := protocol.RuntimeAgent{ID: "A", TaskID: "t", BudgetShare: 0.5}
	b := protocol.RuntimeAgent{ID: "B", TaskID: "t", BudgetShare: 0.5}
	e.admitRuntimeAgentBudget(lead, a)
	e.admitRuntimeAgentBudget(lead, b)
	sa, err := e.runtimeAgentBudget(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err = spendTokens(t, sa, "a", 500); err != nil {
		t.Fatal(err)
	}
	e.releaseRuntimeAgentBudget("t", "A")
	sb, err := e.runtimeAgentBudget(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if got := tokensLeft(sb); got != 500 {
		t.Fatalf("B slice = %d, want 500 (half of the original 1000)", got)
	}
}

// A nested agent whose parent's budget is gone (the parent is terminal and
// no slice was reserved at spawn) fails closed instead of slicing the task.
func TestNestedAgentWithoutParentBudgetFailsClosed(t *testing.T) {
	e := &Engine{}
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 1000}, protocol.UsageTotals{}, nil)
	lead := delegation.WithBudget(context.Background(), shared)
	g := protocol.RuntimeAgent{ID: "G", TaskID: "t", ParentAgentID: "A", BudgetShare: 0.25}
	if _, err := e.runtimeAgentBudget(lead, g); !errors.Is(err, errParentBudgetGone) {
		t.Fatalf("err = %v, want errParentBudgetGone", err)
	}
	if n := e.runtimeAgentBudgets.len(); n != 0 {
		t.Fatalf("registry holds %d entries", n)
	}
}

// The registry entry goes when the agent is terminal: after its runner
// returns, and for an agent cancelled before it ever ran (via the runtime's
// events); the task's leftovers go with the task budget.
func TestRuntimeAgentBudgetRemovedWhenTerminal(t *testing.T) {
	env := runtimeRunnerFixture(t)
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 100000, MaxCostUSD: 10, MaxDurationMinutes: 10}, protocol.UsageTotals{}, nil)
	ctx := delegation.WithBudget(t.Context(), shared)
	rt := env.runtimeFor(t, ctx, readOnlyCaps, env.e.runtimeAgentOptions()...)
	spawned, err := rt.Spawn(ctx, agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "budget", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	got := waitAgentTerminal(t, rt, env.task.ID, spawned.Agent.ID)
	if got.State != protocol.AgentStateCompleted {
		t.Fatalf("state %q blocker %q", got.State, got.Result.Blocker)
	}
	if n := env.e.runtimeAgentBudgets.len(); n != 0 {
		t.Fatalf("registry holds %d entries after the agent finished", n)
	}

	// Cancelled while queued: the runner never runs; the event drops it.
	e := &Engine{}
	e.admitRuntimeAgentBudget(ctx, protocol.RuntimeAgent{ID: "Q", TaskID: "t", BudgetShare: 0.25})
	if e.runtimeAgentBudgets.len() != 1 {
		t.Fatal("spawn-time reservation missing")
	}
	e.runtimeAgentEvent(protocol.RuntimeAgent{ID: "Q", TaskID: "t", State: protocol.AgentStateQueued})
	if e.runtimeAgentBudgets.len() != 1 {
		t.Fatal("non-terminal event removed the reservation")
	}
	e.runtimeAgentEvent(protocol.RuntimeAgent{ID: "Q", TaskID: "t", State: protocol.AgentStateCancelled})
	if e.runtimeAgentBudgets.len() != 0 {
		t.Fatal("terminal event kept the reservation")
	}
	// Task end sweeps anything left for the task.
	e.admitRuntimeAgentBudget(ctx, protocol.RuntimeAgent{ID: "R", TaskID: "t", BudgetShare: 0.25})
	e.forgetRuntimeBudget("t", shared)
	if e.runtimeAgentBudgets.len() != 0 {
		t.Fatal("task end kept agent reservations")
	}
}

// Through the runner: a nested agent whose parent has no budget fails with a
// visible blocker and never calls a model.
func TestRunnerNestedAgentWithoutParentBudgetBlocks(t *testing.T) {
	env := runtimeRunnerFixture(t)
	parent := env.newRuntimeAgent(t, "parent", readOnlyCaps, "")
	child, err := env.e.Store.CreateRuntimeAgent(t.Context(), protocol.RuntimeAgent{TaskID: env.task.ID, ParentAgentID: parent.ID, Name: "child", Purpose: "p",
		Assignment: protocol.AgentAssignment{Prompt: "look"}, Capabilities: readOnlyCaps, BudgetShare: 0.25, Depth: 2}, "call-child")
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.run(t, child)
	if !errors.Is(err, errParentBudgetGone) || !strings.Contains(res.Blocker, "parent") {
		t.Fatalf("err = %v blocker %q", err, res.Blocker)
	}
	if reqs, _ := env.p.snapshot(); len(reqs) != 0 {
		t.Fatalf("model called without a budget: %d", len(reqs))
	}
}
