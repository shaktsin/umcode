package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/work"
	"testing"
	"time"
)

func TestOptimizationTurnSnapshot(t *testing.T) {
	e, th, _, st := pluginHookEngine(t)
	e.Work = &work.Service{Store: st, Log: e.Log}
	e.Tools.Add(tools.NewWorkUpdate(e.Work))
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	p, err := e.resolveOptimizationPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a := optimization.WithPolicy(t.Context(), p)
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	q, err := e.resolveOptimizationPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b := optimization.WithPolicy(t.Context(), q)
	for _, tc := range []struct {
		ctx  context.Context
		want int
	}{{a, 1}, {b, 0}} {
		actual, _, err := e.permittedTurnCatalog(tc.ctx, nil, nil)
		if err != nil || len(actual) != tc.want {
			t.Fatalf("turn catalog=%d want=%d err=%v", len(actual), tc.want, err)
		}
	}
	if err := e.Work.Begin(a, th, "design a new architecture"); err != nil {
		t.Fatal(err)
	}
	w, ok, err := st.OpenWorkForThread(t.Context(), th.ID)
	if err != nil || !ok || w.WorkflowDepth != "designed" {
		t.Fatalf("Work missed turn policy: %+v %v", w, err)
	}
	if !e.optimizationPolicy(context.WithoutCancel(a)).AutoPromote || e.optimizationPolicy(b).AutoPromote {
		t.Fatal("policy lost across persistence boundary")
	}
}

func TestAutomaticWorkflowNoApprovalWait(t *testing.T) {
	e, th, turn, st, g := workflowEngine(t)
	e.Work = &work.Service{Store: st, Log: e.Log, DesignedWorkflow: true}
	ctx := optimization.WithPolicy(t.Context(), optimization.All(true))
	w, _, _ := st.OpenWorkForThread(ctx, th.ID)
	criterion, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: "criterion", Status: "pending", Content: json.RawMessage(`{"command":"go test ./..."}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddWorkEdge(ctx, protocol.WorkEdge{WorkID: w.ID, FromNodeID: criterion.ID, Relation: "verifies", ToNodeID: g.NodeID}); err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, Kind: protocol.EvidenceDiscovery, Summary: "inspected"})
	if err != nil {
		t.Fatal(err)
	}

	_, gates, err := e.Work.Update(ctx, th.ID, protocol.WorkUpdateRequest{WorkID: w.ID, ExpectedRevision: w.Revision, Nodes: []protocol.WorkNodeChange{{ID: g.NodeID, ExpectedRevision: 1, FromStatus: "proposed", ToStatus: "approved", EvidenceIDs: []string{ev.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 0 {
		t.Fatalf("automatic decision emitted human gates: %+v", gates)
	}
	d, err := st.GetWorkDetail(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range d.Nodes {
		if n.ID == g.NodeID && n.DecisionActor != "agent" {
			t.Fatalf("false provenance %+v", n)
		}
	}
	out, denied, err := e.finishWorkflowUpdate(ctx, ctx, turn, protocol.Item{}, protocol.WorkUpdateResult{}, gates)
	if err != nil || denied || out == "" {
		t.Fatalf("finish %s %v %v", out, denied, err)
	}
	approvals, _ := st.ListApprovals(ctx, "")
	if len(approvals) != 0 {
		t.Fatal("fabricated approval")
	}
}
func TestAutomaticWorkflowPendingGateRecovery(t *testing.T) {
	e, th, turn, st, g := workflowEngine(t)
	ctx := optimization.WithPolicy(t.Context(), optimization.All(true))
	a := protocol.Approval{ID: store.NewID("apr"), Kind: "workflow", ThreadID: th.ID, WorkID: g.WorkID, NodeID: g.NodeID, NodeRevision: 1, Status: "pending", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.CreateApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetWorkDetail(ctx, g.WorkID)
	if err := e.recoverWorkflowGate(ctx, ctx, turn, protocol.Item{}, "file.write", &d); err != errWorkflowNotReady {
		t.Fatalf("recovery=%v", err)
	}
	rows, _ := st.ListApprovals(ctx, "")
	if len(rows) != 1 || rows[0].Status == "pending" || rows[0].Status == "approved" {
		t.Fatalf("obsolete waiter %+v", rows)
	}
	d, _ = st.GetWorkDetail(ctx, g.WorkID)
	for _, n := range d.Nodes {
		if n.ID == g.NodeID && n.Status != "proposed" {
			t.Fatal("pending human decision silently granted")
		}
	}
}
func TestAutomaticWorkflowActionPermission(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	ctx := optimization.WithPolicy(t.Context(), optimization.All(true))
	th.ApprovalMode = "auto_read_only"
	tool := &engineTestTool{name: "file.write", risk: tools.RiskRed, output: "ran"}
	e.Tools.Add(tool)
	// A cancelled action approval must remain denied, even with the bundle enabled.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	r := e.runTool(cancelled, ctx, th, turn, llm.ToolCall{ID: "protected", Name: tools.ToWire(tool.Name()), Args: json.RawMessage(`{}`)}, nil)
	if !r.IsError || tool.calls.Load() != 0 {
		t.Fatalf("protected action ran: %+v", r)
	}
	rows, _ := st.ListApprovals(ctx, "")
	for _, a := range rows {
		if a.Kind == "workflow" {
			t.Fatal("duplicate workflow approval")
		}
	}
}

func TestOptimizationResultReductionSnapshot(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	output := reducerVerificationOutput(t)
	tool := &engineTestTool{name: "verification.run", risk: tools.RiskGreen, output: output}
	e.Tools.Add(tool)
	for _, on := range []bool{true, false} {
		ctx := optimization.WithPolicy(t.Context(), optimization.All(on))
		r := e.runTool(ctx, context.WithoutCancel(ctx), th, turn, llm.ToolCall{ID: store.NewID("call"), Name: tools.ToWire(tool.Name()), Args: json.RawMessage(`{}`)}, nil)
		if r.IsError || r.Output != output {
			t.Fatalf("canonical output changed %+v", r)
		}
		if (r.ModelOutput != output) != on {
			t.Fatalf("reduction did not follow turn policy: enabled=%v", on)
		}
	}
}
func TestOptimizationIsolatedPolicyDoesNotWriteGlobalSetting(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ctx := optimization.WithPolicy(t.Context(), optimization.Policy{ContextCompiler: true})
	p := e.optimizationPolicy(ctx)
	if !p.ContextCompiler || p.ProgressiveTools || p.AutoPromote {
		t.Fatalf("isolated ablation lost %+v", p)
	}
	r, err := e.TokenOptimization(t.Context())
	if err != nil || !r.Enabled {
		t.Fatalf("isolated policy changed global %+v %v", r, err)
	}
}
