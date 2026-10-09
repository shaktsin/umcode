package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/fingerprint"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/vault"
	"github.com/shaktsin/umcode/internal/work"
)

type countedWorkUpdater struct {
	svc   *work.Service
	calls atomic.Int32
}

func TestWorkUpdateAcceptedRiskRequestsAtomicOutcome(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approve], func(t *testing.T) {
			e, th, turn, st := pluginHookEngine(t)
			e.Cfg.Models.DesignedWorkflow = true
			w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, WorkflowDepth: "designed"})
			if err != nil {
				t.Fatal(err)
			}
			n, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "unknown", Status: "open", Title: "risk", Content: json.RawMessage(`{"blocking":true}`)})
			if err != nil {
				t.Fatal(err)
			}
			u := &countedWorkUpdater{svc: &work.Service{Store: st, DesignedWorkflow: true}}
			e.Tools.Add(tools.NewWorkUpdate(u))
			args, _ := json.Marshal(protocol.WorkUpdateRequest{WorkID: w.ID, ExpectedRevision: 1, Nodes: []protocol.WorkNodeChange{{ID: n.ID, ExpectedRevision: 1, FromStatus: "open", ToStatus: "accepted_risk"}}})
			ctx := tools.WithScope(t.Context(), &tools.Scope{ThreadID: th.ID})
			done := make(chan toolRunResult, 1)
			go func() {
				done <- e.runTool(ctx, t.Context(), th, turn, llm.ToolCall{ID: "risk", Name: tools.ToWire("work.update"), Args: args}, &fakePluginSnapshot{})
			}()
			a := waitWorkflowApproval(t, e, st, map[string]bool{})
			d, _ := st.GetWorkDetail(t.Context(), w.ID)
			if d.Work.Revision != 2 || d.Nodes[0].Status != "open" || a.NodeRevision != 1 {
				t.Fatalf("premature acceptance: %+v %+v", d, a)
			}
			if _, err := e.RespondApproval(t.Context(), a.ID, approve, true, "user"); err != nil {
				t.Fatal(err)
			}
			out := <-done
			var result protocol.WorkUpdateResult
			if err := json.Unmarshal([]byte(out.Output), &result); err != nil {
				t.Fatal(err)
			}
			if u.calls.Load() != 1 || result.Revision != 3 || result.Transitioned != 0 || out.IsError == approve {
				t.Fatalf("result=%+v output=%+v", result, out)
			}
			d, _ = st.GetWorkDetail(t.Context(), w.ID)
			want := "open"
			if approve {
				want = "accepted_risk"
			}
			if d.Nodes[0].Status != want || d.Nodes[0].Revision != 2 {
				t.Fatalf("outcome=%+v", d)
			}
		})
	}
}

func TestWorkUpdateReplacementAfterRejectionRestoresMutation(t *testing.T) {
	e, th, turn, st, g := workflowEngine(t)
	a := protocol.Approval{ID: store.NewID("apr"), Kind: "workflow", ThreadID: th.ID, WorkID: g.WorkID, NodeID: g.NodeID, NodeRevision: 1, Status: "pending", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.CreateApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), a.ID, false, false, "user"); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetWorkDetail(t.Context(), g.WorkID)
	opt := d.Nodes[1]
	criterion, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: g.WorkID, Kind: "criterion", Status: "passed"})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(t.Context(), protocol.Evidence{WorkID: g.WorkID, Kind: "file_change"})
	if err != nil {
		t.Fatal(err)
	}
	u := &countedWorkUpdater{svc: &work.Service{Store: st, DesignedWorkflow: true}}
	e.Tools.Add(tools.NewWorkUpdate(u))
	req := protocol.WorkUpdateRequest{WorkID: g.WorkID, ExpectedRevision: 2, Nodes: []protocol.WorkNodeChange{
		{Ref: "replacement", Kind: "decision", Title: "replacement solution", Content: json.RawMessage(`{"required":true,"gate_kind":"security"}`), EvidenceIDs: []string{ev.ID}},
		{Ref: "task", Kind: "task", Title: "implement", Content: json.RawMessage(`{"required":true}`)},
	}, Edges: []protocol.WorkEdgeChange{{From: "replacement", Relation: "selects", To: opt.ID}, {From: criterion.ID, Relation: "verifies", To: "replacement"}, {From: criterion.ID, Relation: "verifies", To: "task"}, {From: "task", Relation: "implements", To: "replacement"}}}
	args, _ := json.Marshal(req)
	ctx := tools.WithScope(t.Context(), &tools.Scope{ThreadID: th.ID})
	done := make(chan toolRunResult, 1)
	go func() {
		done <- e.runTool(ctx, t.Context(), th, turn, llm.ToolCall{ID: "replace", Name: tools.ToWire("work.update"), Args: args}, &fakePluginSnapshot{})
	}()
	fresh := waitWorkflowApproval(t, e, st, map[string]bool{})
	if fresh.NodeID == g.NodeID {
		t.Fatal("rejected decision re-prompted")
	}
	if _, err := e.RespondApproval(t.Context(), fresh.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	if out := <-done; out.IsError {
		t.Fatal(out.Output)
	}
	tool := &engineTestTool{name: "file.write", risk: tools.RiskGreen, output: "ran"}
	out := runWorkTool(t, e, th, turn, tool)
	if out.IsError || tool.calls.Load() != 1 || u.calls.Load() != 1 {
		t.Fatalf("replacement failed to open mutation: %+v", out)
	}
	d, _ = st.GetWorkDetail(t.Context(), g.WorkID)
	if d.Work.Revision != 4 {
		t.Fatalf("revision=%d", d.Work.Revision)
	}
	for _, n := range d.Nodes {
		if n.Kind == "task" && n.Status != "ready" {
			t.Fatalf("task=%+v", n)
		}
	}
}

