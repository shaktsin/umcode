package engine

import (
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Final review I1: declining the plan card (or letting it time out) of a task
// that has no approved plan and no agents ends that empty task, so agents can
// be enabled again later in the same chat.
func TestPlanDeclineClosesEmptyTask(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	plan := `{"agents":[` + planEntry("worker", "change A", "read-write", "") + `]}`
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, plan)
	_, card := x.pendingCard(t, x.e)
	x.admitted(t)
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); task.State != protocol.AgentStateCompleted {
		t.Fatalf("task after decline = %s, want completed", task.State)
	}
	// Later in the same chat: the lead may suggest again and the user can enable.
	token = x.enable(t, x.suggestTurn(t))
	if token == "" {
		t.Fatal("no token after the declined task")
	}
	if _, err := x.e.PrepareDelegation(t.Context(), x.th.ID); err != nil {
		t.Fatalf("delegation/enable after the declined task: %v", err)
	}
}

func TestPlanCardTimeoutClosesEmptyTask(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	x.e.planCardTimeout = 300 * time.Millisecond
	plan := `{"agents":[` + planEntry("worker", "change A", "read-write", "") + `]}`
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, plan)
	a, _ := x.pendingCard(t, x.e)
	x.admitted(t)
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if got, _ := x.e.Store.GetApproval(t.Context(), a.ID); got.Status != "expired" {
		t.Fatalf("card = %q, want expired", got.Status)
	}
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); task.State != protocol.AgentStateCompleted {
		t.Fatalf("task after the card timed out = %s, want completed", task.State)
	}
	if _, err := x.e.PrepareDelegation(t.Context(), x.th.ID); err != nil {
		t.Fatalf("delegation/enable after the timed-out card: %v", err)
	}
}

// A task that already has an approved plan and agents is never ended by
// declining a later plan.
func TestPlanDeclineKeepsTaskWithAgents(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	plan := `{"agents":[` + planEntry("worker", "read A", "read", "") + `]}`
	x.approvedFlow(t, plan, nil)
	x.waitAgent(t, x.spawn(t, "worker"))
	x.setLead("more agents", e2eLeadStep{"agent.propose_plan", `{"agents":[` + planEntry("helper", "read B", "read", "") + `]}`})
	_, wait := x.startTurn(t, protocol.TurnStartParams{Text: "more agents", DelegationTaskID: x.task.ID})
	_, card := x.pendingCard(t, x.e)
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); !unfinishedDelegation(task.State) {
		t.Fatalf("task with an agent = %s, want still active", task.State)
	}
}
