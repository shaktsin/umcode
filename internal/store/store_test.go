package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Catches approvals finalized independently of their graph outcome, stale
// identities accepted, and partial commits after a database failure.
func TestDecideWorkflowApproval(t *testing.T) {
	for _, tc := range []struct{ name, status, kind, initial, want string }{
		{"approve", "approved", "decision", "proposed", "approved"},
		{"deny", "denied", "decision", "proposed", "rejected"},
		{"accept risk", "approved", "unknown", "open", "accepted_risk"},
		{"deny risk", "denied", "unknown", "open", "open"},
		{"timeout", "expired", "decision", "proposed", "proposed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, w, n, a := workflowApprovalFixture(t, tc.kind, tc.initial)
			rev, err := st.DecideWorkflowApproval(t.Context(), a.ID, tc.status, "user")
			if err != nil {
				t.Fatal(err)
			}
			d, err := st.GetWorkDetail(t.Context(), w.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantRevision := 2
			wantNodeRevision := 2
			if tc.status == "expired" {
				wantRevision = 1
				wantNodeRevision = 1
			}
			if rev != wantRevision || d.Work.Revision != wantRevision || d.Nodes[0].Status != tc.want || d.Nodes[0].Revision != wantNodeRevision {
				t.Fatalf("rev=%d graph=%+v nodes=%+v", rev, d.Work, d.Nodes)
			}
			if _, err := st.DecideWorkflowApproval(t.Context(), a.ID, tc.status, "again"); err == nil {
				t.Fatal("duplicate succeeded")
			}
			_ = n
		})
	}
	for _, mode := range []string{"stale", "foreign thread", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			st, w, n, a := workflowApprovalFixture(t, "decision", "proposed")
			if mode == "stale" {
				if _, err := st.DB.Exec(`UPDATE work_nodes SET revision=2 WHERE id=?`, n.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "foreign thread" {
				if _, err := st.DB.Exec(`UPDATE approvals SET thread_id='other' WHERE id=?`, a.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "rollback" {
				mustExec(t, st.DB, `CREATE TRIGGER approval_crash BEFORE UPDATE ON approvals BEGIN SELECT RAISE(ABORT,'injected'); END`)
			}
			if _, err := st.DecideWorkflowApproval(t.Context(), a.ID, "approved", "user"); err == nil {
				t.Fatal("invalid outcome committed")
			}
			d, _ := st.GetWorkDetail(t.Context(), w.ID)
			approvals, _ := st.ListApprovals(t.Context(), "")
			if d.Work.Revision != 1 || d.Nodes[0].Status != "proposed" || approvals[0].Status != "pending" {
				t.Fatalf("partial outcome: %+v %+v", d, approvals)
			}
		})
	}
}

func workflowApprovalFixture(t *testing.T, kind, status string) (*Store, protocol.Work, protocol.WorkNode, protocol.Approval) {
	t.Helper()
	st, th := workFixture(t)
	w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, WorkflowDepth: "designed"})
	if err != nil {
		t.Fatal(err)
	}
	content := json.RawMessage(`{"required":true,"gate_kind":"security"}`)
	if kind == "unknown" {
		content = json.RawMessage(`{"blocking":true}`)
	}
	n, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: kind, Status: status, Title: "gate", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	a := protocol.Approval{ID: NewID("apr"), Kind: "workflow", ThreadID: th.ID, WorkID: w.ID, NodeID: n.ID, NodeRevision: 1, Status: "pending", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.CreateApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	return st, w, n, a
}

func TestDecideWorkflowApprovalVerifiesOnlyLinkedAcceptance(t *testing.T) {
	for _, status := range []string{"approved", "denied", "expired"} {
		t.Run(status, func(t *testing.T) {
			st, w, n, a := workflowApprovalFixture(t, "decision", "proposed")
			for _, id := range []string{"linked", "unrelated", "executable"} {
				command := "workflow:approval"
				if id == "executable" {
					command = "go test ./..."
				}
				content, _ := json.Marshal(map[string]string{"command": command})
				if _, err := st.AddWorkNode(t.Context(), protocol.WorkNode{ID: id, WorkID: w.ID, Kind: "criterion", Status: "pending", Content: content}); err != nil {
					t.Fatal(err)
				}
				if id != "unrelated" {
					if err := st.AddWorkEdge(t.Context(), protocol.WorkEdge{WorkID: w.ID, FromNodeID: id, ToNodeID: n.ID, Relation: "verifies"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := st.DecideWorkflowApproval(t.Context(), a.ID, status, "user"); err != nil {
				t.Fatal(err)
			}
			d, err := st.GetWorkDetail(t.Context(), w.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range d.Nodes {
				if node.Kind != "criterion" {
					continue
				}
				want := "pending"
				if status == "approved" && node.ID == "linked" {
					want = "passed"
				}
				if node.Status != want {
					t.Fatalf("criterion %s=%s want %s", node.ID, node.Status, want)
				}
			}
			if status == "approved" {
				if len(d.Attempts) != 1 || d.Attempts[0].CriterionNodeID != "linked" || d.Attempts[0].Status != "passed" || len(d.Evidence) != 1 || d.Evidence[0].SourceURI != "approval://"+a.ID || d.Evidence[0].SourceRevision != n.ID+":1" {
					t.Fatalf("exact approval provenance missing: %+v", d)
				}
			} else if len(d.Attempts) != 0 || len(d.Evidence) != 0 {
				t.Fatal("nonapproval forged acceptance")
			}
		})
	}
}

func TestDecideWorkflowApprovalDerivesDependentsAtomically(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approve], func(t *testing.T) {
			st, w, n, a := workflowApprovalFixture(t, "decision", "proposed")
			opt, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "option", Status: "active", Content: json.RawMessage(`{"solution_rung":1}`)})
			if err != nil {
				t.Fatal(err)
			}
			task, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "task", Status: "pending"})
			if err != nil {
				t.Fatal(err)
			}
			for _, edge := range []protocol.WorkEdge{{WorkID: w.ID, FromNodeID: n.ID, Relation: "selects", ToNodeID: opt.ID}, {WorkID: w.ID, FromNodeID: task.ID, Relation: "implements", ToNodeID: n.ID}} {
				if err := st.AddWorkEdge(t.Context(), edge); err != nil {
					t.Fatal(err)
				}
			}
			status, want := "denied", "blocked"
			if approve {
				status, want = "approved", "ready"
			}
			if _, err := st.DecideWorkflowApproval(t.Context(), a.ID, status, "user"); err != nil {
				t.Fatal(err)
			}
			d, _ := st.GetWorkDetail(t.Context(), w.ID)
			for _, node := range d.Nodes {
				if node.ID == task.ID && (node.Status != want || node.Revision != 2) {
					t.Fatalf("dependent=%+v", node)
				}
			}
			if d.Work.Revision != 2 {
				t.Fatalf("revision=%d", d.Work.Revision)
			}
		})
	}
}

