package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Catches classifiers that escalate ordinary questions or miss explicit design requests.
func TestWorkflowDepthClassification(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"What does this function do?", "direct"},
		{"What is the architecture of this package?", "direct"},
		{"Explain the design patterns used here", "direct"},
		{"Explain how to change the public API", "direct"},
		{"What happens if we delete billing records?", "direct"},
		{"Add a public API endpoint", "designed"},
		{"Update the protocol", "designed"},
		{"Modify the persisted schema", "designed"},
		{"Replace the archive file format", "designed"},
		{"Implement a database migration", "designed"},
		{"Change database compatibility", "designed"},
		{"Modify authentication", "designed"},
		{"Add an authorization check", "designed"},
		{"Change secret handling", "designed"},
		{"Update the trust boundary", "designed"},
		{"Implement billing", "designed"},
		{"Change payment behavior", "designed"},
		{"Introduce money movement", "designed"},
		{"Delete records with destructive recovery", "designed"},
		{"Add an internal log line", "direct"},
		{"Design a replacement cache", "designed"},
		{"Make an architectural decision about storage", "designed"},
	} {
		if got := InitialDepth(tc.text); got != tc.want {
			t.Errorf("InitialDepth(%q)=%s want %s", tc.text, got, tc.want)
		}
	}
	for _, tc := range []struct{ tool, risk, path, want string }{
		{"file.read", "green", "internal/protocol/work.go", "direct"},
		{"file.write", "yellow", "readme.md", "guided"},
		{"shell.run", "yellow", "", "guided"},
		{"verification.run", "yellow", "", "guided"},
		{"file.edit", "yellow", "internal/store/migrations/001.sql", "designed"},
		{"file.edit", "yellow", "internal/protocol/work.go", "designed"},
		{"file.edit", "yellow", "schemas/public.json", "designed"},
		{"file.edit", "yellow", "file_formats/archive.go", "designed"},
		{"file.edit", "yellow", "internal/auth/login.go", "designed"},
		{"file.edit", "yellow", "internal/security/secret.go", "designed"},
		{"file.edit", "yellow", "internal/trust_boundary/check.go", "designed"},
		{"file.edit", "yellow", "internal/billing/pay.go", "designed"},
		{"file.edit", "yellow", "internal/payment/pay.go", "designed"},
		{"file.edit", "yellow", "internal/recovery/destructive.go", "designed"},
	} {
		args, _ := json.Marshal(map[string]string{"path": tc.path})
		if got := ObservedDepth("direct", protocol.WorkDetail{}, Observation{Tool: tc.tool, Risk: tc.risk, Args: args}); got != tc.want {
			t.Errorf("%s %s=%s want %s", tc.tool, tc.path, got, tc.want)
		}
	}
	d := protocol.WorkDetail{Evidence: []protocol.Evidence{{Kind: protocol.EvidenceFileChange, SourceURI: "internal/work/a.go"}}}
	for _, tc := range []struct{ tool, want string }{{"file.read", "direct"}, {"file.write", "designed"}} {
		if got := ObservedDepth("direct", d, Observation{Tool: tc.tool, Args: json.RawMessage(`{"path":"internal/store/b.go"}`)}); got != tc.want {
			t.Errorf("multi-area %s=%s want %s", tc.tool, got, tc.want)
		}
	}
}

// Catches topic phrases and polite explanatory wrappers being mistaken for implementation intent.
func TestWorkflowDepthExplanatoryIntentPrecedesDesignTopics(t *testing.T) {
	for _, text := range []string{"What is an architecture decision?", "Explain the design plan", "How do I choose an architecture?", "Can you explain how to implement billing?", "Could you describe the public API?", "Please explain the architecture decision"} {
		if got := InitialDepth(text); got != "direct" {
			t.Errorf("InitialDepth(%q)=%s want direct", text, got)
		}
	}
	for _, text := range []string{"Can you design a replacement cache?", "Please implement billing", "Could you update the public API?"} {
		if got := InitialDepth(text); got != "designed" {
			t.Errorf("InitialDepth(%q)=%s want designed", text, got)
		}
	}
}

