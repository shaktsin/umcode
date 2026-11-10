package store

import (
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

// seedLegacyChild creates a historical row without reopening the retired
// child-writing API. Runtime agents use separate rows in agents.
func seedLegacyChild(t *testing.T, s *Store, task protocol.AgentTask, id string, state protocol.AgentState, roles ...string) protocol.AgentChild {
	t.Helper()
	role := "implementer"
	if len(roles) != 0 {
		role = roles[0]
	}
	_, err := s.DB.ExecContext(t.Context(), `INSERT INTO agent_children
 (id,task_id,call_id,role,assignment_json,state,revision,workspace_id,result_json,usage_json,version,created_at,updated_at,model_json)
 VALUES (?,?,?,?,'{"prompt":"historical work"}',?,'','','{}','{}',1,?,?, '{}')`,
		id, task.ID, "legacy:"+id, role, state, Now(), Now())
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.AgentChild(t.Context(), task.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func TestMigration0038InterruptsOnlyUnfinishedLegacyDelegation(t *testing.T) {
	s, runtimeTask := runtimeFixture(t)
	legacyTask := agentTaskFixture(t, s, "legacy-task")
	child := seedLegacyChild(t, s, legacyTask, "old-child", protocol.AgentStateRunning)
	completed := seedLegacyChild(t, s, legacyTask, "done-child", protocol.AgentStateCompleted)
	_, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(runtimeTask, "runtime"), "runtime-call")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version=38`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.AgentTask(t.Context(), legacyTask.ID)
	if err != nil || legacy.State != protocol.AgentStateInterrupted {
		t.Fatalf("legacy task = %s, %v", legacy.State, err)
	}
	old, err := s.AgentChild(t.Context(), legacyTask.ID, child.ID)
	if err != nil || old.State != protocol.AgentStateInterrupted {
		t.Fatalf("legacy child = %s, %v", old.State, err)
	}
	old, err = s.AgentChild(t.Context(), legacyTask.ID, completed.ID)
	if err != nil || old.State != protocol.AgentStateCompleted {
		t.Fatalf("completed history = %s, %v", old.State, err)
	}
	active, err := s.AgentTask(t.Context(), runtimeTask.ID)
	if err != nil || active.State != protocol.AgentStateRunning {
		t.Fatalf("runtime task = %s, %v", active.State, err)
	}
}

func TestReconcileLegacyChildrenLeavesRuntimeTaskToRuntimeRecovery(t *testing.T) {
	s, task := runtimeFixture(t)
	a, err := s.CreateRuntimeAgent(t.Context(), newRuntimeAgent(task, "running"), "runtime-call")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureRuntimeAgentExecution(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileAgentChildren(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentTask(t.Context(), task.ID)
	if err != nil || got.State != protocol.AgentStateRunning {
		t.Fatalf("runtime task = %s, %v", got.State, err)
	}
	var mirrorState string
	if err := s.DB.QueryRowContext(t.Context(), `SELECT state FROM agent_children WHERE id=?`, a.ID).Scan(&mirrorState); err != nil || mirrorState != string(protocol.AgentStateQueued) {
		t.Fatalf("runtime mirror = %s, %v", mirrorState, err)
	}
}