func (u *countedWorkUpdater) Update(ctx context.Context, thread string, req protocol.WorkUpdateRequest) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error) {
	u.calls.Add(1)
	return u.svc.Update(ctx, thread, req)
}

// Catches work.update using Call (discarding gates), applying twice, stopping
// after the first denial, leaking approval prose, and returning an old revision.
func TestWorkUpdateAppliesOnceAndRequestsEveryGate(t *testing.T) {
	for _, mode := range []string{"approved", "first denied", "expired"} {
		t.Run(mode, func(t *testing.T) {
			e, th, turn, st := pluginHookEngine(t)
			e.Cfg.Models.DesignedWorkflow = true
			w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, WorkflowDepth: "designed"})
			if err != nil {
				t.Fatal(err)
			}
			opt, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "option", Status: "active", Content: json.RawMessage(`{"solution_rung":1}`)})
			if err != nil {
				t.Fatal(err)
			}
			criterion, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "criterion", Status: "passed"})
			if err != nil {
				t.Fatal(err)
			}
			ev, err := st.AddEvidence(t.Context(), protocol.Evidence{WorkID: w.ID, Kind: "file_change", Summary: "inspection evidence"})
			if err != nil {
				t.Fatal(err)
			}
			u := &countedWorkUpdater{svc: &work.Service{Store: st, DesignedWorkflow: true}}
			e.Tools.Add(tools.NewWorkUpdate(u))
			req := protocol.WorkUpdateRequest{WorkID: w.ID, ExpectedRevision: 1, Rationale: "private rationale", Nodes: []protocol.WorkNodeChange{
				{Ref: "first", Kind: "decision", Title: "private first gate", Content: json.RawMessage(`{"required":true,"gate_kind":"security"}`)},
				{Ref: "second", Kind: "decision", Title: "private second gate", Content: json.RawMessage(`{"required":true,"gate_kind":"billing"}`)},
			}}
			for i := range req.Nodes {
				req.Nodes[i].EvidenceIDs = []string{ev.ID}
			}
			req.Edges = []protocol.WorkEdgeChange{{From: "first", Relation: "selects", To: opt.ID}, {From: "second", Relation: "selects", To: opt.ID}, {From: criterion.ID, Relation: "verifies", To: "first"}, {From: criterion.ID, Relation: "verifies", To: "second"}}
			args, _ := json.Marshal(req)
			ctx := tools.WithScope(t.Context(), &tools.Scope{ThreadID: th.ID})
			if mode == "expired" {
				e.Cfg.Policy.ApprovalTimeoutMinutes = 0
			}
			done := make(chan toolRunResult, 1)
			go func() {
				done <- e.runTool(ctx, t.Context(), th, turn, llm.ToolCall{ID: "update", Name: tools.ToWire("work.update"), Args: args}, &fakePluginSnapshot{})
			}()
			seen := map[string]bool{}
			if mode != "expired" {
				for i := 0; i < 2; i++ {
					a := waitWorkflowApproval(t, e, st, seen)
					seen[a.ID] = true
					approve := mode != "first denied" || i != 0
					if _, err := e.RespondApproval(t.Context(), a.ID, approve, true, "user"); err != nil {
						t.Fatal(err)
					}
				}
			}
			out := <-done
			if u.calls.Load() != 1 || strings.Contains(out.Output, "private") {
				t.Fatalf("calls=%d result=%+v", u.calls.Load(), out)
			}
			var result struct {
				Revision, Created, Transitioned, Linked int
				Gates                                   []struct {
					Status string `json:"status"`
					NodeID string `json:"node_id"`
				}
			}
			if err := json.Unmarshal([]byte(out.Output), &result); err != nil {
				t.Fatalf("output=%s error=%v", out.Output, err)
			}
			wantRevision := 4
			if mode == "expired" {
				wantRevision = 2
			}
			if result.Revision != wantRevision || result.Created != 2 || result.Transitioned != 0 || result.Linked != 6 {
				t.Fatalf("result=%+v", result)
			}
			if mode == "approved" {
				if out.IsError || len(result.Gates) != 0 {
					t.Fatalf("approved=%+v", out)
				}
			} else if !out.IsError || len(result.Gates) == 0 {
				t.Fatalf("denied/expired=%+v", out)
			}
			all, _ := st.ListApprovals(t.Context(), "")
			if len(all) != 2 {
				t.Fatalf("gates requested=%d", len(all))
			}
			d, _ := st.GetWorkDetail(t.Context(), w.ID)
			if d.Work.Revision != wantRevision || len(d.Nodes) != 4 {
				t.Fatalf("graph=%+v", d)
			}
		})
	}
}

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
