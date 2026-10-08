package work

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// Catches two callers successfully committing the same expected revision.
func TestServiceUpdateConcurrentWinner(t *testing.T) {
	f := newFixture(t)
	f.svc.DesignedWorkflow = true
	d := f.begin(t, "ship it")
	f.svc.Now = func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) }
	req := protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "requirement", Title: "correct"}}}
	start := make(chan struct{})
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, _, err := f.svc.Update(context.Background(), f.th.ID, req); out <- err }()
	}
	close(start)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-out
		var validation *ValidationError
		if err == nil {
			success++
		} else if errors.Is(err, store.ErrWorkUpdateConflict) || errors.As(err, &validation) && validation.Code == "stale" {
			conflicts++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	got := f.detail(t)
	if got.Work.Revision != 2 || len(kinds(got, "requirement")) != 1 {
		t.Fatalf("detail=%+v", got)
	}
}

func TestServiceUpdateAcceptedRiskIntentLeavesUnknownOpen(t *testing.T) {
	f := newFixture(t)
	f.svc.DesignedWorkflow = true
	d := f.begin(t, "implement billing")
	n, err := f.st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: d.Work.ID, Kind: "unknown", Status: "open", Content: json.RawMessage(`{"blocking":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	r, gates, err := f.svc.Update(t.Context(), f.th.ID, protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: d.Work.Revision, Nodes: []protocol.WorkNodeChange{{ID: n.ID, ExpectedRevision: 1, FromStatus: "open", ToStatus: "accepted_risk"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != 2 || r.Transitioned != 0 || len(gates) != 1 || gates[0].NodeRevision != 1 {
		t.Fatalf("result=%+v gates=%+v", r, gates)
	}
	got := f.detail(t)
	for _, node := range got.Nodes {
		if node.ID == n.ID && (node.Status != "open" || node.Revision != 1) {
			t.Fatalf("accepted without review: %+v", node)
		}
	}
}

// Catches foreign, closed, superseded-open, or absent calling-thread works.
func TestServiceUpdateRequiresCurrentOpenWork(t *testing.T) {
	for _, mode := range []string{"foreign thread", "closed", "older open", "no open"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			d := f.begin(t, "objective")
			ctx := context.Background()
			threadID := f.th.ID
			switch mode {
			case "foreign thread", "no open":
				th, err := f.st.CreateThread(ctx, protocol.Thread{Title: "other"})
				if err != nil {
					t.Fatal(err)
				}
				threadID = th.ID
				if mode == "foreign thread" {
					if _, err := f.st.CreateWork(ctx, protocol.Work{ThreadID: th.ID}); err != nil {
						t.Fatal(err)
					}
				}
			case "closed":
				if err := f.st.CloseWork(ctx, d.Work.ID, "completed", time.Now()); err != nil {
					t.Fatal(err)
				}
			case "older open":
				if _, err := f.st.CreateWork(ctx, protocol.Work{ThreadID: f.th.ID, CreatedAt: d.Work.CreatedAt.Add(time.Second)}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := f.st.GetWorkDetail(ctx, d.Work.ID)
			_, _, err := f.svc.Update(ctx, threadID, protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "requirement", Title: "correct"}}})
			if err == nil {
				t.Fatal("accepted non-active work")
			}
			after, _ := f.st.GetWorkDetail(ctx, d.Work.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected request changed work")
			}
		})
	}
}

// Catches leaked rationale, nested secrets, redaction changing semantic numbers,
// or mutating the caller's slices/raw content while sanitizing its request.
func TestServiceUpdateRedactsRationale(t *testing.T) {
	f := newFixture(t)
	d := f.begin(t, "objective")
	var logs bytes.Buffer
	f.svc.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	req := protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Rationale: "private-rationale API_TOKEN=rationalesecret12345", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "requirement", Title: "API_TOKEN=titlesecret12345", Content: json.RawMessage(`{"required":true,"count":9007199254740993,"nested":[{"text":"API_TOKEN=contentsecret12345"}],"url":"https://user:password12345@host/"}`)}}}
	before, _ := json.Marshal(req)
	result, _, err := f.svc.Update(context.Background(), f.th.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Revision != 2 {
		t.Fatalf("result=%+v", result)
	}
	after, _ := json.Marshal(req)
	if !bytes.Equal(before, after) {
		t.Fatal("caller request mutated")
	}
	got := kinds(f.detail(t), "requirement")[0]
	if got.Title != "API_TOKEN=[REDACTED]" || !strings.Contains(string(got.Content), "[REDACTED]") || !strings.Contains(string(got.Content), "9007199254740993") {
		t.Fatalf("redaction=%s %s", got.Title, got.Content)
	}
	var audits string
	if err := f.st.DB.QueryRow(`SELECT COALESCE(group_concat(details_json), '') FROM audit_log`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(f.detail(t))
	all := string(stored) + logs.String() + audits
	for _, forbidden := range []string{"private-rationale", "rationalesecret12345", "titlesecret12345", "contentsecret12345", "password12345"} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
}

// Catches sanitization laundering secret candidates or duplicate semantic keys.
func TestServiceUpdateRejectsSecretCandidatesAndDuplicateContent(t *testing.T) {
	for _, tc := range []struct{ name, kind, title, content string }{
		{"candidate title", "memory_candidate", "API_TOKEN=titlesecret12345", `{"category":"command","semantic_key":"test","text":"go test","scope":"."}`},
		{"candidate content", "memory_candidate", "test", `{"category":"command","semantic_key":"test","text":"go test","scope":".","extra":{"text":"API_TOKEN=secretvalue12345"}}`},
		{"duplicate keys", "requirement", "test", `{"required":false,"required":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			d := f.begin(t, "objective")
			ev, err := f.st.AddEvidence(context.Background(), protocol.Evidence{WorkID: d.Work.ID, Kind: "file_change"})
			if err != nil {
				t.Fatal(err)
			}
			fact, err := f.st.AddWorkNode(context.Background(), protocol.WorkNode{WorkID: d.Work.ID, Kind: "fact", Title: "source", Status: "active"})
			if err != nil {
				t.Fatal(err)
			}
			req := protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: tc.kind, Title: tc.title, Content: json.RawMessage(tc.content), EvidenceIDs: []string{ev.ID}}}, Edges: []protocol.WorkEdgeChange{{From: "r", Relation: "candidate_for", To: fact.ID}}}
			before, _ := json.Marshal(req)
			_, _, err = f.svc.Update(context.Background(), f.th.ID, req)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Code != "invalid" {
				t.Fatalf("err=%v", err)
			}
			after, _ := json.Marshal(req)
			if !bytes.Equal(before, after) || f.detail(t).Work.Revision != 1 {
				t.Fatal("rejection mutated request/work")
			}
		})
	}
}

