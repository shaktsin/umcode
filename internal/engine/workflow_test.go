package engine

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workflowEngine(t *testing.T) (*Engine, protocol.Thread, protocol.Turn, *store.Store, protocol.WorkflowGate) {
	t.Helper()
	e, th, turn, st := pluginHookEngine(t)
	e.Cfg.Models.DesignedWorkflow = true
	w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, WorkflowDepth: "designed"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "decision", Status: "proposed", Title: "private gate rationale", Content: json.RawMessage(`{"required":true,"gate_kind":"security"}`)})
	if err != nil {
		t.Fatal(err)
	}
	opt, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "option", Status: "active", Content: json.RawMessage(`{"solution_rung":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddWorkEdge(t.Context(), protocol.WorkEdge{WorkID: w.ID, FromNodeID: n.ID, Relation: "selects", ToNodeID: opt.ID}); err != nil {
		t.Fatal(err)
	}
	return e, th, turn, st, protocol.WorkflowGate{WorkID: w.ID, NodeID: n.ID, NodeRevision: 1, Kind: "security", Reason: "security", Summary: n.Title}
}

func TestWorkflowGateRejectedDecisionCannotMutate(t *testing.T) {
	e, th, turn, st, g := workflowEngine(t)
	a := protocol.Approval{ID: store.NewID("apr"), Kind: "workflow", ThreadID: th.ID, WorkID: g.WorkID, NodeID: g.NodeID, NodeRevision: 1, Status: "pending", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.CreateApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), a.ID, false, false, "user"); err != nil {
		t.Fatal(err)
	}
	tool := &engineTestTool{name: "file.write", risk: tools.RiskGreen, output: "ran"}
	out := runWorkTool(t, e, th, turn, tool)
	if !out.IsError || out.Output != "workflow not ready" || tool.calls.Load() != 0 {
		t.Fatalf("rejected gate allowed mutation: %+v", out)
	}
	all, _ := st.ListApprovals(t.Context(), "")
	if len(all) != 1 {
		t.Fatalf("rejected gate re-prompted: %d", len(all))
	}
}

func TestWorkflowGateApprovedSolutionRequiresRunnableTask(t *testing.T) {
	e, th, _, st, g := workflowEngine(t)
	if err := st.UpdateWorkNode(t.Context(), g.NodeID, "approved", 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	task, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: g.WorkID, Kind: "task", Status: "pending", Content: json.RawMessage(`{"required":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.checkWorkflowGate(t.Context(), th.ID, "file.write"); err == nil || err.Error() != "workflow not ready" {
		t.Fatalf("pending task opened gate: %v", err)
	}
	for _, status := range []string{"ready", "in_progress"} {
		if err := st.UpdateWorkNode(t.Context(), task.ID, status, 2, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := e.checkWorkflowGate(t.Context(), th.ID, "file.write"); err != nil {
			t.Fatalf("runnable task blocked: %v", err)
		}
	}
}

func waitWorkflowApproval(t *testing.T, e *Engine, st *store.Store, seen map[string]bool) protocol.Approval {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("workflow approval not requested")
		default:
		}
		list, err := st.ListApprovals(t.Context(), "pending")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			e.mu.Lock()
			live := e.approvals[a.ID] != nil
			e.mu.Unlock()
			if a.Kind == "workflow" && live && !seen[a.ID] {
				return a
			}
		}
		time.Sleep(time.Millisecond)
	}
}

