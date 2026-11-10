package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func rev(task, agent, result string) AgentRevision {
	return AgentRevision{TaskID: task, AgentID: agent, WorkspaceID: "ws", BaseRevision: "base", ResultRevision: result, ResultTree: "tree-" + result, ChangedPaths: []string{"a.txt"}}
}

func TestSubmitRevisionIdempotent(t *testing.T) {
	s, task := runtimeFixture(t)
	a, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag1", "r1"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.State != "submitted" || a.Version != 1 || a.Seq != 1 {
		t.Fatalf("got %+v", a)
	}
	b, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag1", "r1"))
	if err != nil || b.ID != a.ID || b.Seq != 1 {
		t.Fatalf("repeat: %+v %v", b, err)
	}
	all, err := s.AgentRevisions(t.Context(), task.ID)
	if err != nil || len(all) != 1 {
		t.Fatalf("revisions=%d err=%v", len(all), err)
	}
	got, err := s.AgentRevision(t.Context(), task.ID, "ag1")
	if err != nil || got.ID != a.ID || len(got.ChangedPaths) != 1 {
		t.Fatalf("by agent: %+v %v", got, err)
	}
	if _, err = s.AgentRevision(t.Context(), task.ID, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSecondRevisionSameAgentRefusedUntilDropped(t *testing.T) {
	s, task := runtimeFixture(t)
	a, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag1", "r1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag1", "r2")); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("want ErrAgentTransition, got %v", err)
	}
	a.State = "dropped"
	if err = s.UpdateAgentRevision(t.Context(), a, a.Version); err != nil {
		t.Fatal(err)
	}
	b, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag1", "r2"))
	if err != nil || b.Seq != 2 || b.ID == a.ID {
		t.Fatalf("after drop: %+v %v", b, err)
	}
	got, err := s.AgentRevision(t.Context(), task.ID, "ag1")
	if err != nil || got.ID != b.ID {
		t.Fatalf("latest should be second: %+v %v", got, err)
	}
	// Resubmitting the dropped revision is idempotent, not a new row.
	c, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag1", "r1"))
	if err != nil || c.ID != a.ID || c.State != "dropped" {
		t.Fatalf("resubmit dropped: %+v %v", c, err)
	}
}

func TestRevisionTransitionsTable(t *testing.T) {
	states := []string{"submitted", "applying", "applied", "conflict", "dropped"}
	legal := map[string]bool{
		"submitted>applying": true, "submitted>conflict": true, "submitted>dropped": true,
		"applying>applied": true, "applying>submitted": true, "applying>conflict": true,
		"conflict>applying": true, "conflict>dropped": true,
	}
	// Paths that reach each starting state through legal transitions only.
	path := map[string][]string{
		"submitted": nil,
		"applying":  {"applying"},
		"applied":   {"applying", "applied"},
		"conflict":  {"conflict"},
		"dropped":   {"dropped"},
	}
	s, task := runtimeFixture(t)
	for _, from := range states {
		for _, to := range states {
			ag := "ag-" + from + "-" + to
			r, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, ag, "r"))
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range path[from] {
				r.State = st
				if err = s.UpdateAgentRevision(t.Context(), r, r.Version); err != nil {
					t.Fatalf("setup %s: %v", st, err)
				}
				r, _ = s.AgentRevision(t.Context(), task.ID, ag)
			}
			r.State = to
			err = s.UpdateAgentRevision(t.Context(), r, r.Version)
			if legal[from+">"+to] {
				if err != nil {
					t.Errorf("%s>%s should be legal: %v", from, to, err)
				}
			} else if !errors.Is(err, ErrAgentTransition) {
				t.Errorf("%s>%s want ErrAgentTransition, got %v", from, to, err)
			}
		}
	}
	r, _ := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag-bogus", "r"))
	r.State = "bogus"
	if err := s.UpdateAgentRevision(t.Context(), r, r.Version); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("unknown state: %v", err)
	}
}

func TestRevisionVersionConflict(t *testing.T) {
	s, task := runtimeFixture(t)
	r, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag", "r"))
	if err != nil {
		t.Fatal(err)
	}
	r.State = "applying"
	r.TreeBefore = "t0"
	if err = s.UpdateAgentRevision(t.Context(), r, 99); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("want ErrAgentConflict, got %v", err)
	}
	if err = s.UpdateAgentRevision(t.Context(), r, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.AgentRevision(t.Context(), task.ID, "ag")
	if got.Version != 2 || got.State != "applying" || got.TreeBefore != "t0" {
		t.Fatalf("got %+v", got)
	}
	if err = s.UpdateAgentRevision(t.Context(), r, 1); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("stale: %v", err)
	}
	r.TaskID = "other"
	if err = s.UpdateAgentRevision(t.Context(), r, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign task: %v", err)
	}
}

func TestRevisionFreeTextBounded(t *testing.T) {
	s, task := runtimeFixture(t)
	r, _ := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag", "r"))
	r.State = "conflict"
	r.Reason = strings.Repeat("x", 10000)
	for i := 0; i < 100; i++ {
		r.Conflict = append(r.Conflict, "p")
	}
	if err := s.UpdateAgentRevision(t.Context(), r, r.Version); err != nil {
		t.Fatal(err)
	}
	got, _ := s.AgentRevision(t.Context(), task.ID, "ag")
	if len(got.Reason) > 4096 || len(got.Conflict) != 64 {
		t.Fatalf("reason=%d conflict=%d", len(got.Reason), len(got.Conflict))
	}
}

