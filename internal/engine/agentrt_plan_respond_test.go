package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// planEvents records agent/plan/updated notifications of one thread.
type planEvents struct {
	thread string
	mu     sync.Mutex
	got    []protocol.AgentPlanUpdated
}

func (*planEvents) ID() string            { return "plan-events" }
func (*planEvents) IsAdmin() bool         { return false }
func (s *planEvents) Wants(t string) bool { return t == s.thread }
func (s *planEvents) Notify(method string, params any) {
	if method != protocol.NotifyAgentPlanUpdated {
		return
	}
	ev, ok := params.(protocol.AgentPlanUpdated)
	if !ok {
		return
	}
	s.mu.Lock()
	s.got = append(s.got, ev)
	s.mu.Unlock()
}

// sawState reports whether an event announced plan id in state st, waiting
// briefly (a proposal is announced just after its card is stored).
func (s *planEvents) sawState(id string, st protocol.PlanState) bool {
	deadline := time.Now().Add(5 * time.Second)
	for !s.seen(id, st) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

func (s *planEvents) seen(id string, st protocol.PlanState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.got {
		if ev.ThreadID == s.thread && ev.Plan.ID == id && ev.Plan.State == st {
			return true
		}
	}
	return false
}

func (env planEnv) events() *planEvents {
	sub := &planEvents{thread: env.th.ID}
	env.e.Bus.Add(sub)
	return sub
}

// proposeCard proposes and returns the pending card.
func (env planEnv) proposeCard(t *testing.T, args string, skip ...string) (<-chan proposeResult, protocol.Approval, protocol.PlanCard) {
	t.Helper()
	ch := env.proposeAsync(t, env.lead, args)
	a := env.pendingPlanApproval(t, skip...)
	return ch, a, planCardOf(t, a)
}

func (env planEnv) respond(t *testing.T, p protocol.AgentPlanRespondParams) (protocol.AgentPlanRespondResult, error) {
	t.Helper()
	return env.e.RespondAgentPlan(context.WithoutCancel(t.Context()), p)
}

// requireUntouched asserts a refused respond changed nothing: the plan is
// still proposed with the given stored entries and its card still pending.
func (env planEnv) requireUntouched(t *testing.T, card protocol.PlanCard, a protocol.Approval, before protocol.AgentPlan) {
	t.Helper()
	got, err := env.e.Store.AgentPlan(t.Context(), card.PlanID)
	if err != nil || got.State != protocol.PlanProposed {
		t.Fatalf("plan after refusal: %+v %v", got, err)
	}
	b1, _ := json.Marshal(before.Agents)
	b2, _ := json.Marshal(got.Agents)
	if string(b1) != string(b2) || before.Limits != got.Limits {
		t.Fatalf("refused respond changed the plan:\nbefore %s %+v\nafter  %s %+v", b1, before.Limits, b2, got.Limits)
	}
	ap, err := env.e.Store.GetApproval(t.Context(), a.ID)
	if err != nil || ap.Status != "pending" {
		t.Fatalf("approval after refusal: %+v %v", ap, err)
	}
	if _, err := env.e.Store.ApprovedAgentPlanForTask(t.Context(), env.task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused respond approved a plan: %v", err)
	}
}

func (env planEnv) storedPlan(t *testing.T, id string) protocol.AgentPlan {
	t.Helper()
	p, err := env.e.Store.AgentPlan(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (env planEnv) approvedCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := env.e.Store.DB.QueryRow(`SELECT count(*) FROM agent_plans WHERE thread_id=? AND state='approved'`, env.th.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireRefusal(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted; want refusal naming %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("refusal %q does not name %q", err, p)
		}
	}
}

func twoModelPool(env planEnv, t *testing.T) {
	env.setPool(t, store.AgentModelScope{Provider: "claude", Model: "deterministic", Candidates: []protocol.ConfiguredModel{
		{ID: "cfg-a", Provider: "claude", Model: "deterministic", Enabled: true},
		{ID: "cfg-b", Provider: "claude", Model: "second", Enabled: true},
		{ID: "cfg-h", Provider: "claude", Model: "hidden", Enabled: false},
	}})
}

const twoPlan = `{"agents":[` +
	`{"name":"writer","purpose":"implement","summary":"s","capabilities":{"files":"read-write","shell":"none","network":false,"spawnMax":0}},` +
	`{"name":"reviewer","purpose":"review","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`

func TestPlanRespondApprovesWithModelEdit(t *testing.T) {
	env := planFixture(t)
	twoModelPool(env, t)
	ev := env.events()
	ch, a, card := env.proposeCard(t, onePlan)
	if cardAgent(t, card, "worker").Model != "claude/deterministic" {
		t.Fatalf("default model %q", cardAgent(t, card, "worker").Model)
	}
	if !ev.sawState(card.PlanID, protocol.PlanProposed) {
		t.Fatal("no agent/plan/updated for the proposal")
	}
	res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"worker": "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ApprovalStatus != "approved" || res.Plan.ID != card.PlanID || res.Plan.State != protocol.PlanApproved || res.Plan.Agents[0].Model != "claude/second" || res.Plan.TaskID != env.task.ID {
		t.Fatalf("result %+v", res)
	}
	r := recv(t, ch)
	if r.err != nil || !strings.Contains(r.out, `"state":"approved"`) || !strings.Contains(r.out, `"model":"claude/second"`) {
		t.Fatalf("tool result %q %v", r.out, r.err)
	}
	if got := env.storedPlan(t, card.PlanID); got.State != protocol.PlanApproved || got.Agents[0].Model != "claude/second" {
		t.Fatalf("stored %+v", got)
	}
	if ap, _ := env.e.Store.GetApproval(t.Context(), a.ID); ap.Status != "approved" {
		t.Fatalf("approval %q", ap.Status)
	}
	if !ev.sawState(card.PlanID, protocol.PlanApproved) {
		t.Fatal("no agent/plan/updated for the approval")
	}
}

func TestPlanRespondRefusesModelOutsidePool(t *testing.T) {
	env := planFixture(t)
	twoModelPool(env, t)
	ch, a, card := env.proposeCard(t, onePlan)
	before := env.storedPlan(t, card.PlanID)
	for _, m := range []string{"claude/hidden", "nope"} {
		_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"worker": m}})
		requireRefusal(t, err, m, "pool")
		env.requireUntouched(t, card, a, before)
	}
	// An edit for an agent the plan does not have is refused too.
	_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"ghost": "second"}})
	requireRefusal(t, err, "ghost")
	env.requireUntouched(t, card, a, before)
	// A model that left the pool after the proposal is refused on approve,
	// through agent/plan/respond and through plain approval/respond.
	env.setPool(t, store.AgentModelScope{Provider: "claude", Model: "second", Candidates: []protocol.ConfiguredModel{{Provider: "claude", Model: "second", Enabled: true}}})
	_, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
	requireRefusal(t, err, "claude/deterministic", "pool")
	env.requireUntouched(t, card, a, before)
	_, err = env.e.RespondApproval(t.Context(), a.ID, true, false, "test")
	requireRefusal(t, err, "claude/deterministic", "pool")
	env.requireUntouched(t, card, a, before)
	if _, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
}