// Catches approved selections completing work after their inspection/criterion support is invalidated.
func TestCompletionBlockersRequiresActiveSolutionSupport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.WorkDetail)
	}{
		{"explicitly stale evidence", func(d *protocol.WorkDetail) { stale := time.Unix(3, 0); d.Evidence[0].StaleAt = &stale }},
		{"rerun obsolete evidence", func(d *protocol.WorkDetail) {
			d.Evidence = append(d.Evidence, protocol.Evidence{ID: "evd_latest", WorkID: "wrk_test"})
			d.Attempts = []protocol.VerificationAttempt{{ID: "old", CriterionNodeID: "criterion", Status: "passed", EvidenceID: "evd_test", StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(1, 0)}, {ID: "latest", CriterionNodeID: "criterion", Status: "passed", EvidenceID: "evd_latest", StartedAt: time.Unix(2, 0), FinishedAt: time.Unix(2, 0)}}
		}},
		{"superseded supporting criterion", func(d *protocol.WorkDetail) { d.Nodes[4].Status = "superseded" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := readinessFixture()
			d.Nodes[0].Status = "completed"
			tc.mutate(&d)
			if got := CompletionBlockers(d); len(got) == 0 {
				t.Fatal("completed without active solution support")
			}
		})
	}
	// A separate supported solution must not excuse another required decision's missing support.
	d := readinessFixture()
	d.Nodes[0].Status = "completed"
	d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "unsupported", WorkID: "wrk_test", Kind: "decision", Title: "Required unsupported choice", Status: "approved", Content: json.RawMessage(`{"required":true}`)})
	d.Edges = append(d.Edges, protocol.WorkEdge{WorkID: "wrk_test", FromNodeID: "unsupported", Relation: "selects", ToNodeID: "opt"}, protocol.WorkEdge{WorkID: "wrk_test", FromNodeID: "criterion", Relation: "verifies", ToNodeID: "unsupported"})
	if got := CompletionBlockers(d); len(got) == 0 {
		t.Fatal("unsupported required decision hidden by supported solution")
	}
}

// Catches resolving an unknown with evidence invalidated by a later verification run.
func TestPrepareUpdateUnknownResolutionNeedsActiveEvidence(t *testing.T) {
	d, r := graphFixture()
	d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "risk", WorkID: "wrk_test", Kind: "unknown", Status: "open", Revision: 2, Title: "Risk"})
	d.Evidence = append(d.Evidence, protocol.Evidence{ID: "evd_new", WorkID: "wrk_test"})
	d.Attempts = []protocol.VerificationAttempt{{ID: "old", CriterionNodeID: "criterion", EvidenceID: "evd_test", StartedAt: time.Unix(1, 0)}, {ID: "new", CriterionNodeID: "criterion", EvidenceID: "evd_new", StartedAt: time.Unix(2, 0)}}
	r.Nodes[3].EvidenceIDs = []string{"evd_new"}
	r.Nodes[6].EvidenceIDs = []string{"evd_new"}
	r.Nodes = append(r.Nodes, protocol.WorkNodeChange{ID: "risk", ExpectedRevision: 2, FromStatus: "open", ToStatus: "resolved", EvidenceIDs: []string{"evd_test"}})
	if _, err := PrepareUpdate(d, r, time.Time{}); err == nil {
		t.Fatal("resolved with obsolete evidence")
	}
}