func TestDecideWorkflowApprovalRiskDependentsAndStaleExpiry(t *testing.T) {
	for _, status := range []string{"approved", "denied", "expired"} {
		t.Run(status, func(t *testing.T) {
			st, w, n, a := workflowApprovalFixture(t, "unknown", "open")
			task, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "task", Status: "pending"})
			if err != nil {
				t.Fatal(err)
			}
			if status == "expired" {
				if err := st.UpdateWorkNode(t.Context(), n.ID, "open", 2, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			rev, err := st.DecideWorkflowApproval(t.Context(), a.ID, status, "user")
			if err != nil {
				t.Fatal(err)
			}
			d, _ := st.GetWorkDetail(t.Context(), w.ID)
			want := "ready"
			if status == "denied" {
				want = "blocked"
			}
			if status == "expired" {
				want = "pending"
			}
			for _, node := range d.Nodes {
				if node.ID == task.ID && node.Status != want {
					t.Fatalf("dependent=%+v", node)
				}
			}
			if status == "expired" && (rev != 1 || d.Work.Revision != 1) {
				t.Fatalf("expiry mutated work: %+v", d.Work)
			}
		})
	}
}

func TestDecideWorkflowApprovalDirectProposedIdentity(t *testing.T) {
	for _, content := range []string{`{}`, `{"required":false,"gate_kind":"security"}`, `{"required":true,"gate_kind":"forged"}`} {
		t.Run(content, func(t *testing.T) {
			st, _, n, a := workflowApprovalFixture(t, "decision", "proposed")
			if _, err := st.DB.Exec(`UPDATE work_nodes SET content_json=? WHERE id=?`, content, n.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DecideWorkflowApproval(t.Context(), a.ID, "approved", "user"); err == nil {
				t.Fatal("forged approval identity accepted")
			}
		})
	}
}

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