func TestHeadVersionConflict(t *testing.T) {
	s, task := runtimeFixture(t)
	if _, err := s.AgentIntegrationHead(t.Context(), task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before set: %v", err)
	}
	h := AgentIntegrationHead{TaskID: task.ID, HeadTree: "t1", AppliedCount: 1, Workflow: json.RawMessage(`{"head_tree":"t1"}`)}
	if err := s.SetAgentIntegrationHead(t.Context(), h, 5); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("create with wrong version: %v", err)
	}
	if err := s.SetAgentIntegrationHead(t.Context(), h, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentIntegrationHead(t.Context(), h, 0); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("second create: %v", err)
	}
	got, err := s.AgentIntegrationHead(t.Context(), task.ID)
	if err != nil || got.Version != 1 || got.HeadTree != "t1" || got.AppliedCount != 1 || string(got.Workflow) != `{"head_tree":"t1"}` {
		t.Fatalf("got %+v %v", got, err)
	}
	h.HeadTree = "t2"
	h.AppliedCount = 2
	if err = s.SetAgentIntegrationHead(t.Context(), h, 7); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("stale: %v", err)
	}
	if err = s.SetAgentIntegrationHead(t.Context(), h, 1); err != nil {
		t.Fatal(err)
	}
	got, _ = s.AgentIntegrationHead(t.Context(), task.ID)
	if got.Version != 2 || got.HeadTree != "t2" || got.AppliedCount != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestRevisionsDeletedWithTask(t *testing.T) {
	s, task := runtimeFixture(t)
	if _, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, "ag", "r")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentIntegrationHead(t.Context(), AgentIntegrationHead{TaskID: task.ID, HeadTree: "t"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `DELETE FROM agent_tasks WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, q := range []string{`SELECT COUNT(*) FROM agent_revisions`, `SELECT COUNT(*) FROM agent_integration_head`} {
		if err := s.DB.QueryRowContext(t.Context(), q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s = %d err=%v", q, n, err)
		}
	}
}

func TestRevisionOrderIsSubmissionOrder(t *testing.T) {
	s, task := runtimeFixture(t)
	for _, id := range []string{"zeta", "alpha", "mid"} {
		if _, err := s.SubmitAgentRevision(t.Context(), rev(task.ID, id, "r-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.AgentRevisions(t.Context(), task.ID)
	if err != nil || len(all) != 3 {
		t.Fatalf("%d %v", len(all), err)
	}
	for i, want := range []string{"zeta", "alpha", "mid"} {
		if all[i].AgentID != want || all[i].Seq != i+1 {
			t.Fatalf("pos %d: %+v", i, all[i])
		}
	}
}

func TestRuntimeWorkflowDecode(t *testing.T) {
	w, err := DecodeRuntimeWorkflow(nil)
	if err != nil || w.HeadTree != "" || w.Checks != nil {
		t.Fatalf("nil: %+v %v", w, err)
	}
	if w, err = DecodeRuntimeWorkflow(json.RawMessage("")); err != nil || w.HeadTree != "" {
		t.Fatalf("empty: %+v %v", w, err)
	}
	w, err = DecodeRuntimeWorkflow(json.RawMessage(`{"head_tree":"abc","checks":[{"label":"Tests","command":"go test","status":"passed"}]}`))
	if err != nil || w.HeadTree != "abc" || len(w.Checks) != 1 || w.Checks[0].Label != "Tests" || w.Checks[0].Status != "passed" {
		t.Fatalf("full: %+v %v", w, err)
	}
	if _, err = DecodeRuntimeWorkflow(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("want error on malformed json")
	}
}

func TestRuntimeIntegrationsToReconcile(t *testing.T) {
	s, task := runtimeFixture(t)
	ctx := t.Context()
	want := func(ids ...string) {
		t.Helper()
		got, err := s.RuntimeIntegrationsToReconcile(ctx)
		if err != nil || strings.Join(got, ",") != strings.Join(ids, ",") {
			t.Fatalf("got %v %v, want %v", got, err, ids)
		}
	}
	want()
	r, err := s.SubmitAgentRevision(ctx, rev(task.ID, "ag", "r"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAgentIntegrationHead(ctx, AgentIntegrationHead{TaskID: task.ID, HeadTree: "t0"}, 0); err != nil {
		t.Fatal(err)
	}
	want()
	r.State, r.TreeBefore = RevisionApplying, "t0"
	if err = s.UpdateAgentRevision(ctx, r, r.Version); err != nil {
		t.Fatal(err)
	}
	want(task.ID)
	// Applied, but the head missed its move.
	if r, err = s.AgentRevision(ctx, task.ID, "ag"); err != nil {
		t.Fatal(err)
	}
	r.State, r.TreeAfter = RevisionApplied, "t1"
	if err = s.UpdateAgentRevision(ctx, r, r.Version); err != nil {
		t.Fatal(err)
	}
	want(task.ID)
	if err = s.SetAgentIntegrationHead(ctx, AgentIntegrationHead{TaskID: task.ID, HeadTree: "t1", AppliedCount: 1}, 1); err != nil {
		t.Fatal(err)
	}
	want()
}

// A database migrated by a Plan 1/2 build already recorded 0034 without the
// revision tables; they must come from a later migration.
func TestRevisionTablesAddedToExistingRuntimeDatabase(t *testing.T) {
	body, err := migrationsFS.ReadFile("migrations/0034_agent_runtime.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "agent_revisions") || strings.Contains(string(body), "agent_integration_head") {
		t.Fatal("0034 was edited in place; existing databases never re-run it")
	}
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DROP TABLE agent_revisions; DROP TABLE agent_integration_head; DROP TABLE agent_suggestions; DELETE FROM schema_migrations WHERE version > 34`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{`SELECT COUNT(*) FROM agent_revisions`, `SELECT COUNT(*) FROM agent_integration_head`} {
		var n int
		if err = s.DB.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s after upgrade: %v", q, err)
		}
	}
}