// Catches ambiguous typed metadata and source-kind/evidence checks omitted from graph validation.
func TestPrepareUpdateStructuralIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*protocol.WorkDetail, *protocol.WorkUpdateRequest)
	}{
		{"duplicate metadata", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[3].Content = json.RawMessage(`{"required":true,"required":false}`)
		}},
		{"case alias metadata", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[3].Content = json.RawMessage(`{"Required":false,"required":true}`)
		}},
		{"non goal required metadata", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[1].Content = json.RawMessage(`{"required":true}`)
		}},
		{"missing existing evidence link", "missing", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			d.Nodes[1].EvidenceIDs = []string{"absent"}
		}},
		{"stale supporting evidence", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			now := time.Now()
			d.Evidence[0].StaleAt = &now
		}},
		{"unavailable supporting evidence", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			d.Evidence[0].Availability = "unavailable"
		}},
		{"unsupported existing edge", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			d.Edges = append(d.Edges, protocol.WorkEdge{WorkID: "wrk_test", FromNodeID: "fact", Relation: "bogus", ToNodeID: "goal"})
		}},
		{"bad depends endpoints", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Edges = append(r.Edges, protocol.WorkEdgeChange{From: "task", Relation: "depends_on", To: "fact"})
		}},
		{"unsupported support source", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Edges = append(r.Edges, protocol.WorkEdgeChange{From: "non", Relation: "supports", To: "dec"})
		}},
		{"candidate unapproved source", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Edges[6].To = "dec"
			r.Nodes[3].ToStatus = "proposed"
		}},
		{"candidate missing evidence", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[6].EvidenceIDs = nil }},
		{"candidate traversal", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[6].Content = json.RawMessage(`{"category":"path","semantic_key":"key","text":"x","scope":"../outside"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r := graphFixture()
			tc.mutate(&d, &r)
			_, err := PrepareUpdate(d, r, time.Time{})
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != tc.code {
				t.Fatalf("error=%v want %s", err, tc.code)
			}
		})
	}
}

// Catches evidence-only changes losing node concurrency predicates when readiness cannot advance.
func TestPrepareUpdateEvidenceOnlyRetainsNodeCheck(t *testing.T) {
	d, r := graphFixture()
	r.Nodes[3].ToStatus = "proposed"
	d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "waiting", WorkID: "wrk_test", Kind: "task", Status: "pending", Revision: 3, Title: "Waiting", Content: json.RawMessage(`{"required":true}`)})
	r.Nodes = append(r.Nodes, protocol.WorkNodeChange{ID: "waiting", ExpectedRevision: 3, FromStatus: "pending", ToStatus: "ready", EvidenceIDs: []string{"evd_test"}})
	p, err := PrepareUpdate(d, r, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transitions) != 0 || len(p.NodeChecks) != 1 || p.NodeChecks[0] != (protocol.WorkNodeCheck{ID: "waiting", ExpectedRevision: 3, ExpectedStatus: "pending"}) {
		t.Fatalf("delta=%+v", p)
	}
	if got := p.EvidenceLinks[len(p.EvidenceLinks)-1]; got.NodeID != "waiting" || got.EvidenceID != "evd_test" {
		t.Fatalf("link=%+v", got)
	}
}

// Catches secret content hidden outside candidate text and obsolete verification evidence supporting a decision.
func TestPrepareUpdateRejectsUnsafeCandidateAndObsoleteSupport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.WorkDetail, *protocol.WorkUpdateRequest)
	}{
		{"secret semantic key", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[6].Content = json.RawMessage(`{"category":"command","semantic_key":"sk-123456789012345678901234567890","text":"x","scope":"."}`)
		}},
		{"secret extension", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[6].Content = json.RawMessage(`{"category":"command","semantic_key":"test-command","text":"x","scope":".","notes":"sk-123456789012345678901234567890"}`)
		}},
		{"obsolete verification evidence", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			d.Evidence = append(d.Evidence, protocol.Evidence{ID: "evd_new", WorkID: "wrk_test"})
			d.Attempts = []protocol.VerificationAttempt{{ID: "old", CriterionNodeID: "criterion", EvidenceID: "evd_test", StartedAt: time.Unix(1, 0)}, {ID: "new", CriterionNodeID: "criterion", EvidenceID: "evd_new", StartedAt: time.Unix(2, 0)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r := graphFixture()
			tc.mutate(&d, &r)
			if _, err := PrepareUpdate(d, r, time.Time{}); err == nil {
				t.Fatal("unsafe candidate/support accepted")
			}
		})
	}
}

// Catches off-by-one limits that reject a valid batch exactly at the declared bounds.
func TestPrepareUpdateAcceptsDeclaredBounds(t *testing.T) {
	d, r := graphFixture()
	r.Rationale = strings.Repeat("x", 2048)
	r.Nodes[1].Ref = strings.Repeat("r", 64)
	r.Nodes[1].Title = strings.Repeat("t", 512)
	r.Nodes[1].Content = json.RawMessage(`{"note":"` + strings.Repeat("x", 8181) + `"}`)
	for i := 7; i < 32; i++ {
		r.Nodes = append(r.Nodes, protocol.WorkNodeChange{Ref: fmt.Sprintf("non%d", i), Kind: "non_goal", Title: "Additional scope"})
	}
	for i := 10; i < 64; i++ {
		from := fmt.Sprintf("non%d", 7+(i-10)/25)
		to := fmt.Sprintf("non%d", 7+(i-10)%25)
		r.Edges = append(r.Edges, protocol.WorkEdgeChange{From: from, Relation: "contradicts", To: to})
	}
	for i := 1; i < 16; i++ {
		id := fmt.Sprintf("evd_%d", i)
		d.Evidence = append(d.Evidence, protocol.Evidence{ID: id, WorkID: "wrk_test"})
		r.Nodes[3].EvidenceIDs = append(r.Nodes[3].EvidenceIDs, id)
	}
	p, err := PrepareUpdate(d, r, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Creates) != 32 || len(p.Edges) != 64 {
		t.Fatalf("counts=%d/%d", len(p.Creates), len(p.Edges))
	}
}

func graphFixture() (protocol.WorkDetail, protocol.WorkUpdateRequest) {
	d := protocol.WorkDetail{Work: protocol.Work{ID: "wrk_test", Revision: 4, WorkflowDepth: "guided", Status: "open"}, Nodes: []protocol.WorkNode{
		{ID: "criterion", WorkID: "wrk_test", Kind: "criterion", Title: "Unit tests", Status: "pending", Revision: 1},
		{ID: "fact", WorkID: "wrk_test", Kind: "fact", Title: "Inspected code", Status: "active", Revision: 1, EvidenceIDs: []string{"evd_test"}},
		{ID: "goal", WorkID: "wrk_test", Kind: "goal", Title: "Goal", Status: "active", Revision: 1},
		{ID: "artifact", WorkID: "wrk_test", Kind: "artifact", Title: "Artifact", Status: "active", Revision: 1},
	}, Evidence: []protocol.Evidence{{ID: "evd_test", WorkID: "wrk_test", Kind: "file_change", SourceURI: "internal/work/a.go"}}}
	req := protocol.WorkUpdateRequest{WorkID: "wrk_test", ExpectedRevision: 4, Rationale: "Use existing code", Nodes: []protocol.WorkNodeChange{
		{Ref: "req", Kind: "requirement", Title: "Works correctly", Content: json.RawMessage(`{"required":true}`)},
		{Ref: "non", Kind: "non_goal", Title: "No migration"},
		{Ref: "opt", Kind: "option", Title: "Standard library", Content: json.RawMessage(`{"solution_rung":3}`)},
		{Ref: "dec", Kind: "decision", Title: "Use standard library", ToStatus: "approved", Content: json.RawMessage(`{"required":true}`), EvidenceIDs: []string{"evd_test"}},
		{Ref: "task", Kind: "task", Title: "Implement", Content: json.RawMessage(`{"required":true}`)},
		{Ref: "unk", Kind: "unknown", Title: "Check performance", Content: json.RawMessage(`{"blocking":false}`)},
		{Ref: "candidate", Kind: "memory_candidate", Title: "Durable command", Content: json.RawMessage(`{"category":"command","semantic_key":"test-command","text":"Use go test ./...","scope":"."}`), EvidenceIDs: []string{"evd_test"}},
	}, Edges: []protocol.WorkEdgeChange{
		{From: "dec", Relation: "selects", To: "opt"},
		{From: "criterion", Relation: "verifies", To: "dec"},
		{From: "criterion", Relation: "verifies", To: "task"},
		{From: "task", Relation: "implements", To: "dec"},
		{From: "task", Relation: "implements", To: "req"},
		{From: "fact", Relation: "supports", To: "dec"},
		{From: "candidate", Relation: "candidate_for", To: "fact"},
		{From: "req", Relation: "requires", To: "criterion"},
		{From: "artifact", Relation: "serves", To: "goal"},
		{From: "unk", Relation: "contradicts", To: "req"},
	}}
	return d, req
}

// Catches unresolved local references, dropped evidence links, and trusted requested readiness.
func TestPrepareUpdateResolvesAtomicDelta(t *testing.T) {
	d, req := graphFixture()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	p, err := PrepareUpdate(d, req, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.WorkID != "wrk_test" || p.ExpectedRevision != 4 || p.WorkflowDepth != "guided" || len(p.Creates) != 7 || len(p.Edges) != 10 || len(p.EvidenceLinks) != 2 {
		t.Fatalf("prepared=%+v", p)
	}
	if p.Creates[4].Status != "ready" || p.Creates[3].Status != "approved" || !p.Creates[0].CreatedAt.Equal(now) {
		t.Fatalf("creates=%+v", p.Creates)
	}
	if p.Edges[0].FromNodeID != p.Creates[3].ID || p.Edges[0].ToNodeID != p.Creates[2].ID {
		t.Fatalf("unresolved edge=%+v", p.Edges[0])
	}
	if p.EvidenceLinks[0].NodeID != p.Creates[3].ID || p.EvidenceLinks[0].EvidenceID != "evd_test" {
		t.Fatalf("link=%+v", p.EvidenceLinks[0])
	}
	again, err := PrepareUpdate(d, req, now)
	if err != nil || !reflect.DeepEqual(p, again) {
		t.Fatal("preparation is not deterministic")
	}
	if d.Nodes[0].Status != "pending" || req.Nodes[4].ToStatus != "" {
		t.Fatal("preparation mutated input")
	}
}

// Each case names a malformed batch that must never reach transactional persistence.
func TestPrepareUpdateRejectsInvalidBatch(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*protocol.WorkDetail, *protocol.WorkUpdateRequest)
	}{
		{"wrong work", "foreign", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.WorkID = "wrk_other" }},
		{"stale work revision", "stale", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.ExpectedRevision = 3 }},
		{"closed work", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { d.Work.Status = "completed" }},
		{"duplicate ref", "duplicate", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[1].Ref = "req" }},
		{"invalid ref", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[1].Ref = "bad ref" }},
		{"create mixed with transition", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[1].ID = "fact" }},
		{"engine owned create", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[1].Kind = "fact" }},
		{"unsupported kind", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[1].Kind = "mystery" }},
		{"unsupported initial state", "transition", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[4].ToStatus = "completed" }},
		{"missing node", "missing", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Edges[0].To = "absent" }},
		{"foreign node", "foreign", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { d.Nodes[1].WorkID = "wrk_other" }},
		{"foreign existing edge", "foreign", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			d.Edges = []protocol.WorkEdge{{WorkID: "wrk_other", FromNodeID: "fact", Relation: "serves", ToNodeID: "goal"}}
		}},
		{"missing evidence", "missing", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[3].EvidenceIDs = []string{"absent"}
		}},
		{"foreign evidence", "foreign", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { d.Evidence[0].WorkID = "wrk_other" }},
		{"duplicate evidence", "duplicate", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[3].EvidenceIDs = []string{"evd_test", "evd_test"}
		}},
		{"unknown relation", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Edges[0].Relation = "unknown" }},
		{"invalid selection endpoints", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Edges[0].From = "task" }},
		{"missing selection", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Edges = r.Edges[1:] }},
		{"rung zero", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[2].Content = json.RawMessage(`{"solution_rung":0}`)
		}},
		{"rung seven", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[2].Content = json.RawMessage(`{"solution_rung":7}`)
		}},
		{"missing inspected evidence", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[3].EvidenceIDs = nil }},
		{"missing verification link", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Edges = append(r.Edges[:1], r.Edges[2:]...)
		}},
		{"unknown gate kind", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[3].Content = json.RawMessage(`{"required":true,"gate_kind":"routine"}`)
		}},
		{"gated decision self approval", "approval_required", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[3].Content = json.RawMessage(`{"gate_kind":"security"}`)
			r.Nodes[3].ToStatus = "approved"
		}},
		{"gate on non decision", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[0].Content = json.RawMessage(`{"gate_kind":"security"}`)
		}},
		{"direct update without escalation", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { d.Work.WorkflowDepth = "direct" }},
		{"depth downgrade", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			d.Work.WorkflowDepth = "designed"
			r.WorkflowDepth = "guided"
		}},
		{"title bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[0].Title = strings.Repeat("x", 513)
		}},
		{"ref bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Nodes[0].Ref = strings.Repeat("x", 65) }},
		{"rationale bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) { r.Rationale = strings.Repeat("x", 2049) }},
		{"content bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[0].Content = json.RawMessage(`{"note":"` + strings.Repeat("x", 8192) + `"}`)
		}},
		{"node count bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes = make([]protocol.WorkNodeChange, 33)
		}},
		{"edge count bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Edges = make([]protocol.WorkEdgeChange, 65)
		}},
		{"evidence count bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[0].EvidenceIDs = make([]string, 17)
		}},
		{"total request bound", "limit", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			for i := 0; i < 9; i++ {
				r.Nodes = append(r.Nodes, protocol.WorkNodeChange{Ref: "extra", Kind: "non_goal", Title: "extra", Content: json.RawMessage(`{"note":"` + strings.Repeat("x", 8000) + `"}`)})
			}
		}},
		{"malformed content", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[0].Content = json.RawMessage(`[]`)
		}},
		{"missing candidate source", "structure", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Edges = append(r.Edges[:6], r.Edges[7:]...)
		}},
		{"unsupported candidate category", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[6].Content = json.RawMessage(`{"category":"preference","semantic_key":"key","text":"x","scope":"."}`)
		}},
		{"secret candidate", "invalid", func(d *protocol.WorkDetail, r *protocol.WorkUpdateRequest) {
			r.Nodes[6].Content = json.RawMessage(`{"category":"command","semantic_key":"key","text":"api_key=sk-123456789012345678901234567890123456","scope":"."}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r := graphFixture()
			tc.mutate(&d, &r)
			_, err := PrepareUpdate(d, r, time.Time{})
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != tc.code || ve.Field == "" {
				t.Fatalf("error=%v want typed %s", err, tc.code)
			}
		})
	}
}

