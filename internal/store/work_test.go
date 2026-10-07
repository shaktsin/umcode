package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func workFixture(t *testing.T) (*Store, protocol.Thread) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	th, err := st.CreateThread(ctx, protocol.Thread{Title: "w"})
	if err != nil {
		t.Fatal(err)
	}
	return st, th
}

func TestWorkMigrationAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Kind: "task", Goal: "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != protocol.WorkOpen || w.WorkflowDepth != protocol.DepthDirect || w.ID == "" {
		t.Fatalf("defaults = %+v", w)
	}
	now := time.Now().UTC()
	goal, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeGoal, Title: "ship it", Status: "active", ValidFrom: now})
	if err != nil {
		t.Fatal(err)
	}
	crit, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeCriterion, Title: "unit",
		Content: json.RawMessage(`{"command":"go test ./..."}`), Status: "pending", ValidFrom: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddWorkEdge(ctx, protocol.WorkEdge{WorkID: w.ID, FromNodeID: goal.ID, Relation: protocol.RelRequires, ToNodeID: crit.ID}); err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, NodeID: crit.ID, Kind: protocol.EvidenceVerificationOutput, Summary: "FAIL", ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	code := 1
	if _, err := st.AddVerificationAttempt(ctx, protocol.VerificationAttempt{WorkID: w.ID, CriterionNodeID: crit.ID, CheckType: "command",
		Command: "go test ./...", Status: protocol.AttemptFailed, ExitCode: &code, EvidenceID: ev.ID, StartedAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetWorkDetail(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Nodes) != 2 || len(d.Edges) != 1 || len(d.Evidence) != 1 || len(d.Attempts) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	a := d.Attempts[0]
	if a.CriterionNodeID != crit.ID || a.ExitCode == nil || *a.ExitCode != 1 || a.Status != protocol.AttemptFailed || a.EvidenceID != ev.ID {
		t.Fatalf("attempt = %+v", a)
	}
	if string(d.Nodes[1].Content) != `{"command":"go test ./..."}` || d.Edges[0].Relation != protocol.RelRequires {
		t.Fatalf("nodes/edges = %+v %+v", d.Nodes, d.Edges)
	}
}

func TestOpenWorkForThread(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	if _, ok, err := st.OpenWorkForThread(ctx, th.ID); err != nil || ok {
		t.Fatalf("no work: ok=%v err=%v", ok, err)
	}
	w, _ := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "g"})
	if got, ok, err := st.OpenWorkForThread(ctx, th.ID); err != nil || !ok || got.ID != w.ID {
		t.Fatalf("open work: %+v ok=%v err=%v", got, ok, err)
	}
	if err := st.CloseWork(ctx, w.ID, protocol.WorkCompleted, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.OpenWorkForThread(ctx, th.ID); ok {
		t.Fatal("completed work still open")
	}
	d, _ := st.GetWorkDetail(ctx, w.ID)
	if d.Work.Status != protocol.WorkCompleted || d.Work.CompletedAt == nil {
		t.Fatalf("closed work = %+v", d.Work)
	}
}

func TestAbandonOpenWorksOnlyOpen(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	done, _ := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "done"})
	st.CloseWork(ctx, done.ID, protocol.WorkCompleted, time.Now().UTC())
	open, _ := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "open"})
	if err := st.AbandonOpenWorks(ctx, th.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetWorkDetail(ctx, done.ID)
	b, _ := st.GetWorkDetail(ctx, open.ID)
	if a.Work.Status != protocol.WorkCompleted || b.Work.Status != protocol.WorkAbandoned {
		t.Fatalf("statuses = %s, %s", a.Work.Status, b.Work.Status)
	}
}

