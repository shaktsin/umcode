package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

// cardEvents records, for each agent/plan/updated event, whether the plan's
// card already existed when the event was published.
type cardEvents struct {
	e      *Engine
	thread string
	mu     sync.Mutex
	got    []protocol.AgentPlanUpdated
	noCard []string
}

func (*cardEvents) ID() string            { return "card-events" }
func (*cardEvents) IsAdmin() bool         { return false }
func (s *cardEvents) Wants(t string) bool { return t == s.thread }
func (s *cardEvents) Notify(method string, params any) {
	ev, ok := params.(protocol.AgentPlanUpdated)
	if method != protocol.NotifyAgentPlanUpdated || !ok {
		return
	}
	_, err := s.e.Store.LatestApprovalForNode(context.Background(), "agent_plan", ev.Plan.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, ev)
	if err != nil {
		s.noCard = append(s.noCard, ev.Plan.ID+":"+string(ev.Plan.State))
	}
}

func (s *cardEvents) status(id string, st protocol.PlanState) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.got {
		if ev.Plan.ID == id && ev.Plan.State == st {
			return ev.ApprovalStatus, true
		}
	}
	return "", false
}

// I1: a failure while answering the card restores the original plan only if
// the card was not approved; if it was, the edited plan the user approved is
// the one that ends approved.
func TestPlanRespondRestoreOnlyIfCardNotApproved(t *testing.T) {
	t.Run("decide fails before the card is approved", func(t *testing.T) {
		env := planFixture(t)
		ch, a, card := env.proposeCard(t, twoPlan)
		before := env.storedPlan(t, card.PlanID)
		env.e.approvalDecideHook = func(id string, committed bool) error {
			if !committed {
				return errors.New("injected: database is locked")
			}
			return nil
		}
		_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"writer"}})
		requireRefusal(t, err, "injected")
		env.requireUntouched(t, card, a, before)
		env.e.approvalDecideHook = nil
		if _, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
			t.Fatal(err)
		}
		recv(t, ch)
	})
	t.Run("error after the card is approved", func(t *testing.T) {
		env := planFixture(t)
		ch, _, card := env.proposeCard(t, twoPlan)
		env.e.approvalDecideHook = func(id string, committed bool) error {
			if committed {
				return errors.New("injected: approval not found after decision")
			}
			return nil
		}
		res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"writer"}})
		if err != nil {
			t.Fatalf("approved card reported as failed: %v", err)
		}
		r := recv(t, ch)
		got := env.storedPlan(t, card.PlanID)
		if res.Plan.State != protocol.PlanApproved || got.State != protocol.PlanApproved || len(got.Agents) != 1 || got.Agents[0].Name != "reviewer" {
			t.Fatalf("approved plan is not the edited one: %+v", got)
		}
		if r.err != nil || strings.Contains(r.out, `"writer"`) {
			t.Fatalf("lead told %q %v", r.out, r.err)
		}
		if n := env.approvedCount(t); n != 1 {
			t.Fatalf("approved plans = %d", n)
		}
	})
	t.Run("request cancelled mid-approve", func(t *testing.T) {
		env := planFixture(t)
		ch, _, card := env.proposeCard(t, twoPlan)
		ctx, cancel := context.WithCancel(t.Context())
		env.e.approvalDecideHook = func(id string, committed bool) error {
			if committed {
				cancel()
			}
			return nil
		}
		res, err := env.e.RespondAgentPlan(ctx, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"writer"}})
		if err != nil || res.Plan.State != protocol.PlanApproved {
			t.Fatalf("cancelled request left a half state: %+v %v", res, err)
		}
		recv(t, ch)
		if got := env.storedPlan(t, card.PlanID); got.State != protocol.PlanApproved || len(got.Agents) != 1 {
			t.Fatalf("stored %+v", got)
		}
	})
}

// M1: after the waiter is gone a different non-approve decision is refused.
func TestPlanRespondDifferentDecisionAfterWaiterGone(t *testing.T) {
	for _, tc := range []struct{ first, second string }{
		{protocol.PlanDecisionDecline, protocol.PlanDecisionRevise},
		{protocol.PlanDecisionRevise, protocol.PlanDecisionDecline},
	} {
		t.Run(tc.first+" then "+tc.second, func(t *testing.T) {
			env := planFixture(t)
			ch, _, card := env.proposeCard(t, onePlan)
			if _, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: tc.first, Feedback: "f"}); err != nil {
				t.Fatal(err)
			}
			recv(t, ch)
			env.e.agentPlanMu.Lock()
			gone := env.e.agentPlanWaits[card.PlanID] == nil
			env.e.agentPlanMu.Unlock()
			if !gone {
				t.Fatal("waiter still registered")
			}
			_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: tc.second, Feedback: "f"})
			requireRefusal(t, err, "plan already decided")
			if _, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: tc.first, Feedback: "f"}); err != nil {
				t.Fatalf("identical repeat: %v", err)
			}
		})
	}
}

