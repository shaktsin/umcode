package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/fingerprint"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/vault"
	"github.com/shaktsin/umcode/internal/work"
)

const (
	testPlan   = `{"checks":[{"label":"unit","command":"go test ./..."}]}`
	testRunOK  = `{"status":"passed","results":[{"label":"unit","command":"go test ./...","status":"passed"}]}`
	testRunBad = `{"status":"failed","results":[{"label":"unit","command":"go test ./...","status":"failed","exit_code":1}]}`
)

func workEngine(t *testing.T) (*Engine, protocol.Thread, protocol.Turn, *store.Store) {
	t.Helper()
	e, th, turn, st := pluginHookEngine(t)
	e.Work = &work.Service{Store: st, Log: e.Log}
	if err := e.Work.Begin(t.Context(), th, "ship the change"); err != nil {
		t.Fatal(err)
	}
	return e, th, turn, st
}

func runWorkTool(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn, tool *engineTestTool) toolRunResult {
	t.Helper()
	e.Tools.Add(tool)
	call := llm.ToolCall{ID: "c-" + tool.name, Name: tools.ToWire(tool.name), Args: json.RawMessage(`{"path":"a.txt"}`)}
	return e.runTool(t.Context(), t.Context(), th, turn, call, &fakePluginSnapshot{})
}

func openWorkDetail(t *testing.T, st *store.Store, threadID string) protocol.WorkDetail {
	t.Helper()
	works, err := st.ListWorks(t.Context(), threadID)
	if err != nil || len(works) == 0 {
		t.Fatalf("works = %v err = %v", works, err)
	}
	d, err := st.GetWorkDetail(t.Context(), works[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func countKind(d protocol.WorkDetail, kind string) int {
	n := 0
	for _, node := range d.Nodes {
		if node.Kind == kind {
			n++
		}
	}
	return n
}

func TestRunToolObservedByWork(t *testing.T) {
	e, th, turn, st := workEngine(t)
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
	d := openWorkDetail(t, st, th.ID)
	if countKind(d, protocol.NodeCriterion) != 1 || d.Work.WorkflowDepth != protocol.DepthGuided {
		t.Fatalf("after plan: %+v", d)
	}
	runWorkTool(t, e, th, turn, &engineTestTool{name: "file.search", risk: tools.RiskGreen, err: errors.New("boom")})
	runWorkTool(t, e, th, turn, &engineTestTool{name: "web.fetch", risk: tools.RiskGreen, forbidden: true})
	d = openWorkDetail(t, st, th.ID)
	if countKind(d, protocol.NodeFact) != 2 {
		t.Fatalf("failed and denied tools should record two facts: %+v", d.Nodes)
	}
}

func TestReducerDoesNotChangeWorkEvidenceOrVaultHash(t *testing.T) {
	e, th, turn, st := workEngine(t)
	e.Cfg.Models.ToolResultReducers = true
	e.Work.Vault = &vault.Vault{Dir: t.TempDir()}
	output := reducerVerificationOutput(t)
	res := runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.run", risk: tools.RiskGreen, output: output})
	if res.Output != output || res.ModelOutput == output || strings.Contains(res.ModelOutput, passingReducerMarker) {
		t.Fatalf("result separation failed: %+v", res.Reduction)
	}
	d := openWorkDetail(t, st, th.ID)
	if len(d.Attempts) != 2 {
		t.Fatalf("Work saw %d attempts, want both checks", len(d.Attempts))
	}
	var found bool
	for _, ev := range d.Evidence {
		if ev.VaultHash == "" {
			continue
		}
		b, err := e.Work.Vault.Get(ev.VaultHash)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), passingReducerMarker) {
			found = true
		}
	}
	if !found {
		t.Fatal("vault evidence lost canonical passing output")
	}
}

func TestFinishTurnClosesOrKeepsOpen(t *testing.T) {
	finish := func(e *Engine, th protocol.Thread, turn protocol.Turn, err error) {
		e.finishTurn(context.Background(), th, turn, err, func() {})
	}
	t.Run("passing run completes", func(t *testing.T) {
		e, th, turn, st := workEngine(t)
		runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
		runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.run", risk: tools.RiskYellow, output: testRunOK})
		finish(e, th, turn, nil)
		if d := openWorkDetail(t, st, th.ID); d.Work.Status != protocol.WorkCompleted {
			t.Fatalf("status = %s", d.Work.Status)
		}
	})
	t.Run("failing run stays open and next turn continues it", func(t *testing.T) {
		e, th, turn, st := workEngine(t)
		runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
		runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.run", risk: tools.RiskYellow, output: testRunBad})
		finish(e, th, turn, nil)
		d := openWorkDetail(t, st, th.ID)
		if d.Work.Status != protocol.WorkOpen {
			t.Fatalf("status = %s", d.Work.Status)
		}
		if err := e.Work.Begin(t.Context(), th, "try again"); err != nil {
			t.Fatal(err)
		}
		if works, _ := st.ListWorks(t.Context(), th.ID); len(works) != 1 {
			t.Fatalf("next turn opened a new work: %d works", len(works))
		}
	})
	t.Run("interrupted stays open", func(t *testing.T) {
		e, th, turn, st := workEngine(t)
		finish(e, th, turn, context.Canceled)
		if d := openWorkDetail(t, st, th.ID); d.Work.Status != protocol.WorkOpen {
			t.Fatalf("status = %s", d.Work.Status)
		}
	})
	t.Run("paused stays open", func(t *testing.T) {
		e, th, turn, st := workEngine(t)
		e.markPaused(turn.ID)
		finish(e, th, turn, nil)
		if d := openWorkDetail(t, st, th.ID); d.Work.Status != protocol.WorkOpen {
			t.Fatalf("status = %s", d.Work.Status)
		}
		if e.takePaused(turn.ID) {
			t.Fatal("paused flag was not cleared by finishTurn")
		}
	})
}

func TestRecordingFailureDoesNotFailTool(t *testing.T) {
	e, th, turn, _ := workEngine(t)
	broken, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	e.Work = &work.Service{Store: broken, Log: e.Log}
	res := runWorkTool(t, e, th, turn, &engineTestTool{name: "file.search", risk: tools.RiskGreen, output: "found it"})
	if res.IsError || res.Output != "found it" {
		t.Fatalf("recording failure leaked into the tool result: %+v", res)
	}
	if e.Work.Failures.Load() == 0 {
		t.Fatal("recording failure was not counted")
	}
}

func TestArchiveAbandonsOpenWork(t *testing.T) {
	e, th, _, st := workEngine(t)
	if _, err := e.UpdateThread(t.Context(), th.ID, map[string]any{"archived": true}); err != nil {
		t.Fatal(err)
	}
	if d := openWorkDetail(t, st, th.ID); d.Work.Status != protocol.WorkAbandoned {
		t.Fatalf("status = %s", d.Work.Status)
	}
}

func TestEngineWithoutWorkServiceStillRuns(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	res := runWorkTool(t, e, th, turn, &engineTestTool{name: "file.search", risk: tools.RiskGreen, output: "ok"})
	if res.IsError || res.Output != "ok" {
		t.Fatalf("result = %+v", res)
	}
	e.finishTurn(context.Background(), th, turn, nil, func() {})
	if _, err := e.UpdateThread(t.Context(), th.ID, map[string]any{"archived": true}); err != nil {
		t.Fatal(err)
	}
}

func TestRunToolPassesUnclippedResultToWork(t *testing.T) {
	e, th, turn, st := workEngine(t)
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.run", risk: tools.RiskYellow,
		output: `{"status":"passed","results":[{"label":"unit","comm…[output truncated]`, raw: testRunOK})
	if d := openWorkDetail(t, st, th.ID); len(d.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 recorded from the raw result", len(d.Attempts))
	}
}

