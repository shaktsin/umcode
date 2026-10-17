package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

type childWorkspaceFixture struct{ root string }

func (w childWorkspaceFixture) Resolve(context.Context, protocol.AgentTask, protocol.AgentChild) (ChildWorkspace, error) {
	return ChildWorkspace{ID: "isolated-fixture", Root: w.root}, nil
}

type finalizingChildWorkspace struct {
	childWorkspaceFixture
	engine    *Engine
	fail      bool
	finalized chan bool
}

func (w finalizingChildWorkspace) Finalize(ctx context.Context, _ protocol.AgentTask, _ protocol.AgentChild, workspace ChildWorkspace) (ChildWorkspace, error) {
	w.engine.mu.Lock()
	stopped := len(w.engine.activeTurns) == 0
	w.engine.mu.Unlock()
	w.finalized <- stopped && ctx.Err() == nil
	if w.fail {
		return workspace, errors.New("final revision unavailable; workspace retained")
	}
	workspace.Revision = "final-child-revision"
	return workspace, nil
}

// Dropping finalization or publishing its initial revision as reviewed evidence
// must fail; tool changes stay isolated from ordinary project undo/history.
func TestChildExecutionFinalizationAndHistory(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			e, th, parent, _ := delegationFixture(t)
			task := childExecutionTask(t, e, th)
			root := t.TempDir()
			os.WriteFile(filepath.Join(parent, "target"), []byte("parent"), 0600)
			finalized := make(chan bool, 1)
			e.DelegationWorkspaces = finalizingChildWorkspace{childWorkspaceFixture{root}, e, fail, finalized}
			p := &budgetProvider{eventsFor: func(n int) []llm.Event {
				if n == 1 {
					return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "edit", Name: tools.ToWire("file.write"), Args: json.RawMessage(`{"path":"target","content":"child"}`)}}, {Type: llm.EventDone}}
				}
				return []llm.Event{{Type: llm.EventTextDelta, Text: "isolated change"}, {Type: llm.EventDone}}
			}}
			e.LLMs.Register(p)
			child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "write target"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			got, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case okay := <-finalized:
				if !okay {
					t.Error("finalized before actual stop or with cancelled context")
				}
			default:
				t.Error("workspace never finalized")
			}
			if fail {
				if got[0].State != protocol.AgentStateFailed || got[0].Result.Blocker == "" || got[0].Revision != "" {
					t.Errorf("failed finalization outcome %+v", got)
				}
			} else if got[0].Revision != "final-child-revision" || got[0].State != protocol.AgentStateCompleted {
				t.Errorf("missing final revision %+v", got)
			}
			if len(got[0].Result.ChangedPaths) != 1 || got[0].Result.ChangedPaths[0] != "target" {
				t.Errorf("changed paths missing %+v", got[0].Result)
			}
			data, _ := os.ReadFile(filepath.Join(parent, "target"))
			if string(data) != "parent" {
				t.Fatal("parent changed")
			}
			changes, err := e.Store.ListFileChanges(t.Context(), th.ProjectID, "", "", 100)
			if err != nil || len(changes) != 0 {
				t.Errorf("ordinary changes leaked %+v %v", changes, err)
			}
			execution, _ := e.Store.AgentExecution(t.Context(), task.ID, child.ID)
			turns, _ := e.Store.ListTurns(t.Context(), execution.ThreadID)
			if len(turns) != 1 {
				t.Fatal("child turn missing")
			}
			if _, err = e.Projects.RevertTurn(t.Context(), turns[0].ID, nil); err == nil {
				t.Error("ordinary undo accepted hidden child")
			}
			data, _ = os.ReadFile(filepath.Join(parent, "target"))
			if string(data) != "parent" {
				t.Fatal("child undo changed parent")
			}
		})
	}
}

type childEvent struct {
	method string
	params any
}
type childSubscriber struct{ events chan childEvent }

func (*childSubscriber) ID() string                         { return "child-execution" }
func (*childSubscriber) IsAdmin() bool                      { return true }
func (*childSubscriber) Wants(string) bool                  { return true }
func (s *childSubscriber) Notify(method string, params any) { s.events <- childEvent{method, params} }

// The OS command boundary is replaced with a probe so this tests the actual
// engine approval/permission adapter without an unrestricted subprocess.
type childPermissionProbe struct {
	scopes   chan tools.Scope
	onAssess func()
}

