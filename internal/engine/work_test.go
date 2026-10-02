package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
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