// Catches non-atomic depth escalation by concurrent legacy observations.
func TestServiceUpdateDepthConcurrentEscalation(t *testing.T) {
	f := newFixture(t)
	d := f.begin(t, "objective")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.st.SetWorkDepth(context.Background(), d.Work.ID, "designed"); err != nil {
				t.Error(err)
			}
			if err := f.st.SetWorkDepth(context.Background(), d.Work.ID, "guided"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got := f.detail(t).Work
	if got.WorkflowDepth != "designed" || got.Revision != 2 {
		t.Fatalf("work=%+v", got)
	}
}

// Catches redaction shrinking oversized original requests below semantic bounds.
func TestServiceUpdateRetainsOriginalBounds(t *testing.T) {
	for _, field := range []string{"title", "content"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			d := f.begin(t, "objective")
			node := protocol.WorkNodeChange{Ref: "r", Kind: "requirement", Title: "ok"}
			if field == "title" {
				node.Title = "API_TOKEN=" + strings.Repeat("a", MaxNodeTitleBytes)
			} else {
				node.Content = json.RawMessage(`{"text":"API_TOKEN=` + strings.Repeat("a", MaxNodeContentBytes) + `"}`)
			}
			_, _, err := f.svc.Update(context.Background(), f.th.ID, protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{node}})
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Code != "limit" {
				t.Fatalf("err=%v", err)
			}
			if f.detail(t).Work.Revision != 1 {
				t.Fatal("oversized batch persisted")
			}
		})
	}
}