func (*childPermissionProbe) Name() string            { return "shell.run" }
func (*childPermissionProbe) Description() string     { return "permission probe" }
func (*childPermissionProbe) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (p *childPermissionProbe) Assess(json.RawMessage) (tools.Risk, string) {
	if p.onAssess != nil {
		p.onAssess()
	}
	return tools.RiskRed, "probe"
}
func (p *childPermissionProbe) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	p.scopes <- *tools.ScopeFrom(ctx)
	return "exit_code: 0", nil
}

// Dropping the post-approval live policy check exposes network/compute or
// permits action after the parent chat has left its admitted project.
func TestChildExecutionLivePolicyNarrowing(t *testing.T) {
	for _, tt := range []struct {
		name                      string
		initial, current, foreign bool
	}{{"narrow", true, false, false}, {"cannot broaden", false, true, false}, {"foreign owner", true, true, true}} {
		t.Run(tt.name, func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			task := childExecutionTask(t, e, th)
			initial := tt.initial
			if _, err := e.Projects.Update(t.Context(), protocol.ProjectUpdateParams{ProjectID: th.ProjectID, Tools: &protocol.ProjectTools{Network: &initial, Compute: &initial}}); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			e.DelegationWorkspaces = childWorkspaceFixture{root}
			probe := &childPermissionProbe{scopes: make(chan tools.Scope, 1)}
			e.Tools.Add(probe)
			sub := &childSubscriber{make(chan childEvent, 256)}
			e.Bus.Add(sub)
			p := &budgetProvider{eventsFor: func(n int) []llm.Event {
				if n == 1 {
					return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "probe", Name: tools.ToWire("shell.run"), Args: json.RawMessage(`{"command":"probe"}`)}}, {Type: llm.EventDone}}
				}
				return []llm.Event{{Type: llm.EventDone}}
			}}
			e.LLMs.Register(p)
			child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "check"})
			if err != nil {
				t.Fatal(err)
			}
			var approval protocol.Approval
			for approval.ID == "" {
				select {
				case ev := <-sub.events:
					if ev.method == protocol.NotifyApprovalRequest {
						approval = ev.params.(protocol.ApprovalEvent).Approval
					}
				case <-time.After(5 * time.Second):
					t.Fatal("approval not requested")
				}
			}
			current := tt.current
			if _, err = e.Projects.Update(t.Context(), protocol.ProjectUpdateParams{ProjectID: th.ProjectID, Tools: &protocol.ProjectTools{Network: &current, Compute: &current}}); err != nil {
				t.Fatal(err)
			}
			if tt.foreign {
				if _, err = e.Store.DB.ExecContext(t.Context(), `UPDATE threads SET project_id='' WHERE id=?`, th.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = e.RespondApproval(t.Context(), approval.ID, true, false, "test"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err = e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID}); err != nil {
				t.Fatal(err)
			}
			select {
			case scope := <-probe.scopes:
				if tt.foreign {
					t.Fatal("action executed after parent ownership changed")
				}
				if scope.AllowNet || scope.UseCompute || !scope.RestrictedChild || !pathutil.SameFolder(scope.Root, root) {
					t.Fatalf("live policy was broadened or isolated root replaced %+v", scope)
				}
			default:
				if !tt.foreign {
					t.Fatal("permitted scoped action did not run")
				}
			}
		})
	}
}

func TestChildExecutionApprovalModeNarrowing(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	if err := e.Store.UpdateThread(t.Context(), th.ID, map[string]any{"approval_mode": "auto_all"}); err != nil {
		t.Fatal(err)
	}
	e.DelegationWorkspaces = childWorkspaceFixture{t.TempDir()}
	probe := &childPermissionProbe{scopes: make(chan tools.Scope, 1), onAssess: func() {
		if err := e.Store.UpdateThread(t.Context(), th.ID, map[string]any{"approval_mode": "normal"}); err != nil {
			t.Error(err)
		}
	}}
	e.Tools.Add(probe)
	p := &budgetProvider{eventsFor: func(n int) []llm.Event {
		if n == 1 {
			return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "probe", Name: tools.ToWire("shell.run"), Args: json.RawMessage(`{"command":"probe"}`)}}, {Type: llm.EventDone}}
		}
		return []llm.Event{{Type: llm.EventDone}}
	}}
	e.LLMs.Register(p)
	child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "check"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err = e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.scopes:
		t.Fatal("action auto-approved after parent revoked automatic approval")
	default:
	}
}

