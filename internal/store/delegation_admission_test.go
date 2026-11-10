package store

import (
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"testing"
	"time"
)

// Ownership/CAS checks prevent another chat or a stale coordinator reopening permission.
func TestAgentTaskAdmissionStateOwnership(t *testing.T) {
	s, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := agentTaskFixture(t, s, "admission_task")
	if err = s.UpdateAgentTaskState(t.Context(), task.ID, "foreign", task.ProjectID, task.State, protocol.AgentStateCancelled); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign chat state write: %v", err)
	}
	if err = s.UpdateAgentTaskState(t.Context(), task.ID, task.ThreadID, "foreign", task.State, protocol.AgentStateCancelled); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign project state write: %v", err)
	}
	if err = s.UpdateAgentTaskState(t.Context(), task.ID, task.ThreadID, task.ProjectID, protocol.AgentStateQueued, protocol.AgentStateCancelled); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("stale state write: %v", err)
	}
	if _, err = s.ActiveAgentTask(t.Context(), task.ThreadID, task.ProjectID); err != nil {
		t.Fatal(err)
	}
	second := protocol.Turn{ID: "second_root", ThreadID: task.ThreadID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	duplicate := task
	duplicate.ID = "second_task"
	duplicate.RootTurnID = second.ID
	if err = s.CreateAgentTask(t.Context(), duplicate); err == nil {
		t.Fatal("second active root accepted")
	}
	if err = s.UpdateAgentTaskState(t.Context(), task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActiveAgentTask(t.Context(), task.ThreadID, task.ProjectID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("terminal task still active: %v", err)
	}
	if err = s.UpdateAgentTaskState(t.Context(), task.ID, task.ThreadID, task.ProjectID, protocol.AgentStateCompleted, protocol.AgentStateRunning); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("terminal reactivation: %v", err)
	}
	if err = s.CreateAgentTask(t.Context(), duplicate); err != nil {
		t.Fatalf("new task after terminal denied: %v", err)
	}
}

func TestAgentTaskBaseRootMigrationAndPersistence(t *testing.T) {
	path := t.TempDir() + "/admission.db"
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	task := agentTaskFixture(t, s, "legacy_root")
	if _, err = s.DB.Exec(`ALTER TABLE agent_tasks DROP COLUMN base_root`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM schema_migrations WHERE version=23`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentTask(t.Context(), task.ID)
	if err != nil || got.BaseRoot != "" || got.RootTurnID != task.RootTurnID {
		t.Fatalf("legacy root migration: %+v %v", got, err)
	}
	if err = s.UpdateAgentTaskState(t.Context(), task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCompleted); err != nil {
		t.Fatal(err)
	}
	next := protocol.Turn{ID: "root_persisted", ThreadID: task.ThreadID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	task.ID = "task_persisted"
	task.RootTurnID = next.ID
	task.BaseRoot = "/committed/project"
	if err = s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err = s.AgentTask(t.Context(), task.ID)
	if err != nil || got.BaseRoot != "/committed/project" {
		t.Fatalf("root lost after reopen: %+v %v", got, err)
	}
}