// Catches dropped derived transitions, incorrect reference counts, or gate
// metadata leaking into the compact result instead of its separate return.
func TestServiceUpdateDerivedReadinessAndGates(t *testing.T) {
	for _, gated := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "gate"}[gated], func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			d, req := graphFixture()
			d.Work.ThreadID = f.th.ID
			if _, err := f.st.CreateWork(ctx, d.Work); err != nil {
				t.Fatal(err)
			}
			for _, n := range d.Nodes {
				if _, err := f.st.AddWorkNode(ctx, n); err != nil {
					t.Fatal(err)
				}
			}
			for _, ev := range d.Evidence {
				if _, err := f.st.AddEvidence(ctx, ev); err != nil {
					t.Fatal(err)
				}
			}
			for _, node := range d.Nodes {
				for _, evidenceID := range node.EvidenceIDs {
					if _, err := f.st.DB.ExecContext(ctx, `INSERT INTO work_node_evidence (work_id, node_id, evidence_id) VALUES (?,?,?)`, d.Work.ID, node.ID, evidenceID); err != nil {
						t.Fatal(err)
					}
				}
			}
			waiting, err := f.st.AddWorkNode(ctx, protocol.WorkNode{ID: "waiting", WorkID: d.Work.ID, Kind: "task", Title: "waiting", Status: "pending", Revision: 3, Content: json.RawMessage(`{"required":true}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.st.AddWorkEdge(ctx, protocol.WorkEdge{WorkID: d.Work.ID, FromNodeID: "criterion", Relation: "verifies", ToNodeID: waiting.ID}); err != nil {
				t.Fatal(err)
			}
			if gated {
				req.Nodes[3].Content = json.RawMessage(`{"required":true,"gate_kind":"security"}`)
				req.Nodes[3].ToStatus = "proposed"
			}
			result, gates, err := f.svc.Update(ctx, f.th.ID, req)
			if err != nil {
				t.Fatal(err)
			}
			wantTransitions := 1
			wantStatus := "ready"
			if gated {
				wantTransitions = 0
				wantStatus = "pending"
			}
			if result != (protocol.WorkUpdateResult{Revision: 5, Created: 7, Transitioned: wantTransitions, Linked: 12}) {
				t.Fatalf("result=%+v", result)
			}
			got := f.detail(t)
			for _, node := range got.Nodes {
				if node.ID == "waiting" && (node.Status != wantStatus || node.Revision != 3+wantTransitions) {
					t.Fatalf("waiting=%+v", node)
				}
			}
			if !gated && len(gates) != 0 || gated && (len(gates) != 1 || gates[0].Kind != "security" || gates[0].NodeID == "" || gates[0].NodeRevision != 1) {
				t.Fatalf("gates=%+v", gates)
			}
			blob, _ := json.Marshal(result)
			if strings.Contains(string(blob), "security") || strings.Contains(string(blob), "summary") {
				t.Fatalf("gate in result=%s", blob)
			}
		})
	}
}

// Catches rejecting requests while still leaking secret-shaped IDs or raw
// storage errors through diagnostic metadata.
func TestServiceUpdateRejectedDiagnostics(t *testing.T) {
	f := newFixture(t)
	d := f.begin(t, "objective")
	var logs bytes.Buffer
	f.svc.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	secret := "sk-123456789012345678901234567890"
	_, _, err := f.svc.Update(context.Background(), f.th.ID, protocol.WorkUpdateRequest{WorkID: secret, ExpectedRevision: 1, Rationale: "private-rationale"})
	if err == nil {
		t.Fatal("accepted foreign ID")
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "private-rationale") {
		t.Fatal("rejection leaked request text")
	}
	logs.Reset()
	if _, err := f.st.DB.Exec(`CREATE TRIGGER reject_node BEFORE INSERT ON work_nodes BEGIN SELECT RAISE(ABORT, 'private-storage-error'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.svc.Update(context.Background(), f.th.ID, protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "requirement", Title: "correct"}}, Rationale: "private-rationale"})
	if err == nil {
		t.Fatal("storage failure missing")
	}
	if strings.Contains(logs.String(), "private-storage-error") || strings.Contains(logs.String(), "private-rationale") {
		t.Fatal("storage rejection leaked prose")
	}
	if f.detail(t).Work.Revision != 1 {
		t.Fatal("storage rejection persisted work revision")
	}
}

// Catches loss of JSON field context, unsafe encoding of masks, and changed
// numeric semantics while sanitizing nested credentials outside candidates.
func TestServiceUpdateRedactsCredentialFields(t *testing.T) {
	f := newFixture(t)
	d := f.begin(t, "objective")
	req := protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "requirement", Title: "ordinary", Content: json.RawMessage(`{"required":true,"count":9007199254740993,"nested":[{"API_TOKEN":"ordinarysecret12345","password":"quoted\"secret\\value","Authorization":"plainauth12345"}],"outer":{"API_TOKEN":["arraysecret12345",{"piece":"objectsecret12345"}]}}`)}}}
	before, _ := json.Marshal(req)
	if _, _, err := f.svc.Update(context.Background(), f.th.ID, req); err != nil {
		t.Fatal(err)
	}
	got := kinds(f.detail(t), "requirement")[0]
	var content map[string]any
	if err := json.Unmarshal(got.Content, &content); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !strings.Contains(string(got.Content), "9007199254740993") {
		t.Fatalf("rounded number: %s", got.Content)
	}
	for _, secret := range []string{"ordinarysecret12345", "quoted", "plainauth12345", "arraysecret12345", "objectsecret12345"} {
		if strings.Contains(string(got.Content), secret) {
			t.Fatalf("credential persisted: %s", got.Content)
		}
	}
	if !strings.Contains(string(got.Content), "[REDACTED]") {
		t.Fatal("credential fields were not masked")
	}
	after, _ := json.Marshal(req)
	if !bytes.Equal(before, after) {
		t.Fatal("redaction mutated caller request")
	}
}

