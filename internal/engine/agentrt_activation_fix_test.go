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

// Final review I2: read-only views of a suggestion card and a plan card with
// their real stored state; reading never changes anything.
func TestAgentSuggestionGet(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	id := x.suggestTurn(t)
	get := func(e *Engine) protocol.AgentSuggestion {
		t.Helper()
		s, err := e.GetAgentSuggestion(t.Context(), protocol.AgentSuggestionGetParams{SuggestionID: id})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for range 2 {
		if s := get(x.e); s.State != "pending" || s.ID != id || s.ThreadID != x.th.ID || s.Reason == "" {
			t.Fatalf("pending suggestion = %+v", s)
		}
	}
	if _, err := x.e.GetAgentSuggestion(t.Context(), protocol.AgentSuggestionGetParams{SuggestionID: "missing"}); err == nil {
		t.Fatal("unknown suggestion found")
	}
	if _, err := x.e.GetAgentSuggestion(t.Context(), protocol.AgentSuggestionGetParams{}); err == nil {
		t.Fatal("empty id accepted")
	}
	if _, err := x.e.GetAgentSuggestion(WithDelegationChild(t.Context()), protocol.AgentSuggestionGetParams{SuggestionID: id}); err == nil {
		t.Fatal("a child read a suggestion")
	}
	// After a restart the pending card reads as expired.
	restarted, p2 := restartedEngine(t, x)
	if s := get(restarted); s.State != "expired" {
		t.Fatalf("suggestion after restart = %+v", s)
	}
	// A decided card reads as decided.
	id = x.suggestTurnOn(t, restarted, p2)
	if _, err := restarted.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionDecline}); err != nil {
		t.Fatal(err)
	}
	if s := get(restarted); s.State != "declined" || s.DecidedAt == nil {
		t.Fatalf("declined suggestion = %+v", s)
	}
}

func TestAgentPlanGet(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	first := `{"agents":[` + planEntry("alpha", "change A", "read-write", "") + `]}`
	second := `{"agents":[` + planEntry("solo", "change A", "read-write", "") + `]}`
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, first, second)
	a1, card1 := x.pendingCard(t, x.e)
	get := func(e *Engine, id string) protocol.AgentPlanGetResult {
		t.Helper()
		r, err := e.GetAgentPlan(t.Context(), protocol.AgentPlanGetParams{PlanID: id})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for range 2 {
		r := get(x.e, card1.PlanID)
		if r.Plan.State != protocol.PlanProposed || r.ApprovalStatus != "pending" || r.ApprovalID != a1.ID || r.Card == nil || r.Card.PlanID != card1.PlanID || len(r.Card.Agents) != 1 {
			t.Fatalf("pending plan = %+v", r)
		}
	}
	if got, _ := x.e.Store.GetApproval(t.Context(), a1.ID); got.Status != "pending" {
		t.Fatalf("reading changed the card: %q", got.Status)
	}
	// Revise: the lead re-proposes, the first plan reads superseded/expired.
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card1.PlanID, Decision: protocol.PlanDecisionRevise, Feedback: "one is enough"}); err != nil {
		t.Fatal(err)
	}
	a2, card2 := x.pendingCard(t, x.e, a1.ID)
	if r := get(x.e, card1.PlanID); r.Plan.State != protocol.PlanSuperseded || r.ApprovalStatus != "denied" {
		t.Fatalf("revised and superseded plan = %+v", r)
	}
	// Approve with a model edit: the read shows the approved plan and card.
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card2.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"solo": "second"}}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	r := get(x.e, card2.PlanID)
	if r.Plan.State != protocol.PlanApproved || r.ApprovalStatus != "approved" || r.ApprovalID != a2.ID || r.Card == nil || r.Card.Agents[0].Model != "claude/second" {
		t.Fatalf("approved plan = %+v", r)
	}
	if _, err := x.e.GetAgentPlan(t.Context(), protocol.AgentPlanGetParams{PlanID: "missing"}); err == nil {
		t.Fatal("unknown plan found")
	}
	if _, err := x.e.GetAgentPlan(WithDelegationChild(t.Context()), protocol.AgentPlanGetParams{PlanID: card2.PlanID}); err == nil {
		t.Fatal("a child read a plan")
	}
}

func TestAgentPlanGetAfterRestart(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	token := x.enable(t, x.suggestTurn(t))
	x.proposeTurn(t, token, `{"agents":[`+planEntry("worker", "change A", "read-write", "")+`]}`)
	a, card := x.pendingCard(t, x.e)
	restarted, _ := restartedEngine(t, x)
	r, err := restarted.GetAgentPlan(t.Context(), protocol.AgentPlanGetParams{PlanID: card.PlanID})
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan.State != protocol.PlanProposed || r.ApprovalStatus != "expired" || r.ApprovalID != a.ID || r.Card == nil {
		t.Fatalf("plan after restart = %+v", r)
	}
}
