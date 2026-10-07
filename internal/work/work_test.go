package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

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