// Catches remembered decisions bypassing human workflow review, waking before
// atomic persistence, and timeout changing a proposed node.
func TestWorkflowApproval(t *testing.T) {
	for _, mode := range []string{"approve", "deny", "timeout", "interruption"} {
		t.Run(mode, func(t *testing.T) {
			e, th, turn, st, g := workflowEngine(t)
			if err := st.RememberThreadDecision(t.Context(), th.ID, "work.update", "work.update", "deny"); err != nil {
				t.Fatal(err)
			}
			it, err := e.newItem(t.Context(), turn, protocol.ItemToolCall)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "timeout" {
				e.Cfg.Policy.ApprovalTimeoutMinutes = 0
			}
			done := make(chan struct {
				ok  bool
				err error
			}, 1)
			go func() {
				ok, err := e.requestWorkflowApproval(ctx, context.Background(), turn, it, g)
				done <- struct {
					ok  bool
					err error
				}{ok, err}
			}()
			var a protocol.Approval
			if mode != "timeout" {
				a = waitWorkflowApproval(t, e, st, map[string]bool{})
				if a.WorkID != g.WorkID || a.NodeID != g.NodeID || a.NodeRevision != 1 {
					t.Fatalf("identity=%+v", a)
				}
				if mode == "interruption" {
					cancel()
				} else {
					if _, err := e.RespondApproval(t.Context(), a.ID, mode == "approve", true, "user"); err != nil {
						t.Fatal(err)
					}
				}
			}
			out := <-done
			want := "rejected"
			wantRev := 2
			if mode == "approve" {
				want = "approved"
				if !out.ok || out.err != nil {
					t.Fatalf("result=%+v", out)
				}
			}
			if mode == "timeout" || mode == "interruption" {
				want = "proposed"
				wantRev = 1
				if out.err == nil {
					t.Fatal("missing timeout/interruption error")
				}
			}
			d, err := st.GetWorkDetail(t.Context(), g.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			if d.Nodes[0].Status != want || d.Work.Revision != wantRev {
				t.Fatalf("outcome=%+v", d)
			}
			var count int
			if err := st.DB.QueryRow(`SELECT COUNT(*) FROM thread_approvals WHERE tool='work.update'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("workflow remember wrote decision: %d", count)
			}
			if decision := st.RememberedThreadDecision(t.Context(), th.ID, "work.update", "work.update"); decision != "deny" {
				t.Fatalf("workflow Remember overwrote tool decision: %q", decision)
			}
			var audits int
			if err := st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE event_type='workflow.approval.request'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if audits != 1 {
				t.Fatalf("audits=%d", audits)
			}
			if mode == "deny" {
				if _, err := e.RespondApproval(t.Context(), a.ID, true, false, "again"); err == nil {
					t.Fatal("duplicate approval accepted")
				}
			}
		})
	}
}

func TestWorkflowApprovalRestartExpiresWithoutGraphChange(t *testing.T) {
	_, th, turn, st, g := workflowEngine(t)
	a := protocol.Approval{ID: store.NewID("apr"), Kind: "workflow", ThreadID: th.ID, TurnID: turn.ID, WorkID: g.WorkID, NodeID: g.NodeID, NodeRevision: 1, Status: "pending", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.CreateApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpirePendingApprovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetWorkDetail(t.Context(), g.WorkID)
	all, _ := st.ListApprovals(t.Context(), "")
	if d.Work.Revision != 1 || d.Nodes[0].Status != "proposed" || all[0].Status != "expired" {
		t.Fatalf("restart=%+v %+v", d, all)
	}
}

func TestWorkflowApprovalPersistedWinnerBeforeInterruption(t *testing.T) {
	e, _, turn, st, g := workflowEngine(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	item, err := e.newItem(t.Context(), turn, protocol.ItemToolCall)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { ok, _ := e.requestWorkflowApproval(ctx, context.Background(), turn, item, g); done <- ok }()
	a := waitWorkflowApproval(t, e, st, map[string]bool{})
	// The database commit wins before its transport signal arrives.
	if _, err := st.DecideWorkflowApproval(t.Context(), a.ID, "approved", "user"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if !<-done {
		t.Fatal("committed approval was reported as interrupted")
	}
}

func TestWorkflowGateAllowlistAndDepth(t *testing.T) {
	e, th, _, st, g := workflowEngine(t)
	allowed := []string{"file.read", "file.list", "file.search", "web.search", "web.fetch", "verification.plan", "computer.list", "computer.inspect", "visual.inspect", "work.update"}
	blocked := []string{"file.write", "file.edit", "shell.run", "exec.start", "verification.run", "browser.navigate", "visual.capture", "computer.click", "task.create", "plugin.read", "mcp.read", "work.inspect"}
	for _, name := range allowed {
		if !workflowDiscoveryTool(name) || e.checkWorkflowGate(t.Context(), th.ID, name) != nil {
			t.Errorf("discovery blocked: %s", name)
		}
	}
	for _, name := range blocked {
		if workflowDiscoveryTool(name) || e.checkWorkflowGate(t.Context(), th.ID, name) == nil {
			t.Errorf("mutation allowed: %s", name)
		}
	}
	for _, depth := range []string{"direct", "guided"} {
		if _, err := st.DB.Exec(`UPDATE works SET workflow_depth=? WHERE id=?`, depth, g.WorkID); err != nil {
			t.Fatal(err)
		}
		if err := e.checkWorkflowGate(t.Context(), th.ID, "file.write"); err != nil {
			t.Fatal(err)
		}
	}
}

// Catches hooks/guards/policy firing before blocked mutation and conservative
// plugin/MCP classification using real runTool paths.
func TestWorkflowGateBeforeHooksPolicyAndCall(t *testing.T) {
	for _, name := range []string{"file.write", "shell.run", "verification.run", "computer.click", "task.create", "plugin.read", "mcp.read"} {
		t.Run(name, func(t *testing.T) {
			e, th, turn, st, _ := workflowEngine(t)
			e.Cfg.Policy.ApprovalTimeoutMinutes = 0
			marker := filepath.Join(t.TempDir(), "hook")
			tool := &engineTestTool{name: name, risk: tools.RiskRed, forbidden: true, output: "ran"}
			snapshot := &fakePluginSnapshot{tool: tool, hookSet: captureHookSet(t, hooks.ToolUseFailed, marker)}
			out := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "blocked", Name: tools.ToWire(name), Args: json.RawMessage(`{}`)}, snapshot)
			if !out.IsError || !strings.Contains(out.Output, "workflow") || tool.calls.Load() != 0 || strings.Contains(out.Output, "guard") {
				t.Fatalf("result=%+v", out)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("hook ran: %v", err)
			}
			all, _ := st.ListApprovals(t.Context(), "")
			for _, a := range all {
				if a.Kind == "tool" {
					t.Fatal("policy approval requested before gate")
				}
			}
		})
	}
}

func TestWorkflowProspectiveEscalationBeforeFirstMutation(t *testing.T) {
	for _, depth := range []string{"direct", "guided"} {
		for _, path := range []string{"internal/protocol/types.go", "schema.json", "db/migrations/001.sql", "security/auth.go"} {
			t.Run(depth+"/"+path, func(t *testing.T) {
				e, th, turn, st := pluginHookEngine(t)
				e.Cfg.Models.DesignedWorkflow = true
				e.Cfg.Policy.ApprovalTimeoutMinutes = 0
				w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, WorkflowDepth: depth})
				if err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(t.TempDir(), "hook")
				tool := &engineTestTool{name: "file.write", risk: tools.RiskRed, output: "mutation"}
				snapshot := &fakePluginSnapshot{tool: tool, hookSet: captureHookSet(t, hooks.BeforeToolUse, marker)}
				args, _ := json.Marshal(map[string]string{"path": path, "content": "first write"})
				out := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "first", Name: "file__write", Args: args}, snapshot)
				d, err := st.GetWorkDetail(t.Context(), w.ID)
				if err != nil {
					t.Fatal(err)
				}
				if d.Work.WorkflowDepth != "designed" || d.Work.Revision != 2 || !out.IsError || out.Output != "workflow not ready" || tool.calls.Load() != 0 {
					t.Fatalf("first risky mutation escaped: depth=%s revision=%d out=%+v calls=%d", d.Work.WorkflowDepth, d.Work.Revision, out, tool.calls.Load())
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("hook ran before escalation: %v", err)
				}
				approvals, _ := st.ListApprovals(t.Context(), "")
				if len(approvals) != 0 {
					t.Fatal("ordinary approval ran before graph readiness")
				}
			})
		}
	}
}

func TestWorkflowGateReadFailureAndFeatureOff(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "fail closed", false: "off skips lookup"}[enabled], func(t *testing.T) {
			e, th, turn, st, _ := workflowEngine(t)
			e.Cfg.Models.DesignedWorkflow = enabled
			if _, err := st.DB.Exec(`DROP TABLE works`); err != nil {
				t.Fatal(err)
			}
			tool := &engineTestTool{name: "file.write", risk: tools.RiskGreen, output: "ran"}
			out := runWorkTool(t, e, th, turn, tool)
			if enabled {
				if !out.IsError || out.Output != "workflow gate unavailable" || tool.calls.Load() != 0 {
					t.Fatalf("failed open: %+v", out)
				}
			} else if out.IsError || tool.calls.Load() != 1 {
				t.Fatalf("off changed behavior: %+v", out)
			}
		})
	}
}

func TestWorkflowGateRestartRecovery(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approve], func(t *testing.T) {
			e, th, turn, st, _ := workflowEngine(t)
			tool := &engineTestTool{name: "file.write", risk: tools.RiskGreen, output: "ran"}
			e.Tools.Add(tool)
			done := make(chan toolRunResult, 1)
			go func() {
				done <- e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "recover", Name: tools.ToWire(tool.Name()), Args: json.RawMessage(`{}`)}, &fakePluginSnapshot{})
			}()
			a := waitWorkflowApproval(t, e, st, map[string]bool{})
			if _, err := e.RespondApproval(t.Context(), a.ID, approve, false, "user"); err != nil {
				t.Fatal(err)
			}
			out := <-done
			wantCalls := int32(0)
			if approve {
				wantCalls = 1
			}
			if tool.calls.Load() != wantCalls || out.IsError == approve {
				t.Fatalf("recovery=%+v calls=%d", out, tool.calls.Load())
			}
			all, _ := st.ListApprovals(t.Context(), "")
			if len(all) != 1 {
				t.Fatalf("repeated prompt: %d", len(all))
			}
		})
	}
}

func TestWorkflowGateRestartOrphanAndLiveApproval(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "orphan"}[live], func(t *testing.T) {
			e, th, turn, st, g := workflowEngine(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var old protocol.Approval
			var waiter chan bool
			if live {
				item, err := e.newItem(t.Context(), turn, protocol.ItemToolCall)
				if err != nil {
					t.Fatal(err)
				}
				waiter = make(chan bool, 1)
				go func() { ok, _ := e.requestWorkflowApproval(ctx, context.Background(), turn, item, g); waiter <- ok }()
				old = waitWorkflowApproval(t, e, st, map[string]bool{})
			} else {
				old = protocol.Approval{ID: store.NewID("apr"), Kind: "workflow", ThreadID: th.ID, WorkID: g.WorkID, NodeID: g.NodeID, NodeRevision: 1, Status: "pending", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
				if err := st.CreateApproval(t.Context(), old); err != nil {
					t.Fatal(err)
				}
			}
			tool := &engineTestTool{name: "file.write", risk: tools.RiskGreen, output: "ran"}
			e.Tools.Add(tool)
			done := make(chan toolRunResult, 1)
			go func() {
				done <- e.runTool(ctx, t.Context(), th, turn, llm.ToolCall{ID: "recover", Name: tools.ToWire("file.write"), Args: json.RawMessage(`{}`)}, &fakePluginSnapshot{})
			}()
			if live {
				out := <-done
				if !out.IsError || tool.calls.Load() != 0 || !strings.Contains(out.Output, "pending") || !strings.Contains(out.Output, g.NodeID) {
					t.Fatalf("live gate bypassed or missing compact identity: %+v", out)
				}
				if _, err := e.RespondApproval(t.Context(), old.ID, true, false, "user"); err != nil {
					t.Fatal(err)
				}
				if !<-waiter {
					t.Fatal("approval waiter failed")
				}
				all, _ := st.ListApprovals(t.Context(), "")
				if len(all) != 1 {
					t.Fatal("live gate re-prompted")
				}
			} else {
				a := waitWorkflowApproval(t, e, st, map[string]bool{old.ID: true})
				if _, err := e.RespondApproval(t.Context(), a.ID, true, false, "user"); err != nil {
					t.Fatal(err)
				}
				out := <-done
				prior, _ := st.GetApproval(t.Context(), old.ID)
				if out.IsError || tool.calls.Load() != 1 || prior.Status != "expired" {
					t.Fatalf("orphan recovery: %+v prior=%+v", out, prior)
				}
			}
		})
	}
}

func TestWorkflowGateApprovalThenOrdinaryPolicyStillRuns(t *testing.T) {
	e, th, turn, st, _ := workflowEngine(t)
	th.ApprovalMode = "normal"
	tool := &engineTestTool{name: "shell.run", risk: tools.RiskRed, output: "ran"}
	e.Tools.Add(tool)
	args := json.RawMessage(`{"command":"deploy"}`)
	if err := st.RememberThreadDecision(t.Context(), th.ID, "shell.run", "deploy", "deny"); err != nil {
		t.Fatal(err)
	}
	done := make(chan toolRunResult, 1)
	go func() {
		done <- e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "policy", Name: tools.ToWire("shell.run"), Args: args}, &fakePluginSnapshot{})
	}()
	a := waitWorkflowApproval(t, e, st, map[string]bool{})
	if _, err := e.RespondApproval(t.Context(), a.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if !out.IsError || !strings.Contains(out.Output, "denied") || tool.calls.Load() != 0 {
		t.Fatalf("workflow bypassed policy: %+v", out)
	}
}
