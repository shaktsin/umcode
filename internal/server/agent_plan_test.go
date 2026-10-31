package server_test

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPlanRespondAdminOnly(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Agents.RuntimeV2 = true })
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "plan"}, &th)
	plan := protocol.AgentPlan{ID: "plan-admin", ThreadID: th.ID, State: protocol.PlanProposed, Limits: protocol.DefaultPlanLimits(), Agents: []protocol.PlanAgent{
		{Name: "worker", Purpose: "p", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}},
	}}
	if err := h.eng.Store.SaveAgentPlan(h.ctx, plan); err != nil {
		t.Fatal(err)
	}
	nonadmin, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "tool-client", false)
	if err != nil {
		t.Fatal(err)
	}
	defer nonadmin.Close()
	for _, d := range []string{protocol.PlanDecisionApprove, protocol.PlanDecisionDecline, protocol.PlanDecisionRevise} {
		err := nonadmin.Call(h.ctx, protocol.MethodAgentPlanRespond, protocol.AgentPlanRespondParams{PlanID: plan.ID, Decision: d, Feedback: "x"}, nil)
		if err == nil || !strings.Contains(err.Error(), "only app clients") {
			t.Fatalf("non-app %s: %v", d, err)
		}
	}
	got, err := h.eng.Store.AgentPlan(h.ctx, plan.ID)
	if err != nil || got.State != protocol.PlanProposed {
		t.Fatalf("non-admin changed state: %+v %v", got, err)
	}
	// The app client reaches the engine, which refuses a plan with no card.
	err = h.callErr(protocol.MethodAgentPlanRespond, protocol.AgentPlanRespondParams{PlanID: plan.ID, Decision: protocol.PlanDecisionApprove})
	if strings.Contains(err.Error(), "only app clients") {
		t.Fatalf("admin refused as non-admin: %v", err)
	}
	if got, _ = h.eng.Store.AgentPlan(h.ctx, plan.ID); got.State != protocol.PlanProposed {
		t.Fatalf("plan without a card approved: %+v", got)
	}
}