func TestDeleteThreadCascadesWorks(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	w, _ := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "g"})
	n, _ := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeGoal, Title: "g", ValidFrom: time.Now().UTC()})
	ev, _ := st.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, NodeID: n.ID, Kind: protocol.EvidenceToolError, ObservedAt: time.Now().UTC()})
	st.AddVerificationAttempt(ctx, protocol.VerificationAttempt{WorkID: w.ID, CheckType: "command", Command: "x", Status: protocol.AttemptPassed, EvidenceID: ev.ID, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()})
	if err := st.DeleteThread(ctx, th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorkDetail(ctx, w.ID); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, table := range []string{"work_nodes", "evidence", "verification_attempts", "work_edges"} {
		var c int
		if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&c); err != nil || c != 0 {
			t.Fatalf("%s rows = %d err = %v", table, c, err)
		}
	}
}

func TestNoProjectWorkAllowed(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetWorkDetail(ctx, w.ID)
	if d.Work.ProjectID != "" {
		t.Fatalf("project id = %q", d.Work.ProjectID)
	}
	var isNull int
	st.DB.QueryRowContext(ctx, `SELECT project_id IS NULL FROM works WHERE id = ?`, w.ID).Scan(&isNull)
	if isNull != 1 {
		t.Fatal("project_id was not stored as NULL")
	}
}