// Catches credential keys being sanitized into apparently safe candidates.
func TestServiceUpdateRejectsCandidateCredentialFields(t *testing.T) {
	for _, credential := range []string{`"API_TOKEN":"ordinarysecret12345"`, `"nested":[{"password":"ordinarysecret12345"}]`, `"nested":{"API_TOKEN":["ordinarysecret12345"]}`, `"nested":{"text":"sk-123456789012345678901234567890","text":"ordinary"}`} {
		f := newFixture(t)
		d := f.begin(t, "objective")
		ctx := context.Background()
		ev, err := f.st.AddEvidence(ctx, protocol.Evidence{WorkID: d.Work.ID, Kind: "file_change"})
		if err != nil {
			t.Fatal(err)
		}
		fact, err := f.st.AddWorkNode(ctx, protocol.WorkNode{WorkID: d.Work.ID, Kind: "fact", Title: "source", Status: "active"})
		if err != nil {
			t.Fatal(err)
		}
		req := protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "memory_candidate", Title: "ordinary", Content: json.RawMessage(`{"category":"command","semantic_key":"test-command","text":"go test","scope":".",` + credential + `}`), EvidenceIDs: []string{ev.ID}}}, Edges: []protocol.WorkEdgeChange{{From: "r", Relation: "candidate_for", To: fact.ID}}}
		before := f.detail(t)
		beforeReq, _ := json.Marshal(req)
		_, _, err = f.svc.Update(ctx, f.th.ID, req)
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Code != "invalid" {
			t.Fatalf("credential candidate accepted: %v", err)
		}
		if !reflect.DeepEqual(before, f.detail(t)) {
			t.Fatal("candidate rejection changed graph/revision")
		}
		afterReq, _ := json.Marshal(req)
		if !bytes.Equal(beforeReq, afterReq) {
			t.Fatal("candidate rejection mutated caller request")
		}
	}
}

// Catches preparation deduplicating a relationship and committing other changes.
func TestServiceUpdateRejectsDuplicateRelationships(t *testing.T) {
	for _, mode := range []string{"request edge", "persisted edge", "request evidence", "persisted evidence"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			d := f.begin(t, "objective")
			ctx := context.Background()
			ev, err := f.st.AddEvidence(ctx, protocol.Evidence{WorkID: d.Work.ID, Kind: "file_change"})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := f.st.AddWorkNode(ctx, protocol.WorkNode{WorkID: d.Work.ID, Kind: "task", Title: "waiting", Status: "pending", Content: json.RawMessage(`{"required":true}`)})
			if err != nil {
				t.Fatal(err)
			}
			req := protocol.WorkUpdateRequest{WorkID: d.Work.ID, ExpectedRevision: 1, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "requirement", Title: "ordinary"}}}
			edge := protocol.WorkEdgeChange{From: waiting.ID, Relation: "serves", To: d.Nodes[0].ID}
			switch mode {
			case "request edge":
				req.Edges = []protocol.WorkEdgeChange{edge, edge}
			case "persisted edge":
				if err := f.st.AddWorkEdge(ctx, protocol.WorkEdge{WorkID: d.Work.ID, FromNodeID: edge.From, Relation: edge.Relation, ToNodeID: edge.To}); err != nil {
					t.Fatal(err)
				}
				req.Edges = []protocol.WorkEdgeChange{edge}
			case "request evidence":
				req.Nodes[0].EvidenceIDs = []string{ev.ID, ev.ID}
			case "persisted evidence":
				if _, err := f.st.DB.ExecContext(ctx, `INSERT INTO work_node_evidence (work_id, node_id, evidence_id) VALUES (?,?,?)`, d.Work.ID, waiting.ID, ev.ID); err != nil {
					t.Fatal(err)
				}
				req.Nodes = append(req.Nodes, protocol.WorkNodeChange{ID: waiting.ID, ExpectedRevision: 1, FromStatus: "pending", ToStatus: "ready", EvidenceIDs: []string{ev.ID}})
			}
			before := f.detail(t)
			_, _, err = f.svc.Update(ctx, f.th.ID, req)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Code != "duplicate" {
				t.Fatalf("err=%v", err)
			}
			if !reflect.DeepEqual(before, f.detail(t)) {
				t.Fatal("duplicate rejection changed graph/revision")
			}
		})
	}
}

type fixture struct {
	svc *Service
	st  *store.Store
	th  protocol.Thread
}

// newFixture builds a service over an in-memory store with a clock that
// advances one second per reading, so ordering is deterministic.
func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	th, err := st.CreateThread(ctx, protocol.Thread{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc := &Service{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}}
	return fixture{svc: svc, st: st, th: th}
}

func (f fixture) begin(t *testing.T, text string) protocol.WorkDetail {
	t.Helper()
	if err := f.svc.Begin(context.Background(), f.th, text); err != nil {
		t.Fatal(err)
	}
	return f.detail(t)
}

