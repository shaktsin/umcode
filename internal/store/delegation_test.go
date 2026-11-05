package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func agentTaskFixture(t *testing.T, s *Store, id string) protocol.AgentTask {
	t.Helper()
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: id})
	if err != nil {
		t.Fatal(err)
	}
	turn := protocol.Turn{ID: "turn_" + id, ThreadID: th.ID, Status: protocol.TurnCompleted, StartedAt: time.Now()}
	if err := s.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: id, ThreadID: th.ID, RootTurnID: turn.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRevision: "abc123"}
	if err := s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

// Catches recovery that restarts paid work, loses artifacts or changes terminal
// children, and repeated reconciliation that invalidates already recovered rows.
func TestReconcileAgentChildrenPreservesArtifacts(t *testing.T) {
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := agentTaskFixture(t, s, "recovery")
	states := []protocol.AgentState{protocol.AgentStateQueued, protocol.AgentStateRunning, protocol.AgentStateWaitingApproval, protocol.AgentStateCompleted, protocol.AgentStateFailed, protocol.AgentStateCancelled, protocol.AgentStateInterrupted}
	before := map[string]protocol.AgentChild{}
	for i, state := range states {
		id := fmt.Sprint(i)
		c := seedLegacyChild(t, s, task, id, state)
		if _, err := s.DB.ExecContext(t.Context(), `UPDATE agent_children SET workspace_id='saved-workspace',revision='saved-revision',result_json='{"summary":"saved result","changedPaths":["saved.go"]}',usage_json='{"inputTokens":99,"costUsd":0.5}' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		c, _ = s.AgentChild(t.Context(), task.ID, id)
		before[c.ID] = c
	}
	if err := s.ReconcileAgentChildren(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := s.AgentChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("recovery lost child rows: got %d, want %d", len(after), len(before))
	}
	for _, got := range after {
		old := before[got.ID]
		active := old.State == protocol.AgentStateQueued || old.State == protocol.AgentStateRunning || old.State == protocol.AgentStateWaitingApproval
		if active {
			if got.State != protocol.AgentStateInterrupted || got.Version != old.Version+1 {
				t.Fatalf("unreconciled: %+v", got)
			}
		} else if got.State != old.State || got.Version != old.Version {
			t.Fatalf("terminal child changed: %+v", got)
		}
		if got.WorkspaceID != old.WorkspaceID || got.Revision != old.Revision || !reflect.DeepEqual(got.Result, old.Result) || got.Usage != old.Usage || !got.CreatedAt.Equal(old.CreatedAt) {
			t.Fatalf("lost artifacts: %+v", got)
		}
	}
	if err := s.ReconcileAgentChildren(t.Context()); err != nil {
		t.Fatal(err)
	}
	repeated, err := s.AgentChildren(t.Context(), task.ID)
	if err != nil || !reflect.DeepEqual(after, repeated) {
		t.Fatalf("repeated recovery changed rows: %v", err)
	}
}

// Catches broken migration of existing history and missing ownership constraints.
func TestAgentDelegationMigrationAndOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		if version >= 22 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, string(body))
		if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (?, ?, ?)`, version, entry.Name(), Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO threads(id,title,created_at,updated_at) VALUES('old-thread','existing history',?,?)`, Now(), Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var title string
	if err := s.DB.QueryRow(`SELECT title FROM threads WHERE id='old-thread'`).Scan(&title); err != nil || title != "existing history" {
		t.Fatalf("legacy title=%q err=%v", title, err)
	}
	task := agentTaskFixture(t, s, "migrated")
	other := agentTaskFixture(t, s, "foreign")
	bad := task
	bad.ID, bad.ThreadID = "bad-task", other.ThreadID
	if err := s.CreateAgentTask(t.Context(), bad); err == nil {
		t.Fatal("foreign root turn accepted")
	}
	bad = task
	bad.ID, bad.Activation = "inactive-task", ""
	if err := s.CreateAgentTask(t.Context(), bad); err == nil {
		t.Fatal("unactivated task accepted")
	}
	bad = task
	bad.ID, bad.ProjectID = "foreign-project", "unowned-project"
	if err := s.CreateAgentTask(t.Context(), bad); err == nil {
		t.Fatal("foreign project accepted")
	}
	if _, err := s.DB.Exec(`INSERT INTO agent_children(id,task_id,call_id,role,state,assignment_json,result_json,usage_json,created_at,updated_at) VALUES('orphan','missing','call','implementer','queued','{}','{}','{}',?,?)`, Now(), Now()); err == nil {
		t.Fatal("database accepts orphan child")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=22`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
}

// A UI read before first execution must never initialize an unlimited ledger.
func TestAgentBudgetReadDoesNotInitialize(t *testing.T) {
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "read.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := agentTaskFixture(t, s, "read")
	if _, _, err := s.ReadAgentTaskBudget(t.Context(), task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ledger: %v", err)
	}
	limits, _, err := s.AgentTaskBudget(t.Context(), task.ID, protocol.ExecutionLimits{MaxTokens: 42})
	if err != nil || limits.MaxTokens != 42 {
		t.Fatalf("UI poisoned limits: %+v %v", limits, err)
	}
	limits, _, err = s.ReadAgentTaskBudget(t.Context(), task.ID)
	if err != nil || limits.MaxTokens != 42 {
		t.Fatalf("read: %+v %v", limits, err)
	}
}
