package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

func requestedAgentLeadTurn(t *testing.T, env suggestEnv) context.Context {
	t.Helper()
	turn := delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "Use two agents to implement and review this change."})
	return tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: env.root})
}

func TestUnrequestedAgentPlanIsRefused(t *testing.T) {
	env := suggestFixture(t)
	lead := env.leadTurn(t) // user's message is just "hello"
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	if _, err := tool.Call(lead, json.RawMessage(onePlan)); err == nil {
		t.Fatal("agent plan started without a user request for agents")
	}
	if _, err := env.e.Store.ActiveAgentTask(t.Context(), env.th.ID, env.th.ProjectID); err == nil {
		t.Fatal("unrequested agent task was admitted")
	}
}

func TestExplicitAgentProhibitionIsRefused(t *testing.T) {
	for _, prompt := range []string{"Use no agents for this task", "Review this change, but do not use agents.", "Implement this without subagents"} {
		t.Run(prompt, func(t *testing.T) {
			env := suggestFixture(t)
			turn := delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: prompt})
			lead := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: env.root})
			tool, _ := env.e.Tools.Get("agent.propose_plan")
			if _, err := tool.Call(lead, json.RawMessage(onePlan)); err == nil {
				t.Fatal("agent plan started despite explicit prohibition")
			}
		})
	}
}

func TestAgentRequestSurvivesConfirmationTurn(t *testing.T) {
	env := suggestFixture(t)
	_ = requestedAgentLeadTurn(t, env)
	turn := delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "yes"})
	lead := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: env.root})
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	if _, err := tool.Call(lead, json.RawMessage(onePlan)); err != nil {
		t.Fatalf("confirmation lost the agent request: %v", err)
	}
}

func TestAgentRequestDoesNotSurviveNewUnrelatedTask(t *testing.T) {
	env := suggestFixture(t)
	_ = requestedAgentLeadTurn(t, env)
	_ = delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "New task: summarize the README"})
	turn := delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "yes"})
	lead := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: env.root})
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	if _, err := tool.Call(lead, json.RawMessage(onePlan)); err == nil {
		t.Fatal("stale agent request carried into an unrelated task")
	}
}

func TestUnadmittedPlanStartsTaskOnce(t *testing.T) {
	env := suggestFixture(t)
	lead := requestedAgentLeadTurn(t, env)
	tool, ok := env.e.Tools.Get("agent.propose_plan")
	if !ok {
		t.Fatal("agent.propose_plan unavailable")
	}
	if !env.e.permittedToolFor(lead, turnTool{tool: tool}, nil) {
		t.Fatal("ordinary lead cannot propose an agent plan")
	}
	out, err := tool.Call(lead, json.RawMessage(onePlan))
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ PlanID, State, Message string }
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.PlanID == "" || result.State != "started" {
		t.Fatalf("proposal result %q: %v", out, err)
	}
	plan, err := env.e.Store.AgentPlan(t.Context(), result.PlanID)
	if err != nil || plan.TaskID == "" || plan.State != protocol.PlanApproved {
		t.Fatalf("automatic plan = %+v, %v", plan, err)
	}
	approvals, err := env.e.Store.ListApprovals(t.Context(), "pending")
	if err != nil || len(approvals) != 0 {
		t.Fatalf("plan approvals = %+v, %v", approvals, err)
	}
	if _, err := env.e.Store.ActiveAgentTask(t.Context(), env.th.ID, env.th.ProjectID); err != nil {
		t.Fatal("agent task was not started")
	}
	if _, err := tool.Call(lead, json.RawMessage(onePlan)); err == nil {
		t.Fatal("second proposal in one turn was accepted")
	}
}

func TestRequestedPlanStartsWithoutApproval(t *testing.T) {
	env := suggestFixture(t)
	lead := requestedAgentLeadTurn(t, env)
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	out, err := tool.Call(lead, json.RawMessage(onePlan))
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ PlanID, State string }
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.PlanID == "" || result.State != "started" {
		t.Fatalf("automatic plan result %q: %v", out, err)
	}
	plan, err := env.e.Store.AgentPlan(t.Context(), result.PlanID)
	if err != nil || plan.State != protocol.PlanApproved || plan.TaskID == "" {
		t.Fatalf("plan did not start: %+v, %v", plan, err)
	}
	if pending, err := env.e.Store.ListApprovals(t.Context(), "pending"); err != nil || len(pending) != 0 {
		t.Fatalf("pending approval after automatic start: %+v, %v", pending, err)
	}
}