func (f fixture) detail(t *testing.T) protocol.WorkDetail {
	t.Helper()
	works, err := f.st.ListWorks(context.Background(), f.th.ID)
	if err != nil || len(works) == 0 {
		t.Fatalf("works = %v err = %v", works, err)
	}
	d, err := f.st.GetWorkDetail(context.Background(), works[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f fixture) observe(t *testing.T, o Observation) {
	t.Helper()
	if err := f.svc.Observe(context.Background(), f.th.ID, o); err != nil {
		t.Fatal(err)
	}
}

const planOut = `{"checks":[{"label":"unit","command":"go test ./..."},{"label":"vet","command":"go vet ./..."}]}`

func runOut(unit, vet string) string {
	return `{"status":"failed","results":[{"label":"unit","command":"go test ./...","status":"` + unit + `","exit_code":1,"output":"FAIL"},` +
		`{"label":"vet","command":"go vet ./...","status":"` + vet + `"}]}`
}

func kinds(d protocol.WorkDetail, kind string) []protocol.WorkNode {
	var out []protocol.WorkNode
	for _, n := range d.Nodes {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

func TestBeginOpensThenContinues(t *testing.T) {
	f := newFixture(t)
	d := f.begin(t, "fix the bug")
	if d.Work.Goal != "fix the bug" || d.Work.WorkflowDepth != protocol.DepthDirect || d.Work.Status != protocol.WorkOpen {
		t.Fatalf("work = %+v", d.Work)
	}
	if g := kinds(d, protocol.NodeGoal); len(g) != 1 || g[0].Title != "fix the bug" {
		t.Fatalf("goal nodes = %+v", g)
	}
	f.begin(t, "second message")
	if works, _ := f.st.ListWorks(context.Background(), f.th.ID); len(works) != 1 {
		t.Fatalf("works = %d, want 1", len(works))
	}
	f.st.CloseWork(context.Background(), d.Work.ID, protocol.WorkCompleted, time.Now().UTC())
	d2 := f.begin(t, "new objective")
	if d2.Work.ID == d.Work.ID || d2.Work.Goal != "new objective" {
		t.Fatalf("new work = %+v", d2.Work)
	}
}

func TestBeginNoProjectThread(t *testing.T) {
	f := newFixture(t)
	if d := f.begin(t, "hello"); d.Work.ProjectID != "" {
		t.Fatalf("project id = %q", d.Work.ProjectID)
	}
}

// Catches feature-off classification changes and feature-on missed design escalation on continued work.
func TestDesignedWorkflowServiceClassification(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			f := newFixture(t)
			f.svc.DesignedWorkflow = enabled
			d := f.begin(t, "Design a replacement cache")
			want := "direct"
			if enabled {
				want = "designed"
			}
			if d.Work.WorkflowDepth != want {
				t.Fatalf("begin depth=%s want %s", d.Work.WorkflowDepth, want)
			}
			f.observe(t, Observation{Tool: "file.write", Args: json.RawMessage(`{"path":"internal/protocol/work.go"}`)})
			want = "guided"
			if enabled {
				want = "designed"
			}
			if got := f.detail(t).Work.WorkflowDepth; got != want {
				t.Fatalf("observe depth=%s want %s", got, want)
			}
		})
	}
	f := newFixture(t)
	f.svc.DesignedWorkflow = true
	f.begin(t, "What does this do?")
	if got := f.begin(t, "Design the architecture").Work.WorkflowDepth; got != "designed" {
		t.Fatalf("continued depth=%s", got)
	}
}

// Catches enabled completion closing Guided work without semantic obligations, or breaking Direct/feature-off completion.
func TestDesignedWorkflowServiceCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, depth string
		enabled     bool
		want        string
	}{
		{"off guided", "guided", false, "completed"}, {"on direct", "direct", true, "completed"}, {"on guided", "guided", true, "open"}, {"on designed", "designed", true, "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.svc.DesignedWorkflow = tc.enabled
			d := f.begin(t, "Question")
			if err := f.st.SetWorkDepth(context.Background(), d.Work.ID, tc.depth); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, ""); err != nil {
				t.Fatal(err)
			}
			if got := f.detail(t).Work.Status; got != tc.want {
				t.Fatalf("status=%s want %s", got, tc.want)
			}
		})
	}
	f := newFixture(t)
	f.svc.DesignedWorkflow = true
	d := f.begin(t, "Design a minimal change")
	graph := readinessFixture()
	graph.Nodes[0].Status = "completed"
	for _, n := range graph.Nodes {
		n.WorkID = d.Work.ID
		if _, err := f.st.AddWorkNode(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range graph.Edges {
		e.WorkID = d.Work.ID
		if err := f.st.AddWorkEdge(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range graph.Evidence {
		e.WorkID = d.Work.ID
		if _, err := f.st.AddEvidence(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range graph.Nodes {
		for _, id := range n.EvidenceIDs {
			if _, err := f.st.DB.ExecContext(context.Background(), `INSERT INTO work_node_evidence (work_id,node_id,evidence_id) VALUES (?,?,?)`, d.Work.ID, n.ID, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	a := graph.Attempts[0]
	a.WorkID = d.Work.ID
	if _, err := f.st.AddVerificationAttempt(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t).Work.Status; got != "completed" {
		t.Fatalf("satisfied graph=%s", got)
	}
}

// Catches End closing an enabled Designed work using persisted stale decision support.
func TestDesignedWorkflowServiceCompletionKeepsStaleSolutionOpen(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "on", false: "off"}[enabled], func(t *testing.T) {
			f := newFixture(t)
			f.svc.DesignedWorkflow = enabled
			d := f.begin(t, "Design a minimal change")
			if err := f.st.SetWorkDepth(context.Background(), d.Work.ID, "designed"); err != nil {
				t.Fatal(err)
			}
			graph := readinessFixture()
			graph.Nodes[0].Status = "completed"
			stale := time.Unix(3, 0)
			graph.Evidence[0].StaleAt = &stale
			for _, n := range graph.Nodes {
				n.WorkID = d.Work.ID
				if _, err := f.st.AddWorkNode(context.Background(), n); err != nil {
					t.Fatal(err)
				}
			}
			for _, e := range graph.Edges {
				e.WorkID = d.Work.ID
				if err := f.st.AddWorkEdge(context.Background(), e); err != nil {
					t.Fatal(err)
				}
			}
			for _, e := range graph.Evidence {
				e.WorkID = d.Work.ID
				if _, err := f.st.AddEvidence(context.Background(), e); err != nil {
					t.Fatal(err)
				}
			}
			for _, n := range graph.Nodes {
				for _, id := range n.EvidenceIDs {
					if _, err := f.st.DB.ExecContext(context.Background(), `INSERT INTO work_node_evidence (work_id,node_id,evidence_id) VALUES (?,?,?)`, d.Work.ID, n.ID, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			a := graph.Attempts[0]
			a.WorkID = d.Work.ID
			if _, err := f.st.AddVerificationAttempt(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, ""); err != nil {
				t.Fatal(err)
			}
			want := "completed"
			if enabled {
				want = "open"
			}
			if got := f.detail(t).Work.Status; got != want {
				t.Fatalf("status=%s want %s", got, want)
			}
		})
	}
}

func TestBeginCapsGoal(t *testing.T) {
	f := newFixture(t)
	d := f.begin(t, strings.Repeat("x", 10000))
	if len(d.Work.Goal) > 2000 || len(kinds(d, protocol.NodeGoal)[0].Title) > 2000 {
		t.Fatalf("goal length = %d", len(d.Work.Goal))
	}
}

func TestEscalatesToGuided(t *testing.T) {
	for _, c := range []struct {
		tool, risk string
		want       bool
	}{
		{"file.write", "yellow", true}, {"file.edit", "yellow", true}, {"verification.plan", "green", true},
		{"verification.run", "yellow", true}, {"browser.verify", "yellow", true},
		{"shell.run", "yellow", true}, {"shell.run", "red", true}, {"shell.run", "green", false},
		{"file.read", "green", false}, {"file.search", "green", false}, {"web.fetch", "green", false},
	} {
		if got := EscalatesToGuided(c.tool, c.risk); got != c.want {
			t.Errorf("EscalatesToGuided(%q, %q) = %v, want %v", c.tool, c.risk, got, c.want)
		}
	}
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "file.read", Risk: "green", Output: "x"})
	if d := f.detail(t); d.Work.WorkflowDepth != protocol.DepthDirect {
		t.Fatalf("depth after read = %s", d.Work.WorkflowDepth)
	}
	f.observe(t, Observation{Tool: "file.write", Risk: "yellow", Args: json.RawMessage(`{"path":"a.txt"}`)})
	f.observe(t, Observation{Tool: "file.read", Risk: "green"})
	if d := f.detail(t); d.Work.WorkflowDepth != protocol.DepthGuided {
		t.Fatalf("depth = %s, want guided and never downgraded", d.Work.WorkflowDepth)
	}
}

func TestFileWriteRecordsArtifact(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644)
	sum := sha256.Sum256([]byte("hello\n"))
	f.observe(t, Observation{Tool: "file.write", Risk: "yellow", Root: root, Args: json.RawMessage(`{"path":"a.txt"}`)})
	d := f.detail(t)
	arts := kinds(d, protocol.NodeArtifact)
	if len(arts) != 1 || arts[0].Title != "a.txt" || arts[0].Revision != 1 {
		t.Fatalf("artifacts = %+v", arts)
	}
	goal := kinds(d, protocol.NodeGoal)[0]
	if len(d.Edges) != 1 || d.Edges[0].Relation != protocol.RelServes || d.Edges[0].FromNodeID != arts[0].ID || d.Edges[0].ToNodeID != goal.ID {
		t.Fatalf("edges = %+v", d.Edges)
	}
	if len(d.Evidence) != 1 || d.Evidence[0].Kind != protocol.EvidenceFileChange || d.Evidence[0].SourceURI != "a.txt" ||
		d.Evidence[0].ContentHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("evidence = %+v", d.Evidence)
	}
	// Review Focus: editing the same file again keeps one artifact node.
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world\n"), 0o644)
	f.observe(t, Observation{Tool: "file.edit", Risk: "yellow", Root: root, Args: json.RawMessage(`{"path":"a.txt"}`)})
	d = f.detail(t)
	if arts := kinds(d, protocol.NodeArtifact); len(arts) != 1 || arts[0].Revision != 2 || len(d.Evidence) != 2 {
		t.Fatalf("after second edit: artifacts = %+v evidence = %d", arts, len(d.Evidence))
	}
}

func TestPlanCreatesCriteria(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.plan", Risk: "green", Output: planOut})
	d := f.detail(t)
	crits := kinds(d, protocol.NodeCriterion)
	if len(crits) != 2 || crits[0].Status != "pending" || !strings.Contains(string(crits[0].Content), "go test ./...") {
		t.Fatalf("criteria = %+v", crits)
	}
	if len(d.Edges) != 2 || d.Edges[0].Relation != protocol.RelRequires {
		t.Fatalf("edges = %+v", d.Edges)
	}
	f.observe(t, Observation{Tool: "verification.plan", Risk: "green", Output: planOut})
	if d = f.detail(t); len(kinds(d, protocol.NodeCriterion)) != 2 {
		t.Fatalf("re-plan duplicated criteria: %d", len(kinds(d, protocol.NodeCriterion)))
	}
}

func TestRunRecordsAttemptsAndMatchesCriteria(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.plan", Risk: "green", Output: planOut})
	f.observe(t, Observation{Tool: "verification.run", Risk: "yellow", Output: runOut("failed", "passed")})
	d := f.detail(t)
	if len(d.Attempts) != 2 {
		t.Fatalf("attempts = %+v", d.Attempts)
	}
	crits := kinds(d, protocol.NodeCriterion)
	a0, a1 := d.Attempts[0], d.Attempts[1]
	if a0.Status != protocol.AttemptFailed || a0.ExitCode == nil || *a0.ExitCode != 1 || a0.CriterionNodeID != crits[0].ID || a0.EvidenceID == "" || a0.CheckType != "command" {
		t.Fatalf("attempt 0 = %+v", a0)
	}
	if a1.Status != protocol.AttemptPassed || a1.CriterionNodeID != crits[1].ID || a1.ExitCode == nil || *a1.ExitCode != 0 {
		t.Fatalf("attempt 1 = %+v", a1)
	}
	if crits[0].Status != "failed" || crits[1].Status != "passed" {
		t.Fatalf("criterion statuses = %s, %s", crits[0].Status, crits[1].Status)
	}
	if len(d.Evidence) != 2 || d.Evidence[0].Kind != protocol.EvidenceVerificationOutput {
		t.Fatalf("evidence = %+v", d.Evidence)
	}
}

func TestAdHocRunHasNullCriterion(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.run", Risk: "yellow",
		Output: `{"status":"failed","results":[{"label":"x","command":"make lint","status":"failed","exit_code":2}]}`})
	d := f.detail(t)
	if len(d.Attempts) != 1 || d.Attempts[0].CriterionNodeID != "" || d.Attempts[0].Status != protocol.AttemptFailed {
		t.Fatalf("attempts = %+v", d.Attempts)
	}
	if u := Unresolved(d); len(u) != 0 {
		t.Fatalf("ad hoc run affected closing: %v", u)
	}
}

func TestOutputSummaryCappedAt2KB(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	out := `{"status":"passed","results":[{"label":"x","command":"c","status":"passed","output":"` + strings.Repeat("a", 10000) + `"}]}`
	f.observe(t, Observation{Tool: "verification.run", Risk: "yellow", Output: out})
	d := f.detail(t)
	if len(d.Evidence) != 1 || len(d.Evidence[0].Summary) > 2048 || len(d.Evidence[0].Summary) == 0 {
		t.Fatalf("summary length = %d", len(d.Evidence[0].Summary))
	}
}

func TestBrowserVerifyAttempt(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "browser.verify", Risk: "yellow",
		Output: `{"status":"not_run","command":"npx playwright test","reason":"requires isolated compute"}`})
	d := f.detail(t)
	if len(d.Attempts) != 1 || d.Attempts[0].CheckType != "browser" || d.Attempts[0].Status != protocol.AttemptNotRun ||
		d.Attempts[0].Command != "npx playwright test" {
		t.Fatalf("attempts = %+v", d.Attempts)
	}
}

