package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPlanRespondAdminOnly(t *testing.T) {
	h := newHarness(t, nil)
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
	// approval/respond on the plan's card is admin-only too.
	now := time.Now().UTC()
	card := protocol.Approval{ID: "apr-plan-admin", Kind: "agent_plan", NodeID: plan.ID, ThreadID: th.ID, Tool: "agent.propose_plan", Args: []byte(`{}`), Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = h.eng.Store.CreateApproval(h.ctx, card); err != nil {
		t.Fatal(err)
	}
	for _, approve := range []bool{true, false} {
		err := nonadmin.Call(h.ctx, protocol.MethodApprovalRespond, protocol.ApprovalRespondParams{ApprovalID: card.ID, Approve: approve, Remember: true}, nil)
		if err == nil || !strings.Contains(err.Error(), "only admin clients") {
			t.Fatalf("non-admin approval/respond approve=%v: %v", approve, err)
		}
	}
	if a, _ := h.eng.Store.GetApproval(h.ctx, card.ID); a.Status != "pending" {
		t.Fatalf("non-admin decided the card: %q", a.Status)
	}
	if got, _ = h.eng.Store.AgentPlan(h.ctx, plan.ID); got.State != protocol.PlanProposed {
		t.Fatalf("plan approved by a non-admin: %+v", got)
	}
}