func TestPlanRespondRefusesIncompatibleModel(t *testing.T) {
	env := planFixture(t)
	twoModelPool(env, t)
	env.e.planModelPoolHook = func(pool []modelRoute) []modelRoute {
		for i := range pool {
			if pool[i].Model == "second" {
				pool[i].Tools = false
			}
		}
		return pool
	}
	ch, a, card := env.proposeCard(t, `{"agents":[`+
		`{"name":"coder","purpose":"implement","summary":"s","capabilities":{"files":"read-write","shell":"none","network":false,"spawnMax":0}},`+
		`{"name":"talker","purpose":"advise","summary":"s","capabilities":{"files":"none","shell":"none","network":false,"spawnMax":0}}]}`)
	before := env.storedPlan(t, card.PlanID)
	for _, m := range card.Models {
		if m.Name == "claude/second" && m.ToolsOK {
			t.Fatalf("card shows second toolsOk: %+v", m)
		}
	}
	_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"coder": "claude/second"}})
	requireRefusal(t, err, "coder", "claude/second", "cannot use tools")
	env.requireUntouched(t, card, a, before)
	// An entry that needs no tools may take it.
	res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"talker": "claude/second"}})
	if err != nil || res.Plan.State != protocol.PlanApproved {
		t.Fatalf("talker with a no-tools model: %+v %v", res, err)
	}
	if r := recv(t, ch); r.err != nil {
		t.Fatal(r.err)
	}

	// Plain approval/respond runs the same approve-time checks: a coding
	// entry proposed with the no-tools model cannot be approved there.
	ch, a, card = env.proposeCard(t, `{"agents":[{"name":"coder","purpose":"implement","summary":"s","capabilities":{"files":"read-write","shell":"none","network":false,"spawnMax":0},"model":"claude/second"}]}`, a.ID)
	before = env.storedPlan(t, card.PlanID)
	_, err = env.e.RespondApproval(t.Context(), a.ID, true, false, "test")
	requireRefusal(t, err, "coder", "claude/second", "cannot use tools")
	env.requireUntouched2(t, card, a, before)
	if _, err = env.e.RespondApproval(t.Context(), a.ID, false, false, "test"); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
}