// Removing parent-visible approval identity/state or actual-turn cancellation
// must fail this test; denied/cancelled actions must never write the sentinel.
func TestChildExecutionApproval(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			task := childExecutionTask(t, e, th)
			root := t.TempDir()
			finalized := make(chan bool, 1)
			e.DelegationWorkspaces = finalizingChildWorkspace{childWorkspaceFixture{root}, e, cancelled, finalized}
			sub := &childSubscriber{make(chan childEvent, 256)}
			e.Bus.Add(sub)
			p := &budgetProvider{eventsFor: func(n int) []llm.Event {
				if n == 1 {
					return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "write", Name: tools.ToWire("shell.run"), Args: json.RawMessage(`{"command":"echo changed > sentinel"}`)}}, {Type: llm.EventDone}}
				}
				return []llm.Event{{Type: llm.EventTextDelta, Text: "action blocked"}, {Type: llm.EventDone}}
			}}
			e.LLMs.Register(p)
			child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "start", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "attempt the action"})
			if err != nil {
				t.Fatal(err)
			}
			var approval protocol.Approval
			for approval.ID == "" {
				select {
				case ev := <-sub.events:
					if ev.method == protocol.NotifyApprovalRequest {
						approval = ev.params.(protocol.ApprovalEvent).Approval
					}
					if ev.method == protocol.NotifyItemDelta || ev.method == protocol.NotifyItemCompleted || ev.method == protocol.NotifyTurnStarted {
						t.Fatalf("ordinary child event leaked: %s", ev.method)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("approval not requested")
				}
			}
			encoded, _ := json.Marshal(approval)
			var attrs map[string]any
			json.Unmarshal(encoded, &attrs)
			if approval.ThreadID != th.ID || attrs["agentTaskId"] != task.ID || attrs["agentChildId"] != child.ID {
				t.Errorf("approval attribution %s", encoded)
			}
			current, err := e.Store.AgentChild(t.Context(), task.ID, child.ID)
			if err != nil || current.State != protocol.AgentStateWaitingApproval {
				t.Errorf("approval state %+v %v", current, err)
			}
			persisted, err := e.Store.GetApproval(t.Context(), approval.ID)
			persistedJSON, _ := json.Marshal(persisted)
			if err != nil || !strings.Contains(string(persistedJSON), child.ID) {
				t.Errorf("persisted attribution %s %v", persistedJSON, err)
			}
			forged := approval
			forged.ID = "foreign-approval"
			forged.AgentChildID = "foreign-child"
			if err := e.Store.CreateApproval(t.Context(), forged); err == nil {
				t.Error("foreign child approval ownership accepted")
			}
			if cancelled {
				err = e.DelegationCoordinator.Cancel(t.Context(), task.ID, child.ID)
			} else {
				_, err = e.RespondApproval(t.Context(), approval.ID, false, false, "test")
			}
			if err != nil {
				t.Fatal(err)
			}
			var completed bool
			deadline := time.After(5 * time.Second)
			for !completed {
				select {
				case ev := <-sub.events:
					if ev.method == protocol.NotifyDelegationUpdated {
						d := ev.params.(protocol.DelegationUpdated)
						completed = d.Child.ID == child.ID && (d.Child.State == protocol.AgentStateCompleted || (cancelled && d.Child.State == protocol.AgentStateCancelled))
					}
					if ev.method == protocol.NotifyItemDelta || ev.method == protocol.NotifyItemCompleted || ev.method == protocol.NotifyTurnCompleted {
						t.Fatalf("ordinary child event leaked: %s", ev.method)
					}
				case <-deadline:
					t.Fatal("child not terminal")
				}
			}
			if cancelled {
				for {
					persisted, err = e.Store.GetApproval(t.Context(), approval.ID)
					if err != nil {
						t.Fatal(err)
					}
					if persisted.Status == "expired" {
						break
					}
					select {
					case <-deadline:
						t.Fatal("approval waiter survived cancellation")
					default:
						time.Sleep(time.Millisecond)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(root, "sentinel")); !os.IsNotExist(err) {
				t.Fatal("unapproved action wrote file")
			}
			select {
			case okay := <-finalized:
				if !okay {
					t.Error("finalizer ran before child stopped or inherited cancelled context")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("child workspace not finalized after approval")
			}
			current, err = e.Store.AgentChild(t.Context(), task.ID, child.ID)
			if err != nil || (cancelled && current.State != protocol.AgentStateCancelled) {
				t.Fatalf("finalizer resurrected cancelled child %+v %v", current, err)
			}
			if cancelled {
				deadline := time.Now().Add(2 * time.Second)
				for current.Result.Blocker == "" && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
					current, err = e.Store.AgentChild(t.Context(), task.ID, child.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if !strings.Contains(current.Result.Blocker, "final revision unavailable") {
					t.Errorf("cancelled finalization error lost %+v", current)
				}
			}
		})
	}
}

// A child must keep the root turn's approved pool despite later thread settings.
func TestChildExecutionApprovedModels(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			_, err := e.SetRoutingConfig(t.Context(), protocol.RoutingConfig{Models: []protocol.ConfiguredModel{{ID: "approved", Provider: "claude", Model: "deterministic", Enabled: true}, {ID: "other", Provider: "claude", Model: "unapproved", Enabled: true}}, Pools: []protocol.ModelPool{{ID: "parent", Name: "Parent", Models: []string{"approved"}, Strategy: protocol.PoolPriority, Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			token, err := e.PrepareDelegation(t.Context(), th.ID)
			if err != nil {
				t.Fatal(err)
			}
			tr := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "activate", DelegationActivationToken: token.ActivationToken, Override: protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "parent"}})
			task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, tr.ID)
			if err != nil {
				t.Fatal(err)
			}
			e.DelegationWorkspaces = childWorkspaceFixture{t.TempDir()}
			e.Cfg.Models.Roles = map[string]config.AgentModelConfig{"worker": {Provider: "claude", Model: "deterministic"}}
			if outside {
				e.Cfg.Models.Roles["worker"] = config.AgentModelConfig{Provider: "claude", Model: "unapproved"}
			}
			p := &budgetProvider{events: []llm.Event{{Type: llm.EventTextDelta, Text: "result"}, {Type: llm.EventDone}}}
			e.LLMs.Register(p)
			child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			got, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID})
			if err != nil {
				t.Fatal(err)
			}
			if outside {
				if len(p.requests) != 0 || got[0].State != protocol.AgentStateFailed {
					t.Fatalf("outside pool routed: %+v %d", got, len(p.requests))
				}
				return
			}
			if got[0].State != protocol.AgentStateCompleted || len(p.requests) != 1 || p.requests[0].Model != "deterministic" {
				t.Fatalf("approved route %+v requests %+v", got, p.requests)
			}
		})
	}
}

