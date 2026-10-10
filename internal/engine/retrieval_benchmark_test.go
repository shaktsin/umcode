//go:build darwin || linux

package engine

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func retrievalObservedFixture(t *testing.T) (*Engine, protocol.Thread, protocol.Turn, string) {
	t.Helper()
	e, th, turn, st := compilerEngine(t, true)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(t.Context(), protocol.Project{Name: "retrieval-fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	d := openWorkDetail(t, st, th.ID)
	if _, err = st.DB.Exec(`UPDATE threads SET project_id=? WHERE id=?`, p.ID, th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(`UPDATE works SET project_id=? WHERE id=?`, p.ID, d.Work.ID); err != nil {
		t.Fatal(err)
	}
	th.ProjectID = p.ID
	e.Projects = projects.New(st, e.Cfg)
	items, err := st.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	items[0].Text = "QUASAR_BOUNDARY: Value must return 42; preserve the exported signature."
	if err = st.SaveItem(t.Context(), items[0]); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":         "module retrievalfixture\n\ngo 1.24\n",
		"result.go":      "package retrievalfixture\n// QUASAR_BOUNDARY: Value must return 42; preserve the exported signature.\nfunc Value() int { return 1 }\n" + strings.Repeat("// Repository background unrelated to the requested change.\n", 150),
		"result_test.go": "package retrievalfixture\nimport \"testing\"\nfunc TestValue(t *testing.T) {if Value()!=42 {t.Fatal(Value())}}\n",
	} {
		if err = os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installMemoryProvider(t, e)
	e.Cfg.Models.ContextRetrieval = true
	e.Work.ContextRetrieval = true
	ctx := tools.WithScope(t.Context(), &tools.Scope{ThreadID: th.ID, ProjectID: p.ID, Root: root})
	out := e.runTool(ctx, t.Context(), th, turn, llm.ToolCall{ID: "observed", Name: tools.ToWire("file.read"), Args: json.RawMessage(`{"path":"result.go"}`)}, &fakePluginSnapshot{})
	if out.IsError {
		t.Fatal(out.Output)
	}
	var count int
	if err = st.DB.QueryRow(`SELECT count(*) FROM observed_excerpts WHERE work_id=?`, d.Work.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("actual observation count=%d err=%v", count, err)
	}
	return e, th, turn, root
}

func TestRetrievalRecoversEarlierContextWithoutQualityLoss(t *testing.T) {
	type measurement struct {
		calls, tokens int
		artifact      string
	}
	run := func(t *testing.T, enabled bool) measurement {
		e, th, turn, root := retrievalObservedFixture(t)
		provider := installMemoryProvider(t, e)
		e.Cfg.Models.ContextRetrieval = enabled
		provider.script = func(req llm.Request) []llm.Event {
			seen := map[string]bool{}
			for _, m := range req.Messages {
				if m.Role == llm.RoleTool {
					if m.IsError {
						t.Fatal(m.Result)
					}
					seen[tools.FromWire(m.ToolName)] = true
					if tools.FromWire(m.ToolName) == "verification.run" && !strings.Contains(m.Result, `"status":"passed"`) {
						t.Fatal(m.Result)
					}
				}
			}
			text := joined(req.Messages)
			for _, m := range req.Messages {
				text += m.Result
			}
			if !seen["file.write"] && (!strings.Contains(text, "QUASAR_BOUNDARY") || !strings.Contains(text, "func Value() int")) {
				return memoryCall("file.read", `{"path":"result.go"}`)
			}
			if !seen["file.write"] {
				return memoryCall("file.write", `{"path":"result.go","content":"package retrievalfixture\nfunc Value() int { return 42 }\n"}`)
			}
			if !seen["verification.run"] {
				return memoryCall("verification.run", `{"checks":[{"label":"repository tests","command":"go test ./..."}]}`)
			}
			return memoryAnswer(req)
		}
		e.runTurn(t.Context(), th, turn, resolved{sel: protocol.ModelSelection{Provider: "memory-script", Model: "deterministic"}, meta: models.Meta{ContextWindow: 200000, Tools: true}, limits: protocol.ExecutionLimits{MaxDurationMinutes: 2, MaxTokens: 1000000, MaxCostUSD: 100, MaxToolRounds: 10}}, protocol.TurnStartParams{Text: "Complete the QUASAR repository change and verify it."}, func() {})
		turns, err := e.Store.ListTurns(t.Context(), th.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range turns {
			if got.ID == turn.ID && got.Status != protocol.TurnCompleted {
				t.Fatalf("turn=%+v", got)
			}
		}
		var m measurement
		m.calls = len(provider.requests)
		for _, req := range provider.requests {
			m.tokens += tokens(req.System) + toolSpecTokens(req.Tools) + estimateMessageTokens(req.Messages)
		}
		b, err := os.ReadFile(filepath.Join(root, "result.go"))
		if err != nil {
			t.Fatal(err)
		}
		m.artifact = string(b)
		if m.artifact != "package retrievalfixture\nfunc Value() int { return 42 }\n" {
			t.Fatal("real edit missing")
		}
		verified := false
		for _, req := range provider.requests {
			for _, msg := range req.Messages {
				if tools.FromWire(msg.ToolName) == "verification.run" && strings.Contains(msg.Result, `"status":"passed"`) {
					verified = true
				}
			}
		}
		if !verified {
			t.Fatal("real verification missing")
		}
		if m.calls < 3 {
			t.Fatal("edit and verification did not execute")
		}
		return m
	}
	var baseline, treatment measurement
	t.Run("baseline", func(t *testing.T) { baseline = run(t, false) })
	t.Run("retrieval", func(t *testing.T) { treatment = run(t, true) })
	t.Logf("baseline calls=%d total_estimated_input=%d; retrieval calls=%d total_estimated_input=%d", baseline.calls, baseline.tokens, treatment.calls, treatment.tokens)
	if baseline.artifact != treatment.artifact || treatment.calls >= baseline.calls || treatment.tokens >= baseline.tokens {
		t.Fatalf("no useful savings: baseline=%+v treatment=%+v", baseline, treatment)
	}
}

func TestRetrievalRestartAndEdit(t *testing.T) {
	e, th, turn, root := retrievalObservedFixture(t)
	path := filepath.Join(t.TempDir(), "restart.db")
	if _, err := e.Store.DB.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store, e.Work.Store = reopened, reopened
	items, err := reopened.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	compile := func() string {
		got, ok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "QUASAR repository", root)
		if !ok {
			t.Fatal("compile declined")
		}
		return joined(got.msgs)
	}
	if got := compile(); !strings.Contains(got, `"Kind":"excerpt"`) || !strings.Contains(got, "QUASAR_BOUNDARY") {
		t.Fatalf("restart lost eligible retrieval: %s", got)
	}
	if err = os.WriteFile(filepath.Join(root, "result.go"), []byte("package retrievalfixture\nfunc Value() int {return 9}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := compile(); strings.Contains(got, `"Kind":"excerpt"`) {
		t.Fatal("external edit retained old excerpt")
	}
}

func TestRetrievalInactiveExcerptParent(t *testing.T) {
	e, th, turn, root := retrievalObservedFixture(t)
	if _, err := e.Store.DB.Exec(`UPDATE evidence SET availability='unavailable' WHERE id IN (SELECT evidence_id FROM observed_excerpts)`); err != nil {
		t.Fatal(err)
	}
	d := openWorkDetail(t, e.Store, th.ID)
	cs, _, err := e.retrieve(t.Context(), retrieval.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, WorkID: d.Work.ID, TurnID: turn.ID}, d, "QUASAR", root, nil, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Kind == "excerpt" {
			t.Fatal("excerpt parent is inactive")
		}
	}
}