// requireUntouched2 is requireUntouched for a thread that already has an
// approved plan.
func (env planEnv) requireUntouched2(t *testing.T, card protocol.PlanCard, a protocol.Approval, before protocol.AgentPlan) {
	t.Helper()
	got := env.storedPlan(t, card.PlanID)
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(got)
	if got.State != protocol.PlanProposed || string(b1) != string(b2) {
		t.Fatalf("refused respond changed the plan: %s -> %s", b1, b2)
	}
	if ap, _ := env.e.Store.GetApproval(t.Context(), a.ID); ap.Status != "pending" {
		t.Fatalf("approval %q after refusal", ap.Status)
	}
}

func TestPlanRespondRefusesUnavailableModelWithoutFallback(t *testing.T) {
	env := planFixture(t)
	env.setPool(t, store.AgentModelScope{Provider: "openai", Model: "gpt-x", Candidates: []protocol.ConfiguredModel{{Provider: "openai", Model: "gpt-x", Enabled: true}}})
	ch, a, card := env.proposeCard(t, `{"agents":[{"name":"worker","purpose":"p","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0},"model":"openai/gpt-x"}]}`)
	before := env.storedPlan(t, card.PlanID)
	_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
	requireRefusal(t, err, "openai/gpt-x", "unavailable")
	env.requireUntouched(t, card, a, before)
	if _, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
}

func TestPlanRespondDropEntries(t *testing.T) {
	env := planFixture(t)
	ch, a, card := env.proposeCard(t, twoPlan)
	before := env.storedPlan(t, card.PlanID)
	_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"writer", "Reviewer"}})
	requireRefusal(t, err, "every agent")
	env.requireUntouched(t, card, a, before)
	_, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"ghost"}})
	requireRefusal(t, err, "ghost")
	env.requireUntouched(t, card, a, before)
	// A model edit for a dropped agent is refused.
	_, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"writer"}, Models: map[string]string{"writer": "claude/deterministic"}})
	requireRefusal(t, err, "writer")
	env.requireUntouched(t, card, a, before)
	res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Drop: []string{"writer"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Agents) != 1 || res.Plan.Agents[0].Name != "reviewer" || res.Plan.State != protocol.PlanApproved {
		t.Fatalf("result %+v", res.Plan)
	}
	r := recv(t, ch)
	if r.err != nil || strings.Contains(r.out, `"writer"`) || !strings.Contains(r.out, `"reviewer"`) {
		t.Fatalf("tool result %q %v", r.out, r.err)
	}
}