// Changing defaults after root admission must never broaden a child's pool.
func TestChildExecutionModelScopeFrozen(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	routing := protocol.RoutingConfig{DefaultPool: "parent", Models: []protocol.ConfiguredModel{{ID: "approved", Provider: "claude", Model: "deterministic", Enabled: true}, {ID: "other", Provider: "claude", Model: "unapproved", Enabled: true}}, Pools: []protocol.ModelPool{{ID: "parent", Name: "Parent", Models: []string{"approved"}, Strategy: protocol.PoolPriority, Enabled: true}, {ID: "other", Name: "Other", Models: []string{"other"}, Strategy: protocol.PoolPriority, Enabled: true}}}
	if _, err := e.SetRoutingConfig(t.Context(), routing); err != nil {
		t.Fatal(err)
	}
	task := childExecutionTask(t, e, th)
	routing.DefaultPool = "other"
	if _, err := e.SetRoutingConfig(t.Context(), routing); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Models.Roles = map[string]config.AgentModelConfig{"worker": {Provider: "claude", Model: "unapproved"}}
	e.DelegationWorkspaces = childWorkspaceFixture{t.TempDir()}
	p := &budgetProvider{events: []llm.Event{{Type: llm.EventDone}}}
	e.LLMs.Register(p)
	child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 0 || got[0].State != protocol.AgentStateFailed {
		t.Fatalf("new default broadened original pool: %+v requests %d", got, len(p.requests))
	}
}