func TestRequestedPlanStartsInOrdinaryProject(t *testing.T) {
	env := suggestFixture(t)
	if err := os.RemoveAll(filepath.Join(env.root, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.root, "UMCODE.md"), []byte("test project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lead := requestedAgentLeadTurn(t, env)
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	out, err := tool.Call(lead, json.RawMessage(onePlan))
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ PlanID, State string }
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.State != "started" {
		t.Fatalf("ordinary project plan %q: %v", out, err)
	}
	plan, err := env.e.Store.AgentPlan(t.Context(), result.PlanID)
	if err != nil || plan.TaskID == "" {
		t.Fatalf("ordinary project task: %+v, %v", plan, err)
	}
	if _, err := os.Lstat(filepath.Join(env.root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("plan wrote Git metadata in project: %v", err)
	}
}

func TestAutomaticPlanValidationFailureCanRetry(t *testing.T) {
	env := suggestFixture(t)
	lead := requestedAgentLeadTurn(t, env)
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	bad := strings.Replace(onePlan, `"name":"worker"`, `"name":"worker","model":"not-in-pool"`, 1)
	if _, err := tool.Call(lead, json.RawMessage(bad)); err == nil {
		t.Fatal("invalid model was admitted")
	}
	if pending, err := env.e.Store.ListApprovals(t.Context(), "pending"); err != nil || len(pending) != 0 {
		t.Fatalf("invalid plan left a pending gate: %+v, %v", pending, err)
	}
	if _, err := tool.Call(lead, json.RawMessage(onePlan)); err != nil {
		t.Fatalf("valid retry refused: %v", err)
	}
}

func TestPlanApprovalAdmissionCapturesDirtyCheckout(t *testing.T) {
	env := suggestFixture(t)
	file := filepath.Join(env.root, "local-input.txt")
	if err := os.WriteFile(file, []byte("local instructions\n"), 0600); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(env.root, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	lead := requestedAgentLeadTurn(t, env)
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	out, err := tool.Call(lead, json.RawMessage(onePlan))
	if err != nil {
		t.Fatal(err)
	}
	var proposed struct {
		PlanID string `json:"planId"`
	}
	if err := json.Unmarshal([]byte(out), &proposed); err != nil {
		t.Fatal(err)
	}
	response, err := env.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: proposed.PlanID, Decision: protocol.PlanDecisionApprove})
	if err != nil {
		t.Fatal(err)
	}
	if response.TaskID == "" || response.Plan.State != protocol.PlanApproved || response.ApprovalStatus != "approved" {
		t.Fatalf("approval did not admit task: %+v", response)
	}
	// Simulate an engine restart losing its in-memory decision cache.
	env.e.agentPlanMu.Lock()
	env.e.agentPlanDecided = nil
	env.e.agentPlanMu.Unlock()
	replayed, err := env.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: proposed.PlanID, Decision: protocol.PlanDecisionApprove})
	if err != nil || replayed.TaskID != response.TaskID {
		t.Fatalf("replayed approval = %+v, %v", replayed, err)
	}
	card, err := env.e.GetAgentPlan(t.Context(), protocol.AgentPlanGetParams{PlanID: proposed.PlanID})
	if err != nil || card.Card == nil || card.Card.Limits.MaxConcurrent != card.Plan.Limits.MaxConcurrent {
		t.Fatalf("approved limits or card changed: %+v, %v", card, err)
	}
	task, err := env.e.Store.AgentTask(t.Context(), response.TaskID)
	if err != nil || task.Activation != protocol.AgentActivationPlanApproval || task.ParentRoot == env.root {
		t.Fatalf("task = %+v, %v", task, err)
	}
	data, err := os.ReadFile(filepath.Join(task.ParentRoot, "local-input.txt"))
	if err != nil || string(data) != "local instructions\n" {
		t.Fatalf("private workspace input = %q, %v", data, err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || string(indexAfter) != string(indexBefore) {
		t.Fatalf("selected index changed during approval: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := env.e.Store.AgentPlanContinuation(t.Context(), response.TaskID); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approved plan did not queue its continuation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	view, err := env.e.GetAgentPlan(t.Context(), protocol.AgentPlanGetParams{PlanID: proposed.PlanID})
	if err != nil || view.ContinuationNeeded {
		t.Fatalf("continuation status = %+v, %v", view, err)
	}
}

func TestAutomaticPlanKeepsSnapshotAfterCheckoutDrift(t *testing.T) {
	env := suggestFixture(t)
	lead := requestedAgentLeadTurn(t, env)
	tool, _ := env.e.Tools.Get("agent.propose_plan")
	out, err := tool.Call(lead, json.RawMessage(onePlan))
	if err != nil {
		t.Fatal(err)
	}
	var proposed struct {
		PlanID string `json:"planId"`
	}
	if err := json.Unmarshal([]byte(out), &proposed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.root, "new-since-card.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := env.e.Store.AgentPlan(t.Context(), proposed.PlanID)
	if err != nil || plan.TaskID == "" || plan.State != protocol.PlanApproved {
		t.Fatalf("plan after source drift = %+v, %v", plan, err)
	}
	task, err := env.e.Store.AgentTask(t.Context(), plan.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(task.ParentRoot, "new-since-card.txt")); !os.IsNotExist(err) {
		t.Fatalf("source drift entered the private snapshot: %v", err)
	}
}

func TestPromptFirstPlanCardThroughRealTurn(t *testing.T) {
	x := newActivationE2E(t, nil)
	x.setLead("Use two agents", e2eLeadStep{"agent.propose_plan", onePlan})
	x.turn(t, protocol.TurnStartParams{Text: "Use two agents"})
	list, err := x.e.Store.ListApprovals(t.Context(), "approved")
	if err != nil {
		t.Fatal(err)
	}
	var planID string
	for _, a := range list {
		if a.Kind == "agent_plan" {
			planID = a.NodeID
			break
		}
	}
	if planID == "" {
		t.Fatal("ordinary user turn did not start an agent plan")
	}
	if _, err := x.e.Store.ActiveAgentTask(t.Context(), x.th.ID, x.th.ProjectID); err != nil {
		t.Fatal("agent task did not start")
	}
	task, err := x.e.Store.ActiveAgentTask(t.Context(), x.th.ID, x.th.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		agents, err := x.e.Store.RuntimeAgents(t.Context(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(agents) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("automatic plan did not start a runtime agent")
}
