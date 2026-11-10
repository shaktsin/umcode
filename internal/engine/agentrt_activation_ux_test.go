package engine

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

// UX fix 1: an activation survives a turn in which the lead only asks a
// question; an empty active task is replaced by an explicit enable or
// agent.suggest instead of blocking them.

const uxPool = "e2e-pool"

func uxOverride() protocol.ModelSelection {
	return protocol.ModelSelection{Provider: protocol.PoolProvider, Model: uxPool, Complexity: protocol.ComplexityHigh}
}

// askTurn runs the admitted turn (token) in which the lead only asks a
// clarifying question, and returns the task it admitted.
func (x *activationE2E) askTurn(t *testing.T, token string) protocol.AgentTask {
	t.Helper()
	x.setLead("may I")
	x.turn(t, protocol.TurnStartParams{Text: "two agents please, may I see the plan first", DelegationActivationToken: token, Override: uxOverride()})
	return x.latestTask(t)
}

func TestActivationSurvivesClarifyingTurn(t *testing.T) {
	x := newActivationE2E(t, nil)
	token := x.enable(t, x.suggestTurn(t))
	task := x.askTurn(t, token)
	if !unfinishedDelegation(task.State) {
		t.Fatalf("task after a clarifying admitted turn = %s, want active", task.State)
	}
	// The user answers; the app continues the task by its ID and the lead
	// now proposes: the plan card appears.
	x.setLead("yes go ahead", e2eLeadStep{"agent.propose_plan", `{"agents":[` + planEntry("worker", "read A", "read", "") + `]}`})
	_, wait := x.startTurn(t, protocol.TurnStartParams{Text: "yes go ahead", DelegationTaskID: task.ID, Override: uxOverride()})
	_, card := x.pendingCard(t, x.e)
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if res := x.leadResults(t, "agent.propose_plan"); len(res) != 1 || res[0].IsError {
		t.Fatalf("propose_plan in the follow-up turn: %+v", res)
	}
}

func TestEmptyTaskReplacedByEnable(t *testing.T) {
	x := newActivationE2E(t, nil)
	token := x.enable(t, x.suggestTurn(t))
	old := x.askTurn(t, token)
	res, err := x.e.PrepareDelegation(t.Context(), x.th.ID)
	if err != nil || res.ActivationToken == "" {
		t.Fatalf("delegation/enable with an empty task: %+v %v", res, err)
	}
	if got, _ := x.e.Store.AgentTask(t.Context(), old.ID); got.State != protocol.AgentStateCompleted {
		t.Fatalf("replaced empty task = %s, want completed", got.State)
	}
	// The new token admits a fresh task.
	x.setLead("fresh start")
	x.turn(t, protocol.TurnStartParams{Text: "fresh start", DelegationActivationToken: res.ActivationToken, Override: uxOverride()})
	if fresh := x.latestTask(t); fresh.ID == old.ID || !unfinishedDelegation(fresh.State) {
		t.Fatalf("token admitted %+v, want a new active task", fresh)
	}
}

func TestEmptyTaskReplacedByTokenAdmission(t *testing.T) {
	x := newActivationE2E(t, nil)
	token := x.enable(t, x.suggestTurn(t))
	// A second token while no task exists yet; the first admits a task that
	// stays empty.
	second, err := x.e.PrepareDelegation(t.Context(), x.th.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := x.askTurn(t, token)
	x.setLead("again")
	x.turn(t, protocol.TurnStartParams{Text: "again", DelegationActivationToken: second.ActivationToken, Override: uxOverride()})
	if got, _ := x.e.Store.AgentTask(t.Context(), old.ID); got.State != protocol.AgentStateCompleted {
		t.Fatalf("empty task after a new activation = %s, want completed", got.State)
	}
	if fresh := x.latestTask(t); fresh.ID == old.ID || !unfinishedDelegation(fresh.State) {
		t.Fatalf("activation admitted %+v, want a new active task", fresh)
	}
}

func TestEmptyTaskReplacedBySuggest(t *testing.T) {
	x := newActivationE2E(t, nil)
	token := x.enable(t, x.suggestTurn(t))
	old := x.askTurn(t, token)
	// A plain turn (no task ID): agent.suggest stores a card.
	before := x.suggestionCount(t)
	x.suggestTurn(t)
	if x.suggestionCount(t) != before+1 {
		t.Fatal("no suggestion stored with an empty active task")
	}
	if got, _ := x.e.Store.AgentTask(t.Context(), old.ID); got.State != protocol.AgentStateCompleted {
		t.Fatalf("empty task after agent.suggest = %s, want completed", got.State)
	}
}

func TestTaskWithApprovedPlanStillBlocksEnable(t *testing.T) {
	x := newActivationE2E(t, nil)
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, `{"agents":[`+planEntry("worker", "read A", "read", "")+`]}`)
	_, card := x.pendingCard(t, x.e)
	x.admitted(t)
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	_, err := x.e.PrepareDelegation(t.Context(), x.th.ID)
	if err == nil || !strings.Contains(err.Error(), "already has an unfinished delegation task") {
		t.Fatalf("enable with an approved plan: %v, want the unfinished-task refusal", err)
	}
	if got, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); !unfinishedDelegation(got.State) {
		t.Fatalf("task with an approved plan = %s, want active", got.State)
	}
}