func TestPlanRespondLimitCeilings(t *testing.T) {
	t.Run("ceilings", func(t *testing.T) {
		env := planFixture(t)
		ch, a, card := env.proposeCard(t, onePlan)
		before := env.storedPlan(t, card.PlanID)
		for name, l := range map[string]protocol.PlanLimits{
			"negative concurrent": {MaxConcurrent: -1},
			"negative depth":      {MaxDepth: -1},
			"negative total":      {MaxTotal: -2},
			"negative share":      {SliceShare: -0.1},
		} {
			l := l
			_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Limits: &l})
			if err == nil {
				t.Fatalf("%s accepted", name)
			}
			env.requireUntouched(t, card, a, before)
		}
		res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Limits: &protocol.PlanLimits{MaxConcurrent: 20, MaxDepth: 9, MaxTotal: 100, SliceShare: 0.9}})
		if err != nil {
			t.Fatal(err)
		}
		want := protocol.PlanLimits{MaxConcurrent: tools.MaxPlanConcurrent, MaxDepth: tools.MaxPlanDepth, MaxTotal: tools.MaxPlanTotal, SliceShare: tools.MaxPlanSliceShare}
		if res.Plan.Limits != want || env.storedPlan(t, card.PlanID).Limits != want {
			t.Fatalf("limits = %+v, want clamped %+v", res.Plan.Limits, want)
		}
		recv(t, ch)
	})
	t.Run("network", func(t *testing.T) {
		env := planFixture(t)
		ch, a, card := env.proposeCard(t, `{"agents":[{"name":"fetcher","purpose":"p","summary":"s","capabilities":{"files":"read","shell":"none","network":true,"spawnMax":0}}],"limits":{"network":true}}`)
		before := env.storedPlan(t, card.PlanID)
		if !before.Limits.Network || !before.Agents[0].Capabilities.Network {
			t.Fatalf("proposed %+v", before)
		}
		off := false
		if _, err := env.e.Store.UpdateProject(t.Context(), protocol.ProjectUpdateParams{ProjectID: env.th.ProjectID, Tools: &protocol.ProjectTools{Network: &off}}); err != nil {
			t.Fatal(err)
		}
		// The project's network was turned off after the proposal: a plan
		// that keeps limits.network is refused, also via approval/respond.
		_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
		requireRefusal(t, err, "network")
		env.requireUntouched(t, card, a, before)
		_, err = env.e.RespondApproval(t.Context(), a.ID, true, false, "test")
		requireRefusal(t, err, "network")
		env.requireUntouched(t, card, a, before)
		// Turning limits.network off re-runs the clamp: the agent loses it.
		res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Limits: &protocol.PlanLimits{Network: false}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Plan.Limits.Network || res.Plan.Agents[0].Capabilities.Network {
			t.Fatalf("network kept: %+v", res.Plan)
		}
		if r := recv(t, ch); r.err != nil || strings.Contains(r.out, `"network":true`) {
			t.Fatalf("tool result %q %v", r.out, r.err)
		}
	})
	t.Run("reclamp never widens", func(t *testing.T) {
		env := planFixture(t)
		ch, _, card := env.proposeCard(t, `{"agents":[{"name":"checker","purpose":"p","summary":"s","capabilities":{"files":"read","shell":"read-only","network":false,"spawnMax":0}},`+
			`{"name":"writer","purpose":"p","summary":"s","capabilities":{"files":"read-write","shell":"read-write","network":false,"spawnMax":0}}]}`)
		if g := cardAgent(t, card, "checker").Granted.Shell; g != protocol.ShellReadOnly {
			t.Fatalf("checker shell %q at propose", g)
		}
		// The sandbox is gone by approve time: the clamp re-runs and drops
		// the shell; it never grants more than the stored grant.
		(&shellSeams{}).install(env.e, nil, nil)
		res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Limits: &protocol.PlanLimits{Network: false}})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range res.Plan.Agents {
			if a.Capabilities.Shell != protocol.ShellNone {
				t.Fatalf("%s keeps shell %q without a sandbox", a.Name, a.Capabilities.Shell)
			}
		}
		if res.Plan.Agents[1].Capabilities.Files != protocol.FilesReadWrite {
			t.Fatalf("writer files %q", res.Plan.Agents[1].Capabilities.Files)
		}
		recv(t, ch)
	})
}