func childExecutionTask(t *testing.T, e *Engine, th protocol.Thread) protocol.AgentTask {
	t.Helper()
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "parent private unrelated transcript", DelegationActivationToken: token.ActivationToken})
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// Later parent settings cannot replace the original approved execution model.
func TestChildExecutionParentSettingsFrozen(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	if err := e.Store.UpdateThread(t.Context(), th.ID, map[string]any{"provider": "foreign", "model": "foreign-model", "credential_id": "foreign-key"}); err != nil {
		t.Fatal(err)
	}
	e.DelegationWorkspaces = childWorkspaceFixture{t.TempDir()}
	p := &budgetProvider{events: []llm.Event{{Type: llm.EventDone}}}
	e.LLMs.Register(p)
	child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 || got[0].State != protocol.AgentStateCompleted || p.requests[0].Model != "deterministic" {
		t.Fatalf("parent settings replaced original scope %+v", got)
	}
}

func TestChildExecutionScopes(t *testing.T) {
	e, th, parent, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte("CHILD_PROJECT_GUIDANCE"), 0600)
	os.WriteFile(filepath.Join(root, "source.txt"), []byte("scoped source"), 0600)
	e.DelegationWorkspaces = childWorkspaceFixture{root}
	p := &budgetProvider{events: []llm.Event{{Type: llm.EventTextDelta, Text: "child result"}, {Type: llm.EventDone, Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, Reported: true}}}}
	e.LLMs.Register(p)
	child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "small change", AcceptanceChecks: []string{"CRITERION"}, ContextRefs: []string{"source.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	got, err := e.DelegationCoordinator.Wait(waitCtx, task.ID, []string{child.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != protocol.AgentStateCompleted || got[0].Result.Summary != "child result" {
		t.Fatalf("child outcome %+v", got)
	}
	if len(p.requests) != 1 {
		t.Fatalf("requests %d", len(p.requests))
	}
	encoded, _ := json.Marshal(p.requests[0])
	sent := string(encoded)
	for _, want := range []string{"CHILD_PROJECT_GUIDANCE", "CRITERION", "scoped source"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("missing %q: %s", want, sent)
		}
	}
	for _, secret := range []string{"parent private unrelated transcript", parent} {
		if strings.Contains(sent, secret) {
			t.Fatalf("unscoped context leaked %q", secret)
		}
	}
	if p.budgets[0] == nil || got[0].Usage.Requests != 1 {
		t.Fatalf("budget/child usage lost: %+v", got[0])
	}
	childJSON, _ := json.Marshal(got[0])
	var childAttrs map[string]json.RawMessage
	json.Unmarshal(childJSON, &childAttrs)
	if !strings.Contains(string(childAttrs["model"]), "deterministic") {
		t.Errorf("actual model missing from child snapshot: %s", childJSON)
	}
	threads, _, err := e.Store.ListThreads(t.Context(), protocol.ThreadListParams{})
	if err != nil || len(threads) != 1 {
		t.Fatalf("internal child listed %+v %v", threads, err)
	}
	hits, err := e.Store.SearchItems(t.Context(), "child result", 100)
	if err != nil || len(hits) != 0 {
		t.Fatalf("internal child searched %+v %v", hits, err)
	}
	execution, err := e.Store.AgentExecution(t.Context(), task.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Model.Model != "deterministic" || execution.ThreadID == th.ID {
		t.Fatalf("execution metadata %+v", execution)
	}
	if _, err := e.ReadThread(t.Context(), execution.ThreadID); err == nil {
		t.Fatal("ordinary read exposed internal child")
	}
	if _, err := e.ExportThread(t.Context(), execution.ThreadID); err == nil {
		t.Fatal("ordinary export exposed internal child")
	}
}

func TestChildExecutionWorkspaceRequired(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	for _, w := range []WorkspaceResolver{nil, childWorkspaceFixture{root}} {
		e.DelegationWorkspaces = w
		child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "workspace-"+time.Now().String(), protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "change"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		children, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if children[0].State != protocol.AgentStateFailed || children[0].Result.Blocker == "" {
			t.Fatalf("unsafe workspace did not block %+v", children)
		}
	}
}

