package store

import (
	"context"
	"fmt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func retrievalFixture(t *testing.T) (*Store, protocol.Work, retrieval.Scope, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "current"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, Goal: "compiler"})
	if err != nil {
		t.Fatal(err)
	}
	return s, w, retrieval.Scope{ThreadID: th.ID, WorkID: w.ID, TurnID: "live"}, path
}
func TestRetrievalScopeBeforeLimit(t *testing.T) {
	s, w, scope, _ := retrievalFixture(t)
	other, err := s.CreateThread(t.Context(), protocol.Thread{Title: "current"})
	if err != nil {
		t.Fatal(err)
	}
	ow, _ := s.CreateWork(t.Context(), protocol.Work{ThreadID: other.ID, Goal: "foreign"})
	for i := 0; i < 70; i++ {
		_, err = s.AddWorkNode(t.Context(), protocol.WorkNode{ID: fmt.Sprint("foreign", i), WorkID: ow.ID, Kind: protocol.NodeRequirement, Title: "compiler compiler", Status: "active"})
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeRequirement, Title: "compiler budget", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchRetrieval(t.Context(), scope, retrieval.BuildQuery("compiler", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "node:"+n.ID || got[0].ThreadID != scope.ThreadID {
		t.Fatalf("scope=%+v", got)
	}
	bad := scope
	bad.ThreadID = other.ID
	if hits, err := s.SearchRetrieval(t.Context(), bad, retrieval.BuildQuery("compiler", nil)); err == nil && len(hits) > 0 {
		t.Fatal("foreign work accepted")
	}
}
func TestRetrievalIndexCanonicalLifecycle(t *testing.T) {
	s, w, scope, _ := retrievalFixture(t)
	n, err := s.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeRequirement, Title: "compiler budget", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.AddEvidence(t.Context(), protocol.Evidence{WorkID: w.ID, Kind: protocol.EvidenceDiscovery, Summary: "compiler observed"})
	if err != nil {
		t.Fatal(err)
	}
	search := func() []retrieval.Candidate {
		t.Helper()
		hits, err := s.SearchRetrieval(t.Context(), scope, retrieval.BuildQuery("compiler", nil))
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	if len(search()) != 2 {
		t.Fatal("sources not indexed")
	}
	if err := s.UpdateWorkNode(t.Context(), n.ID, protocol.StatusSuperseded, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEvidenceStale(t.Context(), []string{e.ID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if hits := search(); len(hits) != 0 {
		t.Fatalf("stale sources=%+v", hits)
	}
	if _, err := s.DB.Exec(`DELETE FROM threads WHERE id=?`, scope.ThreadID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM retrieval_documents`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan docs=%d: %v", count, err)
	}
}
func TestRetrievalHistoricalMessages(t *testing.T) {
	s, _, scope, _ := retrievalFixture(t)
	for _, it := range []protocol.Item{
		{ID: "older", ThreadID: scope.ThreadID, TurnID: "old", Seq: 1, Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "compiler chosen"},
		{ID: "live", ThreadID: scope.ThreadID, TurnID: "live", Seq: 2, Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "compiler live"},
		{ID: "tool", ThreadID: scope.ThreadID, TurnID: "old", Seq: 3, Kind: protocol.ItemToolCall, Status: protocol.ItemCompleted, Text: "compiler output"},
	} {
		if err := s.SaveItem(t.Context(), it); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SearchRetrieval(t.Context(), scope, retrieval.BuildQuery("compiler", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "item:older" || !got[0].Historical {
		t.Fatalf("history=%+v", got)
	}
}
func TestRetrievalMigrationRedactsAndReopens(t *testing.T) {
	s, w, scope, path := retrievalFixture(t)
	n, err := s.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeRequirement, Title: "compiler", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "privatevalue123456"
	if _, err = s.DB.Exec(`UPDATE work_nodes SET title=? WHERE id=?`, "compiler API_TOKEN="+secret, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM schema_migrations WHERE version=17`); err != nil {
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
	hits, err := s.SearchRetrieval(context.Background(), scope, retrieval.BuildQuery("compiler", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || strings.Contains(hits[0].Body, secret) || !strings.Contains(hits[0].Body, "[REDACTED]") {
		t.Fatalf("unsafe migration=%+v", hits)
	}
	var count int
	if err = s.DB.QueryRow(`SELECT count(*) FROM retrieval_documents WHERE body LIKE ?`, "%"+secret+"%").Scan(&count); err != nil || count != 0 {
		t.Fatal("secret persisted", err)
	}
}

func TestRetrievalObservedTransactionAndCanonicalText(t *testing.T) {
	s, w, scope, _ := retrievalFixture(t)
	e := protocol.Evidence{ID: "observed", WorkID: w.ID, Kind: protocol.EvidenceDiscovery, Summary: "compiler"}
	x := protocol.ObservedExcerpt{Path: "main.go", StartLine: 1, EndLine: 1, Text: "compiler API_TOKEN=privatevalue123456", ContentHash: strings.Repeat("a", 64), WorkspaceRootHash: strings.Repeat("b", 64)}
	bad := x
	bad.Path = "../foreign"
	if _, err := s.RecordDiscoveryObservation(t.Context(), e, []protocol.ObservedExcerpt{x, bad}); err == nil {
		t.Fatal("invalid batch succeeded")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM evidence WHERE id='observed'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial observation committed", err)
	}
	if _, err := s.RecordDiscoveryObservation(t.Context(), e, []protocol.ObservedExcerpt{x}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchRetrieval(t.Context(), scope, retrieval.BuildQuery("compiler", nil))
	if err != nil || len(hits) != 2 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	for _, h := range hits {
		if strings.Contains(h.Body, "privatevalue123456") {
			t.Fatal("secret leaked")
		}
	}
	n, err := s.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeRequirement, Title: "compiler old", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE work_nodes SET title='compiler new' WHERE id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	hits, err = s.SearchRetrieval(t.Context(), scope, retrieval.BuildQuery("compiler", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.ID == "node:"+n.ID {
			t.Fatal("stale index text accepted")
		}
	}
}

func TestRetrievalProjectIdentityAndIndexRollback(t *testing.T) {
	s, _, _, _ := retrievalFixture(t)
	var scopes []retrieval.Scope
	for i := 0; i < 2; i++ {
		p, err := s.CreateProject(t.Context(), protocol.Project{Name: "same-name", Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		th, err := s.CreateThread(t.Context(), protocol.Thread{ProjectID: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		w, err := s.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, ProjectID: p.ID, Goal: "compiler"})
		if err != nil {
			t.Fatal(err)
		}
		scopes = append(scopes, retrieval.Scope{ThreadID: th.ID, ProjectID: p.ID, WorkID: w.ID, TurnID: "live"})
		if _, err := s.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeRequirement, Title: "compiler", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SearchRetrieval(t.Context(), scopes[0], retrieval.BuildQuery("compiler", nil))
	if err != nil || len(got) != 1 || got[0].ProjectID != scopes[0].ProjectID {
		t.Fatal("project isolation", got, err)
	}
	wrong := scopes[0]
	wrong.ProjectID = scopes[1].ProjectID
	if got, err := s.SearchRetrieval(t.Context(), wrong, retrieval.BuildQuery("compiler", nil)); err == nil || len(got) != 0 {
		t.Fatal("wrong project accepted")
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER retrieval_fail BEFORE INSERT ON retrieval_documents BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = s.AddWorkNode(t.Context(), protocol.WorkNode{ID: "rollback-node", WorkID: scopes[0].WorkID, Kind: protocol.NodeRequirement, Title: "compiler", Status: "active"})
	if err == nil {
		t.Fatal("index failure ignored")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM work_nodes WHERE id='rollback-node'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial canonical write", err)
	}
}

func TestRetrievalUnapprovedDecisionNotAuthority(t *testing.T) {
	s, w, scope, _ := retrievalFixture(t)
	for _, status := range []string{protocol.StatusProposed, protocol.StatusApproved} {
		if _, err := s.AddWorkNode(t.Context(), protocol.WorkNode{ID: status, WorkID: w.ID, Kind: protocol.NodeDecision, Title: "compiler choice", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.SearchRetrieval(t.Context(), scope, retrieval.BuildQuery("compiler", nil))
	if err != nil || len(hits) != 1 || hits[0].ID != "node:approved" {
		t.Fatalf("decision authority=%+v err=%v", hits, err)
	}
}