func TestFailedToolRecordsFactOnly(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "file.search", Risk: "green", Err: "boom"})
	d := f.detail(t)
	facts := kinds(d, protocol.NodeFact)
	if len(facts) != 1 || len(d.Evidence) != 1 || d.Evidence[0].Kind != protocol.EvidenceToolError || d.Evidence[0].NodeID != facts[0].ID {
		t.Fatalf("facts = %+v evidence = %+v", facts, d.Evidence)
	}
	if d.Work.WorkflowDepth != protocol.DepthDirect {
		t.Fatalf("failed read escalated depth")
	}
	if err := f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, ""); err != nil {
		t.Fatal(err)
	}
	if d = f.detail(t); d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("a failed tool kept the work open: %s", d.Work.Status)
	}
}

func TestMalformedOutputIgnored(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	for _, tool := range []string{"verification.plan", "verification.run", "browser.verify"} {
		for _, out := range []string{"", "not json", "{", "{}", `{"results":[]}`} {
			if err := f.svc.Observe(context.Background(), f.th.ID, Observation{Tool: tool, Risk: "yellow", Output: out}); err != nil {
				t.Fatalf("%s %q: %v", tool, out, err)
			}
		}
	}
	d := f.detail(t)
	if len(kinds(d, protocol.NodeCriterion)) != 0 || len(d.Attempts) != 0 || len(d.Evidence) != 0 {
		t.Fatalf("malformed output was recorded: %+v", d)
	}
}