// M2: a repeat approve with different edits is refused.
func TestPlanRespondRepeatApproveDifferentEdits(t *testing.T) {
	env := planFixture(t)
	twoModelPool(env, t)
	ch, a, card := env.proposeCard(t, onePlan)
	first := protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"worker": "second"}}
	if _, err := env.respond(t, first); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
	if _, err := env.respond(t, first); err != nil {
		t.Fatalf("identical repeat: %v", err)
	}
	same := protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"Worker": "claude/second"}}
	if _, err := env.respond(t, same); err != nil {
		t.Fatalf("equivalent repeat: %v", err)
	}
	for _, p := range []protocol.AgentPlanRespondParams{
		{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"worker": "deterministic"}},
		{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove},
		{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"worker": "second"}, Limits: &protocol.PlanLimits{MaxConcurrent: 2}},
	} {
		_, err := env.respond(t, p)
		requireRefusal(t, err, "plan already decided with different edits")
	}
	_, err := env.e.RespondApproval(t.Context(), a.ID, true, false, "test")
	requireRefusal(t, err, "plan already decided with different edits")
	if got := env.storedPlan(t, card.PlanID); got.Agents[0].Model != "claude/second" {
		t.Fatalf("stored model %q", got.Agents[0].Model)
	}
}

// M4: events and the stored card.
func TestPlanEventsAndCardRefresh(t *testing.T) {
	env := planFixture(t)
	twoModelPool(env, t)
	sub := &cardEvents{e: env.e, thread: env.th.ID}
	env.e.Bus.Add(sub)
	first, a1, c1 := env.proposeCard(t, twoPlan)
	_, a2, c2 := env.proposeCard(t, twoPlan, a1.ID)
	recv(t, first)
	if st, ok := sub.status(c1.PlanID, protocol.PlanSuperseded); !ok || st != "expired" {
		t.Fatalf("superseded event status %q (seen %v)", st, ok)
	}
	if _, ok := sub.status(c2.PlanID, protocol.PlanProposed); !ok {
		t.Fatal("no proposal event")
	}
	res, err := env.e.RespondAgentPlanAs(context.WithoutCancel(t.Context()), "client-7", protocol.AgentPlanRespondParams{PlanID: c2.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"reviewer"}, Models: map[string]string{"writer": "second"}})
	if err != nil || res.Plan.State != protocol.PlanApproved {
		t.Fatalf("approve %+v %v", res, err)
	}
	sub.mu.Lock()
	noCard := append([]string(nil), sub.noCard...)
	sub.mu.Unlock()
	if len(noCard) != 0 {
		t.Fatalf("events published before their card existed: %v", noCard)
	}
	ap, err := env.e.Store.GetApproval(t.Context(), a2.ID)
	if err != nil {
		t.Fatal(err)
	}
	// M6: decided_by is the connection's client ID.
	if ap.DecidedBy != "client-7" {
		t.Fatalf("decided_by %q", ap.DecidedBy)
	}
	card := planCardOf(t, ap)
	if len(card.Agents) != 1 || card.Agents[0].Name != "writer" || card.Agents[0].Model != "claude/second" {
		t.Fatalf("stored card not refreshed: %+v", card.Agents)
	}
}

// M9: an approve racing a decline or a supersede ends in one consistent
// state.
func TestPlanRespondRaces(t *testing.T) {
	t.Run("approve vs decline", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			env := planFixture(t)
			ch, _, card := env.proposeCard(t, onePlan)
			var wg sync.WaitGroup
			var errApprove, errDecline error
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, errApprove = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
			}()
			go func() {
				defer wg.Done()
				_, errDecline = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline})
			}()
			wg.Wait()
			r := recv(t, ch)
			if (errApprove == nil) == (errDecline == nil) {
				t.Fatalf("approve %v, decline %v: exactly one must win", errApprove, errDecline)
			}
			got := env.storedPlan(t, card.PlanID)
			if errApprove == nil {
				requireRefusal(t, errDecline, "plan already decided")
				if got.State != protocol.PlanApproved || !strings.Contains(r.out, `"approved"`) {
					t.Fatalf("approve won but plan %q, tool %q", got.State, r.out)
				}
			} else {
				requireRefusal(t, errApprove, "plan already decided")
				if got.State != protocol.PlanProposed || !strings.Contains(r.out, `"declined"`) || env.approvedCount(t) != 0 {
					t.Fatalf("decline won but plan %q, tool %q", got.State, r.out)
				}
			}
		}
	})
	t.Run("approve vs supersede", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			env := planFixture(t)
			ch, a1, c1 := env.proposeCard(t, onePlan)
			done := make(chan error, 1)
			go func() {
				_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: c1.PlanID, Decision: protocol.PlanDecisionApprove})
				done <- err
			}()
			next := env.proposeAsync(t, env.lead, onePlan)
			a2 := env.pendingPlanApproval(t, a1.ID)
			err := <-done
			r := recv(t, ch)
			got := env.storedPlan(t, c1.PlanID)
			if err == nil {
				if got.State != protocol.PlanApproved || r.err != nil {
					t.Fatalf("approve won but plan %q, tool %q %v", got.State, r.out, r.err)
				}
			} else {
				requireRefusal(t, err, "plan was superseded")
				if got.State != protocol.PlanSuperseded || r.err == nil {
					t.Fatalf("supersede won but plan %q, tool %q", got.State, r.out)
				}
			}
			if n := env.approvedCount(t); n > 1 {
				t.Fatalf("approved plans = %d", n)
			}
			if _, err := env.e.RespondApproval(t.Context(), a2.ID, false, false, "test"); err != nil {
				t.Fatal(err)
			}
			recv(t, next)
		}
	})
}