// Catches stale transition predicates and terminal nodes reopening through a delta.
func TestPrepareUpdateTransitions(t *testing.T) {
	for _, tc := range []struct{ kind, from, to, gate, code string }{
		{"decision", "proposed", "approved", "", ""}, {"decision", "proposed", "rejected", "", ""},
		{"decision", "approved", "superseded", "", ""}, {"decision", "rejected", "superseded", "", ""},
		{"task", "ready", "in_progress", "", ""}, {"task", "in_progress", "completed", "", ""}, {"task", "in_progress", "failed", "", ""}, {"task", "in_progress", "blocked", "", ""},
		{"unknown", "open", "resolved", "", ""},
		{"memory_candidate", "pending", "stale", "", ""}, {"memory_candidate", "pending", "rejected", "", ""},
		{"memory_candidate", "pending", "conflicted", "", ""},
		{"requirement", "active", "superseded", "", ""}, {"non_goal", "active", "superseded", "", ""}, {"option", "active", "superseded", "", ""},
		{"task", "completed", "pending", "", "transition"}, {"task", "pending", "completed", "", "transition"},
		{"task", "failed", "ready", "", "transition"}, {"task", "blocked", "in_progress", "", "transition"},
		{"unknown", "resolved", "open", "", "transition"}, {"unknown", "accepted_risk", "open", "", "transition"},
		{"decision", "proposed", "approved", "security", "approval_required"},
		{"decision", "proposed", "rejected", "security", "approval_required"},
		{"unknown", "open", "accepted_risk", "", "approval_required"},
		{"memory_candidate", "pending", "promoted", "", "transition"},
	} {
		t.Run(tc.kind+"/"+tc.from+"/"+tc.to+"/"+tc.gate, func(t *testing.T) {
			d, r := graphFixture()
			n := protocol.WorkNode{ID: "existing", WorkID: "wrk_test", Kind: tc.kind, Title: "Existing", Revision: 2, Status: tc.from, Content: json.RawMessage(`{}`)}
			if tc.kind == "decision" {
				n.EvidenceIDs = []string{"evd_test"}
				n.Content = json.RawMessage(`{"gate_kind":"` + tc.gate + `"}`)
				d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "oldopt", WorkID: "wrk_test", Kind: "option", Status: "active", Content: json.RawMessage(`{"solution_rung":1}`)})
				d.Edges = append(d.Edges, protocol.WorkEdge{WorkID: "wrk_test", FromNodeID: "existing", Relation: "selects", ToNodeID: "oldopt"})
				d.Edges = append(d.Edges, protocol.WorkEdge{WorkID: "wrk_test", FromNodeID: "criterion", Relation: "verifies", ToNodeID: "existing"})
			}
			if tc.kind == "unknown" {
				n.EvidenceIDs = []string{"evd_test"}
			}
			if tc.kind == "memory_candidate" {
				n.Content = json.RawMessage(`{"category":"command","semantic_key":"key","text":"x","scope":"."}`)
				n.EvidenceIDs = []string{"evd_test"}
				d.Edges = append(d.Edges, protocol.WorkEdge{WorkID: "wrk_test", FromNodeID: "existing", Relation: "candidate_for", ToNodeID: "fact"})
			}
			d.Nodes = append(d.Nodes, n)
			r.Nodes = append(r.Nodes, protocol.WorkNodeChange{ID: "existing", ExpectedRevision: 2, FromStatus: tc.from, ToStatus: tc.to})
			p, err := PrepareUpdate(d, r, time.Time{})
			if tc.code != "" {
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.Code != tc.code {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Transitions) != 1 || p.Transitions[0].ToStatus != tc.to {
				t.Fatalf("transitions=%+v", p.Transitions)
			}
		})
	}
	for _, tc := range []struct {
		name, code string
		change     protocol.WorkNodeChange
	}{
		{"revision", "stale", protocol.WorkNodeChange{ID: "criterion", ExpectedRevision: 9, FromStatus: "pending", ToStatus: "ready"}},
		{"from state", "stale", protocol.WorkNodeChange{ID: "criterion", ExpectedRevision: 1, FromStatus: "completed", ToStatus: "ready"}},
		{"missing", "missing", protocol.WorkNodeChange{ID: "absent", ExpectedRevision: 1, FromStatus: "pending", ToStatus: "ready"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r := graphFixture()
			r.Nodes = append(r.Nodes, tc.change)
			_, err := PrepareUpdate(d, r, time.Time{})
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != tc.code {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

// Catches a later routine observation or lower requested depth reopening a simpler workflow.
func TestWorkflowDepthNeverDowngrades(t *testing.T) {
	for _, a := range []string{"direct", "guided", "designed"} {
		if got := MaxDepth("designed", a); got != "designed" {
			t.Errorf("MaxDepth=%s", got)
		}
	}
	if got := ObservedDepth("designed", protocol.WorkDetail{}, Observation{Tool: "file.write"}); got != "designed" {
		t.Fatalf("depth=%s", got)
	}
}

// Catches dependency cycles both wholly within the batch and closed through persisted edges.
func TestDependencyCycle(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "batch", true: "existing"}[existing], func(t *testing.T) {
			d, r := graphFixture()
			if existing {
				d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "prior", WorkID: "wrk_test", Kind: "task", Status: "pending", Revision: 1})
				d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "second", WorkID: "wrk_test", Kind: "task", Status: "pending", Revision: 1})
				d.Edges = append(d.Edges, protocol.WorkEdge{WorkID: "wrk_test", FromNodeID: "prior", Relation: "depends_on", ToNodeID: "second"})
				r.Edges = append(r.Edges, protocol.WorkEdgeChange{From: "second", Relation: "depends_on", To: "prior"})
			} else {
				r.Nodes = append(r.Nodes, protocol.WorkNodeChange{Ref: "other", Kind: "task", Title: "Other"})
				r.Edges = append(r.Edges, protocol.WorkEdgeChange{From: "task", Relation: "depends_on", To: "other"}, protocol.WorkEdgeChange{From: "other", Relation: "depends_on", To: "task"})
			}
			_, err := PrepareUpdate(d, r, time.Time{})
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != "cycle" {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

// Catches bypassing derived readiness by directly starting a stale ready task.
func TestPrepareUpdateCannotStartTaskWithLostReadiness(t *testing.T) {
	d, r := graphFixture()
	d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "waiting", WorkID: "wrk_test", Kind: "task", Status: "ready", Revision: 3, Title: "Waiting", Content: json.RawMessage(`{"required":true}`)})
	r.Nodes = append(r.Nodes, protocol.WorkNodeChange{ID: "waiting", ExpectedRevision: 3, FromStatus: "ready", ToStatus: "in_progress"})
	_, err := PrepareUpdate(d, r, time.Time{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Code != "structure" {
		t.Fatalf("started invalid ready task: %v", err)
	}
}

// Catches duplicate existing IDs and missing concurrency predicates silently overwriting graph state.
func TestPrepareUpdateDuplicateTransitionsAndDerivedDelta(t *testing.T) {
	d, r := graphFixture()
	d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "waiting", WorkID: "wrk_test", Kind: "task", Status: "pending", Revision: 3, Title: "Waiting"})
	change := protocol.WorkNodeChange{ID: "waiting", ExpectedRevision: 3, FromStatus: "pending", ToStatus: "ready"}
	r.Nodes = append(r.Nodes, change, change)
	_, err := PrepareUpdate(d, r, time.Time{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Code != "duplicate" {
		t.Fatalf("error=%v", err)
	}
	r.Nodes = r.Nodes[:len(r.Nodes)-2]
	p, err := PrepareUpdate(d, r, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transitions) != 1 || p.Transitions[0] != (protocol.WorkNodeTransition{ID: "waiting", ExpectedRevision: 3, FromStatus: "pending", ToStatus: "ready"}) {
		t.Fatalf("derived=%+v", p.Transitions)
	}
	if len(p.NodeChecks) != 1 || p.NodeChecks[0] != (protocol.WorkNodeCheck{ID: "waiting", ExpectedRevision: 3, ExpectedStatus: "pending"}) {
		t.Fatalf("derived checks=%+v", p.NodeChecks)
	}
}

// Catches proposed or missing global solution decisions allowing nontrivial tasks to start.
func TestDerivedReadinessNeedsActiveSolution(t *testing.T) {
	d := readinessFixture()
	d.Edges = append(d.Edges[:1], d.Edges[2:]...)
	d.Nodes[2].Status = "superseded"
	if got := DeriveTaskStatuses(d)["task"]; got != "pending" {
		t.Fatalf("without solution=%s", got)
	}
	d = readinessFixture()
	d.Nodes[0].Status = "ready"
	d.Nodes[1].Status = "pending"
	if got := DeriveTaskStatuses(d)["task"]; got != "blocked" {
		t.Fatalf("lost readiness=%s", got)
	}
}

func readinessFixture() protocol.WorkDetail {
	return protocol.WorkDetail{Work: protocol.Work{ID: "wrk_test", WorkflowDepth: "guided"}, Nodes: []protocol.WorkNode{
		{ID: "task", WorkID: "wrk_test", Kind: "task", Status: "pending", Revision: 1, Content: json.RawMessage(`{"required":true}`)},
		{ID: "dep", WorkID: "wrk_test", Kind: "task", Status: "completed", Revision: 1},
		{ID: "dec", WorkID: "wrk_test", Kind: "decision", Status: "approved", Revision: 1, Content: json.RawMessage(`{"required":true}`), EvidenceIDs: []string{"evd_test"}},
		{ID: "opt", WorkID: "wrk_test", Kind: "option", Status: "active", Content: json.RawMessage(`{"solution_rung":6}`)},
		{ID: "criterion", WorkID: "wrk_test", Kind: "criterion", Title: "Tests pass", Status: "passed"},
		{ID: "unk", WorkID: "wrk_test", Kind: "unknown", Title: "Risk", Status: "resolved", Content: json.RawMessage(`{"blocking":true}`)},
	}, Edges: []protocol.WorkEdge{
		{WorkID: "wrk_test", FromNodeID: "task", Relation: "depends_on", ToNodeID: "dep"},
		{WorkID: "wrk_test", FromNodeID: "task", Relation: "implements", ToNodeID: "dec"},
		{WorkID: "wrk_test", FromNodeID: "dec", Relation: "selects", ToNodeID: "opt"},
		{WorkID: "wrk_test", FromNodeID: "criterion", Relation: "verifies", ToNodeID: "task"},
		{WorkID: "wrk_test", FromNodeID: "criterion", Relation: "verifies", ToNodeID: "dec"},
	}, Evidence: []protocol.Evidence{{ID: "evd_test", WorkID: "wrk_test"}}, Attempts: []protocol.VerificationAttempt{{ID: "attempt", CriterionNodeID: "criterion", Status: "passed"}}}
}

// Catches any path that trusts requested ready before graph prerequisites are satisfied.
func TestDerivedReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*protocol.WorkDetail)
	}{
		{"all prerequisites", "ready", func(d *protocol.WorkDetail) {}},
		{"unfinished dependency", "pending", func(d *protocol.WorkDetail) { d.Nodes[1].Status = "in_progress" }},
		{"proposed decision", "pending", func(d *protocol.WorkDetail) { d.Nodes[2].Status = "proposed" }},
		{"unselected decision", "pending", func(d *protocol.WorkDetail) { d.Edges = append(d.Edges[:2], d.Edges[3:]...) }},
		{"blocking unknown", "pending", func(d *protocol.WorkDetail) { d.Nodes[5].Status = "open" }},
		{"accepted risk", "ready", func(d *protocol.WorkDetail) { d.Nodes[5].Status = "accepted_risk" }},
		{"missing criterion", "pending", func(d *protocol.WorkDetail) { d.Edges = append(d.Edges[:3], d.Edges[4:]...) }},
		{"superseded criterion", "pending", func(d *protocol.WorkDetail) { d.Nodes[4].Status = "superseded" }},
		{"optional task", "ready", func(d *protocol.WorkDetail) {
			d.Nodes[0].Content = json.RawMessage(`{"required":false}`)
			d.Edges = append(d.Edges[:3], d.Edges[4:]...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := readinessFixture()
			tc.mutate(&d)
			if got := DeriveTaskStatuses(d)["task"]; got != tc.want {
				t.Fatalf("status=%s want %s", got, tc.want)
			}
		})
	}
	for _, status := range []string{"in_progress", "completed", "failed", "blocked"} {
		d := readinessFixture()
		d.Nodes[0].Status = status
		if got := DeriveTaskStatuses(d)["task"]; got != status {
			t.Fatalf("terminal/started %s became %s", status, got)
		}
	}
	d, r := graphFixture()
	r.Nodes[3].ToStatus = "proposed"
	r.Nodes[4].ToStatus = "ready"
	p, err := PrepareUpdate(d, r, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Creates[4].Status != "pending" {
		t.Fatalf("forced ready=%s", p.Creates[4].Status)
	}
}

// Catches completion that skips graph requirements or accepts stale passing verification.
func TestCompletionBlockers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocked bool
		mutate  func(*protocol.WorkDetail)
	}{
		{"complete graph", false, func(d *protocol.WorkDetail) {}},
		{"pending task", true, func(d *protocol.WorkDetail) { d.Nodes[0].Status = "pending" }},
		{"ready task", true, func(d *protocol.WorkDetail) { d.Nodes[0].Status = "ready" }},
		{"running task", true, func(d *protocol.WorkDetail) { d.Nodes[0].Status = "in_progress" }},
		{"failed task", true, func(d *protocol.WorkDetail) { d.Nodes[0].Status = "failed" }},
		{"blocked task", true, func(d *protocol.WorkDetail) { d.Nodes[0].Status = "blocked" }},
		{"proposed decision", true, func(d *protocol.WorkDetail) { d.Nodes[2].Status = "proposed" }},
		{"unresolved criterion", true, func(d *protocol.WorkDetail) { d.Attempts = nil }},
		{"stale criterion", true, func(d *protocol.WorkDetail) { d.Nodes[4].Status = "stale" }},
		{"later file change", true, func(d *protocol.WorkDetail) {
			d.Evidence = append(d.Evidence, protocol.Evidence{Kind: "file_change", ObservedAt: time.Unix(1, 0)})
		}},
		{"open unknown", true, func(d *protocol.WorkDetail) { d.Nodes[5].Status = "open" }},
		{"no solution", true, func(d *protocol.WorkDetail) { d.Nodes[2].Status = "superseded" }},
		{"no task graph", true, func(d *protocol.WorkDetail) { d.Nodes = d.Nodes[2:] }},
		{"optional rejected nodes", false, func(d *protocol.WorkDetail) {
			d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "optional", Kind: "decision", Status: "rejected", Content: json.RawMessage(`{"required":false}`)}, protocol.WorkNode{ID: "obsolete", Kind: "criterion", Status: "superseded"})
		}},
		{"optional criterion", false, func(d *protocol.WorkDetail) {
			d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "optional-check", Kind: "criterion", Status: "pending", Content: json.RawMessage(`{"required":false}`)})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := readinessFixture()
			d.Nodes[0].Status = "completed"
			tc.mutate(&d)
			if got := CompletionBlockers(d); (len(got) > 0) != tc.blocked {
				t.Fatalf("blockers=%v want blocked=%v", got, tc.blocked)
			}
		})
	}
	d := protocol.WorkDetail{Work: protocol.Work{WorkflowDepth: "direct"}}
	if got := CompletionBlockers(d); len(got) != 0 {
		t.Fatalf("Direct blockers=%v", got)
	}
}