func TestChildExecutionReviewerPolicy(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	os.WriteFile(sentinel, []byte("original"), 0600)
	ctx := withChildExecution(WithDelegationChild(t.Context()), &childExecution{child: protocol.AgentChild{Role: protocol.AgentRoleReviewer}, workspace: ChildWorkspace{Root: root}})
	ctx = tools.WithScope(ctx, &tools.Scope{Root: root, ApprovalMode: "auto_all"})
	for _, name := range []string{"file.write", "file.edit", "shell.run", "exec.start", "exec.write", "exec.stop", "verification.plan", "verification.run", "skill.run_script", "mcp_mutate", "plugin.mutate", "agent.spawn", "agent_spawn", "agent.activate"} {
		for _, name := range []string{name, tools.ToWire(name)} {
			call := llm.ToolCall{ID: name, Name: name, Args: json.RawMessage(`{"path":"sentinel","content":"changed","command":"echo changed > sentinel"}`)}
			result := e.runTool(ctx, ctx, th, protocol.Turn{ThreadID: th.ID}, call, nil)
			if !result.IsError {
				t.Errorf("reviewer allowed %s", name)
			}
		}
	}
	data, _ := os.ReadFile(sentinel)
	if string(data) != "original" {
		t.Fatal("reviewer changed file")
	}
}

// Removing the real-turn cancellation registration or followup boundary drains
// makes this fail: the provider holds its first stream until a correction exists.
type controlledChildProvider struct {
	entered  chan llm.Request
	release  chan struct{}
	requests []llm.Request
}

func (*controlledChildProvider) ID() string { return "claude" }
func (*controlledChildProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return nil, nil
}
func (p *controlledChildProvider) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.requests = append(p.requests, req)
	ch := make(chan llm.Event, 2)
	if len(p.requests) == 1 {
		p.entered <- req
		go func() {
			defer close(ch)
			select {
			case <-ctx.Done():
				ch <- llm.Event{Type: llm.EventError, Err: ctx.Err()}
			case <-p.release:
				ch <- llm.Event{Type: llm.EventTextDelta, Text: "first answer"}
				ch <- llm.Event{Type: llm.EventDone}
			}
		}()
	} else {
		ch <- llm.Event{Type: llm.EventTextDelta, Text: "corrected answer"}
		ch <- llm.Event{Type: llm.EventDone}
		close(ch)
	}
	return ch, nil
}
func TestChildExecutionFollowupAndCancellation(t *testing.T) {
	for _, cancelChild := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelChild), func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			task := childExecutionTask(t, e, th)
			e.DelegationWorkspaces = childWorkspaceFixture{t.TempDir()}
			p := &controlledChildProvider{entered: make(chan llm.Request, 1), release: make(chan struct{})}
			e.LLMs.Register(p)
			child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "start", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-p.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("provider not entered")
			}
			if cancelChild {
				err = e.DelegationCoordinator.Cancel(t.Context(), task.ID, child.ID)
			} else {
				_, err = e.DelegationCoordinator.Followup(t.Context(), task.ID, child.ID, "correction", "CORRECTION_AT_BOUNDARY")
				close(p.release)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Cancellation makes a child terminal before runner exit; wait for actual loop.
			deadline := time.Now().Add(5 * time.Second)
			for {
				e.mu.Lock()
				running := len(e.activeTurns)
				e.mu.Unlock()
				if running == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("actual child loop stayed alive")
				}
				time.Sleep(time.Millisecond)
			}
			ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			got, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID})
			if err != nil {
				t.Fatal(err)
			}
			if cancelChild {
				if got[0].State != protocol.AgentStateCancelled || len(p.requests) != 1 {
					t.Fatalf("cancel result %+v requests %d", got, len(p.requests))
				}
				return
			}
			if got[0].State != protocol.AgentStateCompleted || len(p.requests) != 2 {
				t.Fatalf("followup outcome %+v requests %d", got, len(p.requests))
			}
			encoded, _ := json.Marshal(p.requests[1])
			if !strings.Contains(string(encoded), "CORRECTION_AT_BOUNDARY") {
				t.Fatal("queued correction never reached provider")
			}
			pending, err := e.Store.PendingAgentFollowups(t.Context(), task.ID, child.ID)
			if err != nil || len(pending) != 0 {
				t.Fatalf("unacknowledged %+v %v", pending, err)
			}
			execution, _ := e.Store.AgentExecution(t.Context(), task.ID, child.ID)
			items, _ := e.Store.ListItems(t.Context(), execution.ThreadID, 0)
			count := 0
			for _, it := range items {
				if it.Text == "CORRECTION_AT_BOUNDARY" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("correction persisted %d times", count)
			}
		})
	}
}
