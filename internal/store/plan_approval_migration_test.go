package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPlanApprovalMigrationPreservesExistingChildren(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	turn := protocol.Turn{ID: "upgrade-root", ThreadID: th.ID, Status: protocol.TurnCompleted, StartedAt: time.Now()}
	if err := s.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "upgrade-task", ThreadID: th.ID, RootTurnID: turn.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRevision: "base"}
	if err := s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	now := Now()
	if _, err := s.DB.Exec(`INSERT INTO agent_children(id,task_id,call_id,role,assignment_json,state,result_json,usage_json,created_at,updated_at,model_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		"upgrade-child", task.ID, "call", "implementer", `{}`, "queued", `{}`, `{}`, now, now, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reconstruct the database just before migration 41, with a populated
	// foreign-key child. Reopening must extend the CHECK without deleting it.
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrationsFS.ReadFile("migrations/0041_plan_approval_activation.sql")
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(string(body), ",'plan_approval'", "", 1)
	if oldSchema == string(body) {
		t.Fatal("migration activation CHECK was not found")
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version=41`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.AgentChild(t.Context(), task.ID, "upgrade-child"); err != nil {
		t.Fatalf("existing child lost during migration: %v", err)
	}
	var table, rowid, parent string
	var fkID int
	err = reopened.DB.QueryRow(`PRAGMA foreign_key_check`).Scan(&table, &rowid, &parent, &fkID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign key check: %s %s %s %d, %v", table, rowid, parent, fkID, err)
	}
}