func TestEndRules(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f fixture, t *testing.T)
		status string
		paused bool
		want   string
	}{
		{"no criteria completes", func(fixture, *testing.T) {}, protocol.TurnCompleted, false, protocol.WorkCompleted},
		{"failed criterion stays open", func(f fixture, t *testing.T) {
			f.observe(t, Observation{Tool: "verification.plan", Output: planOut})
			f.observe(t, Observation{Tool: "verification.run", Output: runOut("failed", "passed")})
		}, protocol.TurnCompleted, false, protocol.WorkOpen},
		{"unrun criterion stays open", func(f fixture, t *testing.T) {
			f.observe(t, Observation{Tool: "verification.plan", Output: planOut})
		}, protocol.TurnCompleted, false, protocol.WorkOpen},
		{"all passed completes", func(f fixture, t *testing.T) {
			f.observe(t, Observation{Tool: "verification.plan", Output: planOut})
			f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed")})
		}, protocol.TurnCompleted, false, protocol.WorkCompleted},
		{"interrupted stays open", func(fixture, *testing.T) {}, protocol.TurnInterrupted, false, protocol.WorkOpen},
		{"failed turn stays open", func(fixture, *testing.T) {}, protocol.TurnFailed, false, protocol.WorkOpen},
		{"paused stays open", func(fixture, *testing.T) {}, protocol.TurnCompleted, true, protocol.WorkOpen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.begin(t, "g")
			c.setup(f, t)
			if err := f.svc.End(context.Background(), f.th.ID, c.status, c.paused, ""); err != nil {
				t.Fatal(err)
			}
			d := f.detail(t)
			if d.Work.Status != c.want {
				t.Fatalf("status = %s, want %s", d.Work.Status, c.want)
			}
			if (c.want == protocol.WorkCompleted) != (d.Work.CompletedAt != nil) {
				t.Fatalf("completed_at = %v", d.Work.CompletedAt)
			}
		})
	}
}

