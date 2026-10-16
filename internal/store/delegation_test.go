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

// Catches lost recovery records, duplicate paid-work identities, cross-task
// reads/writes, and stale updates that overwrite a concurrent lifecycle change.
func TestAgentChildPersistenceOwnershipAndIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegation.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	task := agentTaskFixture(t, s, "task_one")
	other := agentTaskFixture(t, s, "task_two")
	child, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{ID: "child_one", TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "fix lifecycle", AcceptanceChecks: []string{"go test ./internal/store"}, ContextRefs: []string{"file:internal/store"}}}, "call_one")
	if err != nil {
		t.Fatal(err)
	}
	if child.Version != 1 || child.State != protocol.AgentStateQueued || child.CreatedAt.IsZero() || child.UpdatedAt.IsZero() {
		t.Fatalf("defaults: %+v", child)
	}
	duplicate, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{ID: "replacement", TaskID: task.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "different retry payload"}}, "call_one")
	if err != nil || !reflect.DeepEqual(child, duplicate) {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	foreign, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: other.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "review"}}, "call_one")
	if err != nil || foreign.ID == child.ID {
		t.Fatalf("task-scoped call: %+v, %v", foreign, err)
	}
	if _, err := s.AgentChild(t.Context(), other.ID, child.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign lookup: %v", err)
	}
	changed := child
	changed.TaskID = other.ID
	changed.State = protocol.AgentStateRunning
	if err := s.UpdateAgentChild(t.Context(), changed, child.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	child.State = protocol.AgentStateRunning
	child.WorkspaceID = "workspace_recoverable"
	child.Revision = "def456"
	child.Result = protocol.AgentResult{Summary: "partial", Findings: []protocol.AgentFinding{{Path: "store.go", Line: 12, Message: "check ownership", Severity: "high"}}, Checks: []protocol.AgentCheckEvidence{{Command: "go test", Status: "passed", Summary: "ok"}}, ChangedPaths: []string{"store.go"}, Blocker: "pending review"}
	child.Usage = protocol.UsageTotals{InputTokens: 42, OutputTokens: 7, Requests: 1, CostUSD: 0.25}
	if err := s.UpdateAgentChild(t.Context(), child, child.Version); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAgentChild(t.Context(), child, child.Version); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("stale update: %v", err)
	}
	s.Close()
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	gotTask, err := s.AgentTask(t.Context(), task.ID)
	if err != nil || gotTask.ThreadID != task.ThreadID || gotTask.RootTurnID != task.RootTurnID || gotTask.Activation != task.Activation || gotTask.BaseRevision != task.BaseRevision || gotTask.CreatedAt.IsZero() {
		t.Fatalf("task=%+v err=%v", gotTask, err)
	}
	got, err := s.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil || got.State != protocol.AgentStateRunning || got.Version != 2 || got.WorkspaceID != child.WorkspaceID || got.Revision != child.Revision || !reflect.DeepEqual(got.Assignment, child.Assignment) || !reflect.DeepEqual(got.Result, child.Result) || got.Usage != child.Usage {
		t.Fatalf("child=%+v err=%v", got, err)
	}
	children, err := s.AgentChildren(t.Context(), task.ID)
	if err != nil || len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("children=%+v err=%v", children, err)
	}
	if _, err := s.AgentChildren(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task list: %v", err)
	}
	if _, err := s.AgentTask(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task: %v", err)
	}
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
		c, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "recover"}}, fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		if state != protocol.AgentStateQueued {
			c.State = protocol.AgentStateRunning
			if err := s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
				t.Fatal(err)
			}
			c, _ = s.AgentChild(t.Context(), task.ID, c.ID)
		}
		c.State, c.WorkspaceID, c.Revision = state, "saved-workspace", "saved-revision"
		c.Result = protocol.AgentResult{Summary: "saved result", ChangedPaths: []string{"saved.go"}}
		c.Usage = protocol.UsageTotals{InputTokens: 99, CostUSD: 0.5}
		if err := s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
			t.Fatal(err)
		}
		c, _ = s.AgentChild(t.Context(), task.ID, c.ID)
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

// Catches invalid lifecycle jumps, role forgery, and correction-limit bypasses.
func TestAgentChildTransitionValidation(t *testing.T) {
	s, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := agentTaskFixture(t, s, "transitions")
	c, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "work"}}, "spawn")
	if err != nil {
		t.Fatal(err)
	}
	invalid := c
	invalid.State = protocol.AgentStateCompleted
	if err := s.UpdateAgentChild(t.Context(), invalid, c.Version); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("queued completed: %v", err)
	}
	invalid = c
	invalid.Role = protocol.AgentRoleReviewer
	if err := s.UpdateAgentChild(t.Context(), invalid, c.Version); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("role mutation: %v", err)
	}
	for _, rounds := range []int{-1, 3} {
		invalid = c
		invalid.CorrectionRounds = rounds
		if err := s.UpdateAgentChild(t.Context(), invalid, c.Version); !errors.Is(err, ErrAgentTransition) {
			t.Fatalf("rounds %d: %v", rounds, err)
		}
	}
	for _, state := range []protocol.AgentState{protocol.AgentStateRunning, protocol.AgentStateWaitingApproval, protocol.AgentStateRunning, protocol.AgentStateCompleted, protocol.AgentStateQueued, protocol.AgentStateRunning, protocol.AgentStateFailed, protocol.AgentStateQueued} {
		if state == protocol.AgentStateQueued {
			c.CorrectionRounds++
		}
		c.State = state
		if err := s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
		c, err = s.AgentChild(t.Context(), task.ID, c.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A third correction must not be hidden by retaining the old counter.
	c.State = protocol.AgentStateRunning
	if err := s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
		t.Fatal(err)
	}
	c, err = s.AgentChild(t.Context(), task.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.State = protocol.AgentStateFailed
	if err := s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
		t.Fatal(err)
	}
	c, err = s.AgentChild(t.Context(), task.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	invalid = c
	invalid.State = protocol.AgentStateQueued
	if err := s.UpdateAgentChild(t.Context(), invalid, c.Version); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("third correction accepted without increment: %v", err)
	}
	invalid = c
	invalid.CorrectionRounds = 1
	if err := s.UpdateAgentChild(t.Context(), invalid, c.Version); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("round reset: %v", err)
	}
	c, err = s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "cancel review"}}, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	c.State = protocol.AgentStateCancelled
	if err := s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
		t.Fatal(err)
	}
	c, _ = s.AgentChild(t.Context(), task.ID, c.ID)
	c.State = protocol.AgentStateQueued
	if err := s.UpdateAgentChild(t.Context(), c, c.Version); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("cancelled resumed: %v", err)
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
	for _, child := range []protocol.AgentChild{
		{TaskID: "missing", Role: protocol.AgentRoleImplementer},
		{TaskID: task.ID, Role: "unknown"},
		{TaskID: task.ID, Role: protocol.AgentRoleReviewer, State: protocol.AgentStateRunning},
	} {
		child.Assignment.Prompt = "invalid"
		if _, err := s.CreateAgentChild(t.Context(), child, "bad-call"); err == nil {
			t.Fatalf("invalid child accepted: %+v", child)
		}
	}
	if _, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "review"}}, ""); err == nil {
		t.Fatal("empty call ID accepted")
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
