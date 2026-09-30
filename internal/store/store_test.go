package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestLegacyBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "umcode.db")
	// Simulate a database created by the Python app.
	raw, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := migrationsFS.ReadFile("migrations/0001_baseline.sql")
	if _, err := raw.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, raw, `INSERT INTO sessions (id, connector, chat_id, channel, created_at) VALUES (7, 'web', 'admin', 'web', '2026-05-01T10:00:00+00:00')`)
	mustExec(t, raw, `INSERT INTO messages (session_id, role, content, created_at) VALUES (7, 'user', 'summarise my inbox please', '2026-05-01T10:00:01+00:00')`)
	mustExec(t, raw, `INSERT INTO messages (session_id, role, content, created_at) VALUES (7, 'assistant', 'You have 3 new emails', '2026-05-01T10:00:05+00:00')`)
	mustExec(t, raw, `INSERT INTO messages (session_id, role, content, created_at) VALUES (7, 'tool', '{}', '2026-05-01T10:00:03+00:00')`)
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	th, err := s.GetThread(ctx, "legacy-s7")
	if err != nil {
		t.Fatal(err)
	}
	if th.Title != "summarise my inbox please" || th.Channel != "web" {
		t.Fatalf("unexpected thread %+v", th)
	}
	items, err := s.ListItems(ctx, th.ID, 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	hits, err := s.SearchItems(ctx, "inbox", 10)
	if err != nil || len(hits) != 1 || hits[0].ThreadID != "legacy-s7" {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	// Re-opening must not re-run migrations.
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	items, _ = s2.ListItems(ctx, "legacy-s7", 0)
	if len(items) != 2 {
		t.Fatalf("re-open duplicated items: %d", len(items))
	}
}

func TestThreadsCredentialsUsage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a, _ := s.CreateThread(ctx, protocol.Thread{Title: "a"})
	time.Sleep(2 * time.Millisecond)
	b, _ := s.CreateThread(ctx, protocol.Thread{Title: "b"})
	if err := s.UpdateThread(ctx, a.ID, map[string]any{"approval_mode": "auto_workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberThreadDecision(ctx, a.ID, "shell.run", "npm test", "allow"); err != nil {
		t.Fatal(err)
	}
	if got := s.RememberedThreadDecision(ctx, a.ID, "shell.run", "npm test"); got != "allow" {
		t.Fatalf("chat-scoped decision = %q, want allow", got)
	}
	if got := s.RememberedThreadDecision(ctx, b.ID, "shell.run", "npm test"); got != "" {
		t.Fatalf("decision leaked across chats: %q", got)
	}
	a, err = s.GetThread(ctx, a.ID)
	if err != nil || a.ApprovalMode != "auto_workspace" {
		t.Fatalf("chat approval mode = %q, err=%v", a.ApprovalMode, err)
	}
	if err := s.UpdateThread(ctx, a.ID, map[string]any{"pinned": true}); err != nil {
		t.Fatal(err)
	}
	list, _, err := s.ListThreads(ctx, protocol.ThreadListParams{})
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("order wrong: %+v %v", list, err)
	}

	k1, err := s.CreateCredential(ctx, protocol.Credential{Provider: "openai", Label: "personal", Last4: "abcd"})
	if err != nil || !k1.IsDefault {
		t.Fatalf("first key should be default: %+v %v", k1, err)
	}
	k2, _ := s.CreateCredential(ctx, protocol.Credential{Provider: "openai", Label: "work", Last4: "wxyz", IsDefault: true})
	c1, _ := s.GetCredential(ctx, k1.ID)
	if c1.IsDefault || !k2.IsDefault {
		t.Fatal("default did not move to k2")
	}
	if err := s.DeleteCredential(ctx, k2.ID); err != nil {
		t.Fatal(err)
	}
	c1, _ = s.GetCredential(ctx, k1.ID)
	if !c1.IsDefault {
		t.Fatal("default should fall back to k1")
	}

	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.InsertUsage(ctx, UsageRecord{CredentialID: k1.ID, Provider: "openai", Model: "gpt-x", ThreadID: a.ID, TurnID: "t1",
			Usage: protocol.UsageTotals{InputTokens: 100, OutputTokens: 50, CostUSD: 0.01}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := s.UsageGrouped(ctx, protocol.UsageSummaryParams{GroupBy: "credential", From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil || len(rows) != 1 || total.InputTokens != 300 || total.Requests != 3 {
		t.Fatalf("usage rows=%+v total=%+v err=%v", rows, total, err)
	}
	th, _ := s.GetThread(ctx, a.ID)
	if th.Usage.OutputTokens != 150 {
		t.Fatalf("thread usage %+v", th.Usage)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func TestPythonTasksAreLeased(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A row written by the Python app (its timestamp format, no Go columns set).
	mustExec(t, s.DB, `INSERT INTO tasks (name, prompt, task_type, schedule_json, timezone, status, next_run_at, created_by, created_at, updated_at)
		VALUES ('py', 'do it', 'periodic', '{"frequency":"daily","time":"09:00"}', 'UTC', 'active', '2026-09-18T09:00:00Z', 'web', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
	now := time.Date(2026, 9, 18, 9, 0, 30, 0, time.UTC)
	due, err := s.LeaseDueTasks(ctx, now, time.Hour, 5)
	if err != nil || len(due) != 1 || due[0].Schedule.Time != "09:00" || due[0].NextRunAt == nil {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	// Leased: not returned again until the lease expires.
	if again, _ := s.LeaseDueTasks(ctx, now, time.Hour, 5); len(again) != 0 {
		t.Fatalf("leased twice: %+v", again)
	}
	run, _ := s.StartTaskRun(ctx, due[0].ID, "trn_1")
	next := now.Add(24 * time.Hour)
	if err := s.FinishTaskRun(ctx, run, due[0].ID, true, "ok", "", &next, false); err != nil {
		t.Fatal(err)
	}
	var nextS string
	s.DB.QueryRow(`SELECT next_run_at FROM tasks WHERE id = ?`, due[0].ID).Scan(&nextS)
	if nextS != "2026-09-19T09:00:30.000000Z" {
		t.Fatalf("next_run_at stored as %q", nextS)
	}
}
