package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func runtimeFixture(t *testing.T) (*Store, protocol.AgentTask) {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	tr := protocol.Turn{ID: "root", ThreadID: th.ID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), tr); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "task", ThreadID: th.ID, RootTurnID: tr.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRevision: "base"}
	if err = s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	return s, task
}

func newRuntimeAgent(task protocol.AgentTask, name string) protocol.RuntimeAgent {
	return protocol.RuntimeAgent{TaskID: task.ID, Name: name, Purpose: "p", Assignment: protocol.AgentAssignment{Prompt: "do it"}}
}

func TestCreateRuntimeAgentIsIdempotentByCallID(t *testing.T) {
	s, task := runtimeFixture(t)
	a, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "one"), "c1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "other"), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.ID != b.ID {
		t.Fatalf("ids differ: %q %q", a.ID, b.ID)
	}
	all, err := s.RuntimeAgents(t.Context(), task.ID)
	if err != nil || len(all) != 1 {
		t.Fatalf("agents=%d err=%v", len(all), err)
	}
	if all[0].Version != 1 || all[0].State != protocol.AgentStateQueued {
		t.Fatalf("got version=%d state=%s", all[0].Version, all[0].State)
	}
	got, err := s.RuntimeAgentForCall(t.Context(), task.ID, "c1")
	if err != nil || got.ID != a.ID {
		t.Fatalf("for call: %v %v", got.ID, err)
	}
}

func TestUpdateRuntimeAgentChecksVersionAndTransition(t *testing.T) {
	s, task := runtimeFixture(t)
	a, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "one"), "c1")
	if err != nil {
		t.Fatal(err)
	}
	bad := a
	bad.State = protocol.AgentStateCompleted
	if err := s.UpdateRuntimeAgent(t.Context(), bad, 1); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("queued -> completed: want ErrAgentTransition, got %v", err)
	}
	a.State = protocol.AgentStateRunning
	if err := s.UpdateRuntimeAgent(t.Context(), a, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RuntimeAgent(t.Context(), task.ID, a.ID)
	if got.Version != 2 || got.State != protocol.AgentStateRunning {
		t.Fatalf("version=%d state=%s", got.Version, got.State)
	}
	if err := s.UpdateRuntimeAgent(t.Context(), a, 1); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("stale version: want ErrAgentConflict, got %v", err)
	}
	got, _ = s.RuntimeAgent(t.Context(), task.ID, a.ID)
	if got.Version != 2 {
		t.Fatalf("version=%d", got.Version)
	}
}

func TestRuntimeAgentJSONRoundTrip(t *testing.T) {
	s, task := runtimeFixture(t)
	in := newRuntimeAgent(task, "one")
	in.Capabilities = protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadOnly, Network: false, SpawnMax: 2}
	in.RouteTrail = []string{"a", "b"}
	in.BudgetShare = 0.25
	in.Depth = 1
	in.Assignment.AcceptanceChecks = []string{"go test"}
	a, err := s.CreateRuntimeAgent(t.Context(), in, "c1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RuntimeAgent(t.Context(), task.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Capabilities != in.Capabilities || got.BudgetShare != 0.25 || got.Depth != 1 ||
		len(got.RouteTrail) != 2 || got.RouteTrail[1] != "b" ||
		len(got.Assignment.AcceptanceChecks) != 1 || got.Assignment.AcceptanceChecks[0] != "go test" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestReconcileRuntimeAgentsInterruptsActive(t *testing.T) {
	s, task := runtimeFixture(t)
	ids := map[string]string{}
	for i, st := range []protocol.AgentState{protocol.AgentStateQueued, protocol.AgentStateRunning, protocol.AgentStateWaitingApproval, protocol.AgentStateCompleted} {
		a, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, string(st)), string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		path := map[protocol.AgentState][]protocol.AgentState{
			protocol.AgentStateRunning:         {protocol.AgentStateRunning},
			protocol.AgentStateWaitingApproval: {protocol.AgentStateRunning, protocol.AgentStateWaitingApproval},
			protocol.AgentStateCompleted:       {protocol.AgentStateRunning, protocol.AgentStateCompleted},
		}[st]
		v := int64(1)
		for _, next := range path {
			a.State = next
			if err := s.UpdateRuntimeAgent(t.Context(), a, v); err != nil {
				t.Fatal(err)
			}
			v++
		}
		ids[string(st)] = a.ID
	}
	if err := s.ReconcileRuntimeAgents(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]protocol.AgentState{
		"queued": protocol.AgentStateInterrupted, "running": protocol.AgentStateInterrupted,
		"waiting_approval": protocol.AgentStateInterrupted, "completed": protocol.AgentStateCompleted,
	} {
		got, err := s.RuntimeAgent(t.Context(), task.ID, ids[name])
		if err != nil || got.State != want {
			t.Fatalf("%s: state=%s err=%v want %s", name, got.State, err, want)
		}
	}
}

