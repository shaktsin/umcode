package engine

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Plan 5 Task 2: a turn admitted for delegation that ends without ever
// proposing a plan closes its empty task, so it never blocks enabling agents
// or agent.suggest later in the chat; a task with a plan card still waiting,
// a revised (still proposed) plan or an approved plan is kept.

// latestTask returns the newest delegation task of the chat, whatever its
// state.
func (x *activationE2E) latestTask(t *testing.T) protocol.AgentTask {
	t.Helper()
	var id string
	if err := x.e.Store.DB.QueryRow(`SELECT id FROM agent_tasks WHERE thread_id=? ORDER BY rowid DESC LIMIT 1`, x.th.ID).Scan(&id); err != nil {
		t.Fatalf("no delegation task: %v", err)
	}
	task, err := x.e.Store.AgentTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestAdmittedTurnWithoutPlanClosesTask(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	token := x.enable(t, x.suggestTurn(t))
	// The admitted turn: the lead answers without agent.propose_plan.
	x.setLead("just answer")
	x.turn(t, protocol.TurnStartParams{Text: "just answer", DelegationActivationToken: token,
		Override: protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "e2e-pool", Complexity: protocol.ComplexityHigh}})
	task := x.latestTask(t)
	if task.State != protocol.AgentStateCompleted {
		t.Fatalf("task after an admitted turn without a plan = %s, want completed", task.State)
	}
	// Nothing blocks the chat: the lead may suggest again and agents can be
	// enabled.
	if id := x.suggestTurn(t); id == "" {
		t.Fatal("no suggestion after the closed task")
	}
	if _, err := x.e.PrepareDelegation(t.Context(), x.th.ID); err != nil {
		t.Fatalf("delegation/enable after the unproposed activation: %v", err)
	}
}

func TestAdmittedTurnWithPendingPlanKeepsTask(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, `{"agents":[`+planEntry("worker", "read A", "read", "")+`]}`)
	_, card := x.pendingCard(t, x.e)
	x.admitted(t)
	// While the card waits for a decision the turn-end close refuses.
	if x.e.closeUnproposedActivation(t.Context(), x.task.ID) {
		t.Fatal("a task whose plan card is waiting was closed")
	}
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); !unfinishedDelegation(task.State) {
		t.Fatalf("task with a pending card = %s, want active", task.State)
	}
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	// The approved plan keeps the task after its turn ends.
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); !unfinishedDelegation(task.State) {
		t.Fatalf("task with an approved plan = %s after its turn, want active", task.State)
	}
}

func TestRevisedPlanTurnEndKeepsTask(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	token := x.enable(t, x.suggestTurn(t))
	// One proposal only: after the revise the lead ends its turn.
	_, wait := x.proposeTurn(t, token, `{"agents":[`+planEntry("worker", "change A", "read-write", "")+`]}`)
	_, card := x.pendingCard(t, x.e)
	x.admitted(t)
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionRevise, Feedback: "use a reader instead"}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); !unfinishedDelegation(task.State) {
		t.Fatalf("task with a revised plan = %s after its turn, want active", task.State)
	}
}

// n2: the approve commits but binding the plan fails once (a transient store
// error) in agent/plan/respond; the waiting agent.propose_plan then binds it
// and that successful bind announces agent/plan/updated approved, once.
func TestApprovedPlanPublishedWhenBindFailsOnce(t *testing.T) {
	x := newActivationE2E(t, runtimeV2On)
	sub := &recordingSub{}
	x.e.Bus.Add(sub)
	defer x.e.Bus.Remove(sub.ID())
	var once sync.Once
	binds := 0
	var bindMu sync.Mutex
	x.e.planBindHook = func(string) error {
		bindMu.Lock()
		binds++
		bindMu.Unlock()
		var err error
		once.Do(func() { err = errors.New("injected: database is locked") })
		return err
	}
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, `{"agents":[`+planEntry("worker", "read A", "read", "")+`]}`)
	a, card := x.pendingCard(t, x.e)
	// The respond may report the transient failure; the card is approved.
	_, _ = x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if got, _ := x.e.Store.AgentPlan(t.Context(), card.PlanID); got.State != protocol.PlanApproved {
		t.Fatalf("plan = %s, want approved", got.State)
	}
	res := x.leadResults(t, "agent.propose_plan")
	if len(res) != 1 || res[0].IsError || !strings.Contains(res[0].Result, `"approved"`) {
		t.Fatalf("lead told %+v, want the approved plan", res)
	}
	bindMu.Lock()
	if binds < 2 {
		t.Fatalf("binds = %d, want the failed one and a successful retry", binds)
	}
	bindMu.Unlock()
	time.Sleep(200 * time.Millisecond) // let late publishers run
	sub.mu.Lock()
	defer sub.mu.Unlock()
	resolved, approved := 0, 0
	for _, ev := range sub.events {
		switch p := ev.params.(type) {
		case protocol.ApprovalEvent:
			if ev.method == protocol.NotifyApprovalResolved && p.Approval.ID == a.ID {
				resolved++
			}
		case protocol.AgentPlanUpdated:
			if ev.method == protocol.NotifyAgentPlanUpdated && p.Plan.ID == card.PlanID && p.Plan.State == protocol.PlanApproved {
				approved++
				if p.ApprovalStatus != "approved" {
					t.Fatalf("approved plan announced with card status %q", p.ApprovalStatus)
				}
			}
		}
	}
	if resolved != 1 || approved != 1 {
		t.Fatalf("approval/resolved events %d, approved plan events %d; want 1 and 1", resolved, approved)
	}
}