func TestFinishTurnPassesRootAndClosesStale(t *testing.T) {
	e, th, turn, st := workEngine(t)
	value := "ws1"
	e.Work.Workspace = func(ctx context.Context, root string) (fingerprint.Workspace, bool) {
		if root != "/proj" {
			t.Errorf("root = %q", root)
		}
		return fingerprint.Workspace{Value: value, Paths: []string{"a.go"}}, true
	}
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: "/proj"})
	run := func(tool *engineTestTool) {
		e.Tools.Add(tool)
		e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "c-" + tool.name, Name: tools.ToWire(tool.name), Args: json.RawMessage(`{}`)}, &fakePluginSnapshot{})
	}
	run(&engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
	run(&engineTestTool{name: "verification.run", risk: tools.RiskYellow, output: testRunOK})
	value = "ws2" // the shell edited a file after the passing run
	e.finishTurn(ctx, th, turn, nil, func() {})
	d := openWorkDetail(t, st, th.ID)
	if d.Work.Status != protocol.WorkOpen {
		t.Fatalf("status = %s, want open", d.Work.Status)
	}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeCriterion && n.Status != protocol.StatusStale {
			t.Fatalf("criterion status = %s, want stale", n.Status)
		}
	}
}

func TestGetWorkReportsCorruptOrDeletedObjectsUnavailable(t *testing.T) {
	e, th, turn, st := workEngine(t)
	e.Work.Vault = &vault.Vault{Dir: t.TempDir()}
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.run", risk: tools.RiskYellow,
		output: `{"results":[{"command":"a","status":"passed","exit_code":0,"output":"output A"},{"command":"b","status":"passed","exit_code":0,"output":"output B"}]}`})
	d := openWorkDetail(t, st, th.ID)
	var hashes []string
	for _, ev := range d.Evidence {
		if ev.VaultHash != "" {
			hashes = append(hashes, ev.VaultHash)
		}
	}
	if len(hashes) != 2 {
		t.Fatalf("hashes = %v", hashes)
	}
	os.Remove(filepath.Join(e.Work.Vault.Dir, "objects", hashes[0][:2], hashes[0]))
	corrupt := filepath.Join(e.Work.Vault.Dir, "objects", hashes[1][:2], hashes[1])
	os.WriteFile(corrupt, []byte("tampered"), 0o600)
	got, err := e.GetWork(t.Context(), d.Work.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range got.Evidence {
		if ev.VaultHash != "" && ev.Availability != protocol.AvailUnavailable {
			t.Fatalf("evidence %s availability = %s, want unavailable", ev.VaultHash[:8], ev.Availability)
		}
	}
	active, _ := e.GetWork(t.Context(), d.Work.ID, true)
	if len(active.Evidence) != 0 {
		t.Fatalf("activeOnly kept unavailable evidence: %+v", active.Evidence)
	}
}