func TestPlanRespondStaleAndDouble(t *testing.T) {
	t.Run("superseded", func(t *testing.T) {
		env := planFixture(t)
		ev := env.events()
		first, a1, c1 := env.proposeCard(t, onePlan)
		second, a2, c2 := env.proposeCard(t, onePlan, a1.ID)
		if r := recv(t, first); r.err == nil || !strings.Contains(r.err.Error(), "plan was superseded") {
			t.Fatalf("first = %q %v", r.out, r.err)
		}
		if !ev.sawState(c1.PlanID, protocol.PlanSuperseded) {
			t.Fatal("no agent/plan/updated for the superseded plan")
		}
		for _, d := range []string{protocol.PlanDecisionApprove, protocol.PlanDecisionDecline, protocol.PlanDecisionRevise} {
			_, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: c1.PlanID, Decision: d, Feedback: "x"})
			requireRefusal(t, err, "plan was superseded")
		}
		_, err := env.e.RespondApproval(t.Context(), a1.ID, true, false, "test")
		requireRefusal(t, err, "plan was superseded")
		if _, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: c2.PlanID, Decision: protocol.PlanDecisionApprove}); err != nil {
			t.Fatal(err)
		}
		if r := recv(t, second); r.err != nil {
			t.Fatal(r.err)
		}
		// The same decision again is idempotent; another one is refused.
		res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: c2.PlanID, Decision: protocol.PlanDecisionApprove})
		if err != nil || res.Plan.State != protocol.PlanApproved || res.ApprovalStatus != "approved" {
			t.Fatalf("repeat approve: %+v %v", res, err)
		}
		if _, err = env.e.RespondApproval(t.Context(), a2.ID, true, false, "test"); err != nil {
			t.Fatalf("repeat approve via approval/respond: %v", err)
		}
		_, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: c2.PlanID, Decision: protocol.PlanDecisionDecline})
		requireRefusal(t, err, "plan already decided")
		if n := env.approvedCount(t); n != 1 {
			t.Fatalf("approved plans = %d", n)
		}
		if _, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: "plan-missing", Decision: protocol.PlanDecisionApprove}); err == nil {
			t.Fatal("unknown plan accepted")
		}
	})
	t.Run("concurrent double approve", func(t *testing.T) {
		env := planFixture(t)
		ch, _, card := env.proposeCard(t, onePlan)
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("approve %d: %v", i, err)
			}
		}
		if r := recv(t, ch); r.err != nil {
			t.Fatal(r.err)
		}
		if n := env.approvedCount(t); n != 1 {
			t.Fatalf("approved plans = %d", n)
		}
	})
	t.Run("decideAgentPlan approve twice", func(t *testing.T) {
		env := planFixture(t)
		ch, _, card := env.proposeCard(t, onePlan)
		if _, err := env.e.decideAgentPlan(t.Context(), card.PlanID, planApproved, "", "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.e.decideAgentPlan(t.Context(), card.PlanID, planApproved, "", "test"); err != nil {
			t.Fatalf("second approve: %v", err)
		}
		if r := recv(t, ch); r.err != nil || !strings.Contains(r.out, `"approved"`) {
			t.Fatalf("tool %q %v", r.out, r.err)
		}
		if _, err := env.e.decideAgentPlan(t.Context(), card.PlanID, planDeclined, "", "test"); err == nil || !strings.Contains(err.Error(), "plan already decided") {
			t.Fatalf("decline after approve: %v", err)
		}
		if n := env.approvedCount(t); n != 1 {
			t.Fatalf("approved plans = %d", n)
		}
	})
	t.Run("declined", func(t *testing.T) {
		env := planFixture(t)
		ev := env.events()
		ch, a, card := env.proposeCard(t, onePlan)
		if _, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
			t.Fatal(err)
		}
		recv(t, ch)
		res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline})
		if err != nil || res.ApprovalStatus != "denied" || res.Plan.State != protocol.PlanProposed {
			t.Fatalf("repeat decline: %+v %v", res, err)
		}
		_, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
		requireRefusal(t, err, "plan already decided")
		_, err = env.e.RespondApproval(t.Context(), a.ID, true, false, "test")
		requireRefusal(t, err, "plan already decided")
		if n := env.approvedCount(t); n != 0 {
			t.Fatalf("declined plan approved: %d", n)
		}
		ev.mu.Lock()
		n := len(ev.got)
		ev.mu.Unlock()
		if n < 2 {
			t.Fatalf("plan events = %d, want the proposal and the decision", n)
		}
	})
}

func TestPlanRespondRefusesChildrenAndBadParams(t *testing.T) {
	env := planFixture(t)
	ch, a, card := env.proposeCard(t, onePlan)
	before := env.storedPlan(t, card.PlanID)
	child := withChildExecution(WithDelegationChild(t.Context()), &childExecution{})
	if _, err := env.e.RespondAgentPlan(child, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove}); err == nil {
		t.Fatal("child answered a plan card")
	}
	for _, p := range []protocol.AgentPlanRespondParams{
		{Decision: protocol.PlanDecisionApprove},
		{PlanID: card.PlanID, Decision: "maybe"},
		{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline, Drop: []string{"worker"}},
		{PlanID: card.PlanID, Decision: protocol.PlanDecisionRevise},
	} {
		if _, err := env.respond(t, p); err == nil {
			t.Fatalf("%+v accepted", p)
		}
	}
	env.requireUntouched(t, card, a, before)
	// No tool reaches it: the model cannot answer its own plan card.
	for _, tool := range env.e.Tools.All() {
		if strings.Contains(tool.Name(), "respond") {
			t.Fatalf("tool %s exposes a respond path", tool.Name())
		}
	}
	if _, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
}

