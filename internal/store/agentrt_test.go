package store

import (
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
	if err := s.UpdateRuntimeAgent(t.Context(), bad, 1); err == nil {
		t.Fatal("queued -> completed must fail")
	}
	a.State = protocol.AgentStateRunning
	if err := s.UpdateRuntimeAgent(t.Context(), a, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RuntimeAgent(t.Context(), task.ID, a.ID)
	if got.Version != 2 || got.State != protocol.AgentStateRunning {
		t.Fatalf("version=%d state=%s", got.Version, got.State)
	}
	if err := s.UpdateRuntimeAgent(t.Context(), a, 1); err == nil {
		t.Fatal("stale version must fail")
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