func savePlan(t *testing.T, s *Store, id, threadID string) protocol.AgentPlan {
	t.Helper()
	p := protocol.AgentPlan{ID: id, ThreadID: threadID, State: protocol.PlanProposed, Limits: protocol.DefaultPlanLimits(), CreatedAt: time.Now().UTC(),
		Agents: []protocol.PlanAgent{{Name: "w", Purpose: "p"}}}
	if err := s.SaveAgentPlan(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApproveAgentPlanSupersedesPreviousAndBindsTask(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, "A", task.ThreadID)
	savePlan(t, s, "B", task.ThreadID)
	if err := s.ApproveAgentPlan(t.Context(), "A", "T"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ApprovedAgentPlanForTask(t.Context(), "T"); err != nil || got.ID != "A" || got.ApprovedAt == nil || got.State != protocol.PlanApproved {
		t.Fatalf("A: %+v %v", got, err)
	}
	if err := s.ApproveAgentPlan(t.Context(), "B", "T2"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DB.QueryRowContext(t.Context(), `SELECT state FROM agent_plans WHERE id='A'`).Scan(&state); err != nil || state != string(protocol.PlanSuperseded) {
		t.Fatalf("A state=%q err=%v", state, err)
	}
	if got, err := s.ApprovedAgentPlanForTask(t.Context(), "T2"); err != nil || got.ID != "B" || len(got.Agents) != 1 || got.Limits.MaxTotal != 40 {
		t.Fatalf("B: %+v %v", got, err)
	}
	if _, err := s.ApprovedAgentPlanForTask(t.Context(), "T"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("T: want ErrNotFound, got %v", err)
	}
	if got, err := s.LatestApprovedAgentPlan(t.Context(), task.ThreadID); err != nil || got.ID != "B" {
		t.Fatalf("latest: %+v %v", got, err)
	}
	if err := s.ApproveAgentPlan(t.Context(), "missing", "T3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing plan: %v", err)
	}
}

func TestProposedPlanIsNotReturnedAsApproved(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, "P", task.ThreadID)
	if _, err := s.ApprovedAgentPlanForTask(t.Context(), task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("task getter: %v", err)
	}
	if _, err := s.LatestApprovedAgentPlan(t.Context(), task.ThreadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("thread getter: %v", err)
	}
}

func TestAgentMessagesDeliverOnceInOrder(t *testing.T) {
	s, task := runtimeFixture(t)
	var ms []protocol.AgentMessage
	for _, c := range [][2]string{{"X", "m1"}, {"X", "m2"}, {"Y", "m3"}} {
		m, err := s.EnqueueAgentMessage(t.Context(), protocol.AgentMessage{TaskID: task.ID, FromAgentID: "root", ToAgentID: c[0], Body: c[1]})
		if err != nil || m.ID == "" {
			t.Fatalf("enqueue: %+v %v", m, err)
		}
		ms = append(ms, m)
	}
	ids := func(to string) []string {
		got, err := s.PendingAgentMessages(t.Context(), task.ID, to)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, m := range got {
			out = append(out, m.Body)
		}
		return out
	}
	if g := ids("X"); len(g) != 2 || g[0] != "m1" || g[1] != "m2" {
		t.Fatalf("X=%v", g)
	}
	if err := s.AcknowledgeAgentMessage(t.Context(), task.ID, ms[0].ID); err != nil {
		t.Fatal(err)
	}
	if g := ids("X"); len(g) != 1 || g[0] != "m2" {
		t.Fatalf("X after ack=%v", g)
	}
	if err := s.AcknowledgeAgentMessage(t.Context(), task.ID, ms[0].ID); err != nil {
		t.Fatalf("re-ack: %v", err)
	}
	if g := ids("Y"); len(g) != 1 || g[0] != "m3" {
		t.Fatalf("Y=%v", g)
	}
}

func TestApproveAgentPlanStateGuards(t *testing.T) {
	s, task := runtimeFixture(t)
	savePlan(t, s, "A", task.ThreadID)
	savePlan(t, s, "B", task.ThreadID)
	if err := s.ApproveAgentPlan(t.Context(), "A", "T"); err != nil {
		t.Fatal(err)
	}
	first, _ := s.ApprovedAgentPlanForTask(t.Context(), "T")
	// idempotent repeat for the same task
	if err := s.ApproveAgentPlan(t.Context(), "A", "T"); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	again, err := s.ApprovedAgentPlanForTask(t.Context(), "T")
	if err != nil || again.ApprovedAt == nil || !again.ApprovedAt.Equal(*first.ApprovedAt) {
		t.Fatalf("approved_at changed: %+v %v", again, err)
	}
	if got, err := s.LatestApprovedAgentPlan(t.Context(), task.ThreadID); err != nil || got.ID != "A" {
		t.Fatalf("latest: %+v %v", got, err)
	}
	// different task conflicts and leaves the binding unchanged
	if err := s.ApproveAgentPlan(t.Context(), "A", "T9"); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("other task: %v", err)
	}
	if got, err := s.ApprovedAgentPlanForTask(t.Context(), "T"); err != nil || got.ID != "A" {
		t.Fatalf("binding: %+v %v", got, err)
	}
	if _, err := s.ApprovedAgentPlanForTask(t.Context(), "T9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("T9: %v", err)
	}
	// supersede A via B, then A cannot be resurrected
	if err := s.ApproveAgentPlan(t.Context(), "B", "T2"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveAgentPlan(t.Context(), "A", "T"); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("superseded: %v", err)
	}
	var state string
	if err := s.DB.QueryRowContext(t.Context(), `SELECT state FROM agent_plans WHERE id='A'`).Scan(&state); err != nil || state != string(protocol.PlanSuperseded) {
		t.Fatalf("A state=%q err=%v", state, err)
	}
	if got, err := s.LatestApprovedAgentPlan(t.Context(), task.ThreadID); err != nil || got.ID != "B" {
		t.Fatalf("latest: %+v %v", got, err)
	}
}

func TestAgentMessagesAckScopedToTaskAndStableOrder(t *testing.T) {
	s, task := runtimeFixture(t)
	for _, id := range []string{"z1", "a2"} {
		if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO agent_messages (id,task_id,from_agent_id,to_agent_id,body,created_at) VALUES(?,?,?,?,?,?)`,
			id, task.ID, "root", "X", id, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PendingAgentMessages(t.Context(), task.ID, "X")
	if err != nil || len(got) != 2 || got[0].ID != "z1" || got[1].ID != "a2" {
		t.Fatalf("order: %+v %v", got, err)
	}
	if err := s.AcknowledgeAgentMessage(t.Context(), "other-task", "z1"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PendingAgentMessages(t.Context(), task.ID, "X")
	if len(got) != 2 {
		t.Fatalf("foreign ack delivered message: %+v", got)
	}
}
