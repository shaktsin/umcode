package store

import (
	"context"
	"encoding/json"
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
