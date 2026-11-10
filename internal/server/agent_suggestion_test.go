package server_test

import (
	"testing"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func TestSuggestionRespondAdminOnly(t *testing.T) {
	h := newHarness(t, nil)
	commitDelegationFixture(t, h)
	h.addKey("claude", "test", "fake")
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "suggest"}, &th)
	sug, err := h.eng.Store.CreateAgentSuggestion(h.ctx, store.AgentSuggestion{ThreadID: th.ID, TurnID: "turn-1", Reason: "parallel work"})
	if err != nil {
		t.Fatal(err)
	}
	nonadmin, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "tool-client", false)
	if err != nil {
		t.Fatal(err)
	}
	defer nonadmin.Close()
	for _, d := range []string{protocol.SuggestionEnable, protocol.SuggestionDecline} {
		if err = nonadmin.Call(h.ctx, protocol.MethodAgentSuggestionRespond, protocol.AgentSuggestionRespondParams{SuggestionID: sug.ID, Decision: d}, nil); err == nil {
			t.Fatalf("non-app %s accepted", d)
		}
	}
	got, err := h.eng.Store.GetAgentSuggestion(h.ctx, sug.ID)
	if err != nil || got.State != store.SuggestionPending {
		t.Fatalf("non-admin changed state: %+v %v", got, err)
	}
	var res protocol.AgentSuggestionRespondResult
	h.call(protocol.MethodAgentSuggestionRespond, protocol.AgentSuggestionRespondParams{SuggestionID: sug.ID, Decision: protocol.SuggestionEnable}, &res)
	if res.Suggestion.State != store.SuggestionEnabled || res.ActivationToken == "" || res.ExpiresAt == nil {
		t.Fatalf("admin enable: %+v", res)
	}
	h.fake.push(textReply("done"))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "go", DelegationActivationToken: res.ActivationToken}, &started)
	h.waitTurn(started.Turn.ID, nil)
	if _, err = h.eng.Store.AgentTaskForRootTurn(h.ctx, th.ID, started.Turn.ID); err != nil {
		t.Fatalf("follow-up turn not admitted: %v", err)
	}
}

// agent/suggestion/get and agent/plan/get are app-only reads.
func TestActivationGetAdminOnly(t *testing.T) {
	h := newHarness(t, nil)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "get"}, &th)
	sug, err := h.eng.Store.CreateAgentSuggestion(h.ctx, store.AgentSuggestion{ThreadID: th.ID, TurnID: "turn-1", Reason: "parallel work"})
	if err != nil {
		t.Fatal(err)
	}
	plan := protocol.AgentPlan{ID: "plan-get", ThreadID: th.ID, State: protocol.PlanProposed, Limits: protocol.DefaultPlanLimits(), Agents: []protocol.PlanAgent{
		{Name: "worker", Purpose: "p", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}},
	}}
	if err = h.eng.Store.SaveAgentPlan(h.ctx, plan); err != nil {
		t.Fatal(err)
	}
	nonadmin, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "tool-client", false)
	if err != nil {
		t.Fatal(err)
	}
	defer nonadmin.Close()
	if err = nonadmin.Call(h.ctx, protocol.MethodAgentSuggestionGet, protocol.AgentSuggestionGetParams{SuggestionID: sug.ID}, nil); err == nil {
		t.Fatal("non-app suggestion read accepted")
	}
	if err = nonadmin.Call(h.ctx, protocol.MethodAgentPlanGet, protocol.AgentPlanGetParams{PlanID: plan.ID}, nil); err == nil {
		t.Fatal("non-app plan read accepted")
	}
	var s protocol.AgentSuggestion
	h.call(protocol.MethodAgentSuggestionGet, protocol.AgentSuggestionGetParams{SuggestionID: sug.ID}, &s)
	if s.ID != sug.ID || s.State != store.SuggestionPending {
		t.Fatalf("suggestion = %+v", s)
	}
	var p protocol.AgentPlanGetResult
	h.call(protocol.MethodAgentPlanGet, protocol.AgentPlanGetParams{PlanID: plan.ID}, &p)
	if p.Plan.ID != plan.ID || p.Plan.State != protocol.PlanProposed || p.ApprovalStatus != "" {
		t.Fatalf("plan = %+v", p)
	}
	if got, _ := h.eng.Store.GetAgentSuggestion(h.ctx, sug.ID); got.State != store.SuggestionPending {
		t.Fatalf("reading changed the suggestion: %+v", got)
	}
}