func TestPlanRespondReviseReturnsFeedbackToLead(t *testing.T) {
	env := planFixture(t)
	ch, a, card := env.proposeCard(t, onePlan)
	res, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionRevise, Feedback: "  split it into a writer and a reviewer "})
	if err != nil {
		t.Fatal(err)
	}
	if res.ApprovalStatus != "denied" || res.Plan.State != protocol.PlanProposed {
		t.Fatalf("result %+v", res)
	}
	r := recv(t, ch)
	if r.err != nil || r.out != `{"state":"revise","feedback":"split it into a writer and a reviewer"}` {
		t.Fatalf("tool result %q %v", r.out, r.err)
	}
	if ap, _ := env.e.Store.GetApproval(t.Context(), a.ID); ap.Status != "denied" {
		t.Fatalf("approval %q", ap.Status)
	}
	if _, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionRevise, Feedback: "again"}); err != nil {
		t.Fatalf("repeat revise: %v", err)
	}
	_, err = env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
	requireRefusal(t, err, "plan already decided")
}

func TestApprovedAssignmentsPrefillNextPlan(t *testing.T) {
	env := planFixture(t)
	twoModelPool(env, t)
	ch, _, card := env.proposeCard(t, twoPlan)
	if _, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"Writer": "cfg-b"}}); err != nil {
		t.Fatal(err)
	}
	if r := recv(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
	ch, _, next := env.proposeCard(t, twoPlan)
	if m := cardAgent(t, next, "writer").Model; m != "claude/second" {
		t.Fatalf("writer prefilled %q, want the approved assignment", m)
	}
	if m := cardAgent(t, next, "reviewer").Model; m != "claude/deterministic" {
		t.Fatalf("reviewer prefilled %q", m)
	}
	want := []protocol.PreviousAssignment{{PlanID: card.PlanID, Name: "writer", Model: "claude/second"}, {PlanID: card.PlanID, Name: "reviewer", Model: "claude/deterministic"}}
	if !slices.Equal(next.Previous, want) {
		t.Fatalf("previous = %+v", next.Previous)
	}
	if _, err := env.respond(t, protocol.AgentPlanRespondParams{PlanID: next.PlanID, Decision: protocol.PlanDecisionDecline}); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
}

func TestPendingPlanExpiresOnRestart(t *testing.T) {
	env := planFixture(t)
	ctx, cancel := context.WithCancel(env.lead)
	defer cancel()
	ch := env.proposeAsync(t, ctx, onePlan)
	a := env.pendingPlanApproval(t)
	card := planCardOf(t, a)

	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider = "claude"
	cfg.LLM.Model = "deterministic"
	reg := llm.NewRegistry()
	reg.Register(&delegationProvider{})
	restarted, err := New(t.Context(), Options{Config: cfg, Store: env.e.Store, Secrets: secrets.NewMemoryStore(), LLMs: reg, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Shutdown(context.Background()) })
	if ap, _ := env.e.Store.GetApproval(t.Context(), a.ID); ap.Status != "expired" {
		t.Fatalf("approval after restart %q", ap.Status)
	}
	if p := env.storedPlan(t, card.PlanID); p.State != protocol.PlanProposed {
		t.Fatalf("plan after restart %q", p.State)
	}
	_, err = restarted.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
	requireRefusal(t, err, "expired")
	_, err = restarted.RespondApproval(t.Context(), a.ID, true, false, "test")
	requireRefusal(t, err, "expired")
	if p := env.storedPlan(t, card.PlanID); p.State != protocol.PlanProposed {
		t.Fatalf("plan after refused approve %q", p.State)
	}
	call := llm.ToolCall{ID: "s-restart", Name: tools.ToWire("agent.spawn"), Args: json.RawMessage(`{"planEntry":"worker","assignment":{"prompt":"go"}}`)}
	if r := restarted.runTool(env.lead, t.Context(), env.th, env.turn, call, nil); !r.IsError {
		t.Fatalf("spawn after restart = %+v", r)
	}
	if _, err := env.e.Store.ApprovedAgentPlanForTask(t.Context(), env.task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approved plan after restart: %v", err)
	}
	cancel()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("old proposal did not return")
	}
}