// Catches lost gate identity and ungated decisions accidentally demanding human approval.
func TestPrepareUpdateWorkflowGates(t *testing.T) {
	for _, kind := range []string{"public_contract", "persisted_schema", "security", "destructive", "billing", "architecture_choice", "accepted_risk"} {
		t.Run(kind, func(t *testing.T) {
			d, r := graphFixture()
			r.Nodes[3].ToStatus = "proposed"
			r.Nodes[3].Content = json.RawMessage(`{"required":true,"gate_kind":"` + kind + `"}`)
			p, err := PrepareUpdate(d, r, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Gates) != 1 || p.Gates[0].WorkID != "wrk_test" || p.Gates[0].NodeID != p.Creates[3].ID || p.Gates[0].NodeRevision != 1 || p.Gates[0].Kind != kind {
				t.Fatalf("gates=%+v", p.Gates)
			}
			if p.Creates[4].Status != "pending" {
				t.Fatalf("task=%s", p.Creates[4].Status)
			}
		})
	}
	d := readinessFixture()
	d.Nodes[2].Status = "proposed"
	if got := PendingWorkflowGates(d); len(got) != 0 {
		t.Fatalf("ungated=%v", got)
	}
	d.Nodes[2].Content = json.RawMessage(`{"required":true,"gate_kind":"security"}`)
	if got := PendingWorkflowGates(d); len(got) != 1 || got[0].NodeID != "dec" || got[0].NodeRevision != 1 {
		t.Fatalf("pending=%v", got)
	}
}