func TestAttemptsOrderedAndAppendOnly(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	w, _ := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "g"})
	at := time.Now().UTC()
	for _, s := range []string{protocol.AttemptFailed, protocol.AttemptBlocked, protocol.AttemptPassed} {
		if _, err := st.AddVerificationAttempt(ctx, protocol.VerificationAttempt{WorkID: w.ID, CheckType: "command", Command: "c", Status: s, StartedAt: at, FinishedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := st.GetWorkDetail(ctx, w.ID)
	if len(d.Attempts) != 3 || d.Attempts[0].Status != protocol.AttemptFailed || d.Attempts[1].Status != protocol.AttemptBlocked || d.Attempts[2].Status != protocol.AttemptPassed {
		t.Fatalf("attempts = %+v", d.Attempts)
	}
}

// designedLegacyFixture applies exactly the pre-Designed migrations and inserts
// populated Phase 2 records without relying on the current Work scan contract.
func designedLegacyFixture(t *testing.T) (*Store, protocol.Thread) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite3", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	st := &Store{DB: db, Path: ":memory:"}
	t.Cleanup(func() { st.Close() })
	mustExec(t, db, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
			t.Fatal(err)
		}
		if version > 14 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, string(body))
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?, ?, ?)`, version, entry.Name(), Now()); err != nil {
			t.Fatal(err)
		}
	}
	th, err := st.CreateThread(ctx, protocol.Thread{Title: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	at := "2026-10-07T12:00:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO works (id, thread_id, kind, status, workflow_depth, goal, created_at, completed_at)
		VALUES ('wrk_old', ?, 'task', 'completed', 'guided', 'old goal', ?, ?)`, th.ID, at, at); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO work_nodes (id, work_id, kind, title, content_json, status, confidence, revision, valid_from,
		valid_until, superseded_by, created_at, updated_at) VALUES
		('wnd_old_goal', 'wrk_old', 'goal', 'old goal', '{"note":"kept"}', 'active', 0.8, 3, '2026-10-07T12:00:00Z', NULL, '', '2026-10-07T12:00:00Z', '2026-10-07T12:01:00Z'),
		('wnd_old_criterion', 'wrk_old', 'criterion', 'old check', '{"command":"go test ./..."}', 'passed', 1, 2, '2026-10-07T12:00:00Z', '2026-10-07T12:01:00Z', 'wnd_replacement', '2026-10-07T12:00:00Z', '2026-10-07T12:01:00Z')`)
	mustExec(t, db, `INSERT INTO work_edges VALUES ('wrk_old', 'wnd_old_goal', 'requires', 'wnd_old_criterion')`)
	mustExec(t, db, `INSERT INTO evidence (id, work_id, node_id, kind, source_uri, source_revision, content_hash, summary, confidence, observed_at, stale_at)
		VALUES ('evd_old', 'wrk_old', 'wnd_old_criterion', 'verification_output', 'shell://test', 'abc', 'hash', 'PASS', 0.9, '2026-10-07T12:00:00Z', NULL)`)
	mustExec(t, db, `INSERT INTO verification_attempts (id, work_id, criterion_node_id, check_type, command, environment_json, status, exit_code, evidence_id, started_at, finished_at)
		VALUES ('vat_old', 'wrk_old', 'wnd_old_criterion', 'command', 'go test ./...', '{"os":"darwin"}', 'passed', 0, 'evd_old', '2026-10-07T12:00:00Z', '2026-10-07T12:01:00Z')`)
	mustExec(t, db, `INSERT INTO approvals (id, thread_id, turn_id, item_id, tool, args_json, risk, reason, action_summary, status, decided_by, created_at, expires_at, decided_at)
		VALUES ('apr_old', 'thr_old', 'trn_old', 'itm_old', 'shell.run', '{"command":"go test ./..."}', 'yellow', 'old reason', 'old summary', 'approved', 'user', '2026-10-07T12:00:00Z', '2026-10-07T12:30:00Z', '2026-10-07T12:01:00Z')`)
	return st, th
}

func TestDesignedWorkflowMigrationAndRoundTrip(t *testing.T) {
	for _, upgraded := range []bool{false, true} {
		t.Run(fmt.Sprintf("upgraded_%v", upgraded), func(t *testing.T) {
			ctx := context.Background()
			var st *Store
			var th protocol.Thread
			if upgraded {
				st, th = designedLegacyFixture(t)
				if err := st.migrate(ctx); err != nil {
					t.Fatal(err)
				}
				old, err := st.GetWorkDetail(ctx, "wrk_old")
				if err != nil || old.Work.Revision != 1 {
					t.Fatalf("old revision = %d, err=%v", old.Work.Revision, err)
				}
			} else {
				st, th = workFixture(t)
			}
			w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "new design", WorkflowDepth: protocol.DepthDesigned, Revision: 7})
			if err != nil {
				t.Fatal(err)
			}
			if w.Revision != 7 {
				t.Fatalf("created revision = %d, want 7", w.Revision)
			}
			n, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeDecision, Title: "choose", Status: "proposed"})
			if err != nil {
				t.Fatal(err)
			}
			var evidenceIDs []string
			for _, id := range []string{"evd_a", "evd_b"} {
				ev, err := st.AddEvidence(ctx, protocol.Evidence{ID: id, WorkID: w.ID, Kind: protocol.EvidenceVerificationOutput, Summary: "ok"})
				if err != nil {
					t.Fatal(err)
				}
				evidenceIDs = append(evidenceIDs, ev.ID)
				if _, err := st.DB.ExecContext(ctx, `INSERT INTO work_node_evidence (work_id, node_id, evidence_id) VALUES (?, ?, ?)`, w.ID, n.ID, ev.ID); err != nil {
					t.Fatal(err)
				}
			}
			d, err := st.GetWorkDetail(ctx, w.ID)
			if err != nil {
				t.Fatal(err)
			}
			if d.Work.Revision != 7 || len(d.Nodes) != 1 || !reflect.DeepEqual(d.Nodes[0].EvidenceIDs, evidenceIDs) {
				t.Fatalf("detail = %+v", d)
			}
			listed, err := st.ListWorks(ctx, th.ID)
			if err != nil || len(listed) == 0 || listed[0].Revision != 7 {
				t.Fatalf("list = %+v, err=%v", listed, err)
			}
			open, ok, err := st.OpenWorkForThread(ctx, th.ID)
			if err != nil || !ok || open.Revision != 7 {
				t.Fatalf("open = %+v, ok=%v, err=%v", open, ok, err)
			}
			defaultWork, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "default revision"})
			if err != nil || defaultWork.Revision != 1 {
				t.Fatalf("default = %+v, err=%v", defaultWork, err)
			}
			if _, err := st.DB.ExecContext(ctx, `INSERT INTO work_node_evidence VALUES (?, ?, ?)`, w.ID, n.ID, evidenceIDs[0]); err == nil {
				t.Fatal("duplicate evidence link accepted")
			}
			if err := st.DeleteThread(ctx, th.ID); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_node_evidence`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("evidence links after cascade = %d, err=%v", count, err)
			}
		})
	}
}