func TestEndWithoutOpenWorkIsNoop(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Observe(context.Background(), f.th.ID, Observation{Tool: "file.write"}); err != nil {
		t.Fatal(err)
	}
}

func TestFileChangeAfterPassReopens(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.plan", Output: planOut})
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed")})
	if u := Unresolved(f.detail(t)); len(u) != 0 {
		t.Fatalf("unresolved after pass = %v", u)
	}
	f.observe(t, Observation{Tool: "file.edit", Risk: "yellow", Args: json.RawMessage(`{"path":"a.go"}`)})
	if u := Unresolved(f.detail(t)); len(u) != 2 {
		t.Fatalf("unresolved after later edit = %v, want both criteria", u)
	}
	f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, "")
	if d := f.detail(t); d.Work.Status != protocol.WorkOpen {
		t.Fatalf("status = %s, want open", d.Work.Status)
	}
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed")})
	if u := Unresolved(f.detail(t)); len(u) != 0 {
		t.Fatalf("unresolved after re-run = %v", u)
	}
}

func TestFailuresCounted(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.st.Close()
	if err := f.svc.Observe(context.Background(), f.th.ID, Observation{Tool: "file.write", Args: json.RawMessage(`{"path":"a"}`)}); err == nil {
		t.Fatal("expected an error from a closed store")
	}
	if f.svc.Failures.Load() == 0 {
		t.Fatal("failure was not counted")
	}
}

func TestPathSpellingsShareOneArtifact(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644)
	sum := sha256.Sum256([]byte("hello\n"))
	for _, p := range []string{"a.txt", "./a.txt", " a.txt ", filepath.Join(root, "a.txt")} {
		args, _ := json.Marshal(map[string]string{"path": p})
		f.observe(t, Observation{Tool: "file.edit", Risk: "yellow", Root: root, Args: args})
	}
	d := f.detail(t)
	arts := kinds(d, protocol.NodeArtifact)
	if len(arts) != 1 || arts[0].Title != "a.txt" || arts[0].Revision != 4 {
		t.Fatalf("artifacts = %+v", arts)
	}
	for _, ev := range d.Evidence {
		if ev.ContentHash != hex.EncodeToString(sum[:]) || ev.SourceURI != "a.txt" {
			t.Fatalf("evidence = %+v", ev)
		}
	}
}

func TestReplanSupersedesDroppedCriteria(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.plan", Output: planOut})
	f.observe(t, Observation{Tool: "verification.plan", Output: `{"checks":[{"label":"unit","command":"go test ./..."}]}`})
	f.observe(t, Observation{Tool: "verification.run", Output: `{"status":"passed","results":[{"label":"unit","command":"go test ./...","status":"passed"}]}`})
	if u := Unresolved(f.detail(t)); len(u) != 0 {
		t.Fatalf("dropped criterion still blocks: %v", u)
	}
	f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, "")
	if d := f.detail(t); d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("status = %s", d.Work.Status)
	}
}