func TestDesignedWorkflowMigrationPreservesExistingWork(t *testing.T) {
	ctx := context.Background()
	st, _ := designedLegacyFixture(t)
	// Snapshot every old column, including NULLs, before applying migration 0015.
	queries := map[string]string{}
	before := map[string][][]any{}
	for _, table := range []string{"works", "work_nodes", "work_edges", "evidence", "verification_attempts", "approvals"} {
		rows, err := st.DB.QueryContext(ctx, `SELECT * FROM `+table+` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		queries[table] = `SELECT ` + strings.Join(columns, ",") + ` FROM ` + table + ` ORDER BY rowid`
		before[table] = designedRows(t, st.DB, queries[table])
	}
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for table, query := range queries {
		if got := designedRows(t, st.DB, query); !reflect.DeepEqual(got, before[table]) {
			t.Fatalf("migration changed %s: before=%v after=%v", table, before[table], got)
		}
	}
	d, err := st.GetWorkDetail(ctx, "wrk_old")
	if err != nil || d.Work.Revision != 1 || len(d.Nodes) != 2 || len(d.Edges) != 1 || len(d.Evidence) != 1 || len(d.Attempts) != 1 {
		t.Fatalf("legacy detail = %+v, err=%v", d, err)
	}
	approvals, err := st.ListApprovals(ctx, "")
	if err != nil || len(approvals) != 1 || approvals[0].Kind != "tool" || approvals[0].WorkID != "" || approvals[0].NodeID != "" || approvals[0].NodeRevision != 0 {
		t.Fatalf("legacy approvals = %+v, err=%v", approvals, err)
	}
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}

func designedRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]any
	for rows.Next() {
		values, targets := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		out = append(out, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDesignedWorkflowApprovalRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		id, kind, workID, nodeID string
		revision                 int
	}{
		{"apr_tool", "", "", "", 0},
		{"apr_workflow", "workflow", "wrk_gate", "wnd_gate", 4},
	} {
		a := protocol.Approval{ID: tc.id, ThreadID: th.ID, TurnID: "trn", ItemID: "itm", Tool: "shell.run",
			Args: json.RawMessage(`{"command":"test"}`), Risk: "yellow", Reason: "reason", ActionSummary: "summary", Status: "pending",
			CreatedAt: at, ExpiresAt: at.Add(time.Minute), Kind: tc.kind, WorkID: tc.workID, NodeID: tc.nodeID, NodeRevision: tc.revision}
		if err := st.CreateApproval(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	approvals, err := st.ListApprovals(ctx, "pending")
	if err != nil || len(approvals) != 2 {
		t.Fatalf("approvals = %+v, err=%v", approvals, err)
	}
	byID := map[string]protocol.Approval{}
	for _, a := range approvals {
		byID[a.ID] = a
		if a.Tool != "shell.run" || string(a.Args) != `{"command":"test"}` || a.Reason != "reason" || a.ActionSummary != "summary" || !a.CreatedAt.Equal(at) || !a.ExpiresAt.Equal(at.Add(time.Minute)) {
			t.Fatalf("existing approval fields changed: %+v", a)
		}
	}
	if a := byID["apr_tool"]; a.Kind != "tool" || a.WorkID != "" || a.NodeID != "" || a.NodeRevision != 0 {
		t.Fatalf("tool approval = %+v", a)
	}
	if a := byID["apr_workflow"]; a.Kind != "workflow" || a.WorkID != "wrk_gate" || a.NodeID != "wnd_gate" || a.NodeRevision != 4 {
		t.Fatalf("workflow approval = %+v", a)
	}
}
