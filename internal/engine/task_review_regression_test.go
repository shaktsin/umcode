package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func persistTaskTestUser(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn, text string) protocol.Item {
	t.Helper()
	item, err := e.newItem(t.Context(), turn, protocol.ItemUserMessage)
	if err != nil {
		t.Fatal(err)
	}
	item.Text, item.Status = text, protocol.ItemCompleted
	if err := e.Store.SaveItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	return item
}
func waitTaskReviewApproval(t *testing.T, e *Engine) protocol.Approval {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("approval not requested")
		default:
		}
		list, err := e.Store.ListApprovals(t.Context(), "pending")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			e.mu.Lock()
			live := e.approvals[a.ID] != nil
			e.mu.Unlock()
			if live {
				return a
			}
		}
		time.Sleep(time.Millisecond)
	}
}
func TestTaskInterruptedShellCannotReplay(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	user := persistTaskTestUser(t, e, th, turn, "Append one marker")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Tools.HostSandbox = "off"
	tools.RegisterBuiltins(e.Tools, e.Cfg, nil, nil)
	root := t.TempDir()
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: root, AllowNet: true, ApprovalMode: "auto_all"})
	args := json.RawMessage(`{"command":"printf marker >> marker.txt; sleep 2","timeout_seconds":1}`)
	out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "first", Name: "shell.run", Args: args}, nil)
	if !out.IsError {
		t.Fatal("timed-out command reported success")
	}
	snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
	if snap.Operations[0].Status != "outcome_unknown" {
		t.Fatalf("uncertain effect recorded as %s", snap.Operations[0].Status)
	}
	out = e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "retry", Name: "shell.run", Args: args}, nil)
	b, _ := os.ReadFile(filepath.Join(root, "marker.txt"))
	if !out.IsError || string(b) != "marker" {
		t.Fatalf("unsafe replay: %+v %q", out, b)
	}
}
func TestTaskPersistedBlockerCorrectsLaterSummary(t *testing.T) {
	e, th, _, provider := taskRuntimeFixture(t, "edit_conflict", false)
	// First fail one edit; then the next request ends the turn without fixing it.
	provider.mode = "loop"
	provider.calls = 3
	runTaskRuntimeTurn(t, e, th, "retry")
	provider.mode = "summary_only"
	provider.calls = 0
	finished := runTaskRuntimeTurn(t, e, th, "retry")
	if finished.Status != protocol.TurnFailed {
		t.Fatalf("persisted blocker allowed completion: %+v", finished)
	}
	items, _ := e.Store.ListItems(t.Context(), th.ID, 0)
	for _, it := range items {
		if it.TurnID == finished.ID && it.Kind == protocol.ItemAgentMessage && strings.Contains(it.Text, "successfully") {
			t.Fatalf("false summary survived: %s", it.Text)
		}
	}
}
func TestTaskRetryOpaqueAndArchivedAmbiguity(t *testing.T) {
	for _, kind := range []string{"opaque", "archived"} {
		t.Run(kind, func(t *testing.T) {
			snap := protocol.TaskSnapshot{}
			for _, path := range []string{"one", "two"} {
				op := protocol.TaskOperation{ID: path, Status: "outcome_unknown", Tool: "shell.run", Args: json.RawMessage(`{"command":"` + path + `"}`)}
				if kind == "archived" {
					op.Tool = "file.write"
					op.Args = nil
					op.ArgsRef = "item-" + path
					op.Resources = json.RawMessage(`{"path":"` + path + `"}`)
				}
				snap.Operations = append(snap.Operations, op)
			}
			if !ambiguousTaskRetry(snap) {
				t.Fatal("unrelated operations silently selected")
			}
		})
	}
}
func TestTaskWorkflowRevalidatedAfterApproval(t *testing.T) {
	e, th, turn, st, g := workflowEngine(t)
	e.Cfg.Policy.RequireWorkflowApproval = true
	if err := st.UpdateWorkNode(t.Context(), g.NodeID, "approved", 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: g.WorkID, Kind: "task", Status: "ready", Content: json.RawMessage(`{"required":true}`)}); err != nil {
		t.Fatal(err)
	}
	user := persistTaskTestUser(t, e, th, turn, "Make the approved change")
	if _, err := st.CreateExecutionTask(t.Context(), protocol.ExecutionTask{ThreadID: th.ID, WorkID: g.WorkID, OriginItemID: user.ID, Objective: user.Text}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateThread(t.Context(), th.ID, map[string]any{"approval_mode": "normal"}); err != nil {
		t.Fatal(err)
	}
	th.ApprovalMode = "normal"
	tool := &engineTestTool{name: "file.write", risk: tools.RiskRed, output: "effect ran"}
	e.Tools.Add(tool)
	done := make(chan toolRunResult, 1)
	go func() {
		done <- e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "review-wait", Name: "file.write", Args: json.RawMessage(`{"path":"notes.txt","content":"changed"}`)}, nil)
	}()
	pending := waitTaskReviewApproval(t, e)
	if err := st.UpdateWorkNode(t.Context(), g.NodeID, "rejected", 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), pending.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		if !out.IsError || tool.calls.Load() != 0 {
			t.Fatalf("stale graph authority: %+v effects=%d", out, tool.calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not finish")
	}
}

func TestTaskOpaqueOutcomeUserResolution(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	user := persistTaskTestUser(t, e, th, turn, "Append one marker then continue")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Tools.HostSandbox = "off"
	tools.RegisterBuiltins(e.Tools, e.Cfg, nil, nil)
	e.Tools.Add(&taskProposalTool{engine: e})
	root := t.TempDir()
	ctx := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{Root: root, AllowNet: true, ApprovalMode: "auto_all"})
	e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "interrupted", Name: "shell.run", Args: json.RawMessage(`{"command":"printf marker >> marker.txt; sleep 2","timeout_seconds":1}`)}, nil)
	snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
	unknown := snap.Operations[0]
	e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "inspect", Name: "file.read", Args: json.RawMessage(`{"path":"marker.txt"}`)}, nil)
	snap, _ = e.Store.ActiveExecutionTask(ctx, th.ID)
	evidence := snap.Operations[len(snap.Operations)-1].ItemID
	args, _ := json.Marshal(map[string]any{"task_id": snap.Task.ID, "expected_revision": snap.Task.Revision, "resolution": map[string]any{"operation_id": unknown.ID, "evidence_ids": []string{evidence}, "reason": "The marker is present once. Accept the inspected state and continue without repeating this action."}})
	done := make(chan toolRunResult, 1)
	go func() {
		done <- e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "resolve", Name: "task.propose", Args: args}, nil)
	}()
	pending := waitTaskReviewApproval(t, e)
	if _, err := e.RespondApproval(t.Context(), pending.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		if out.IsError {
			t.Fatal(out.Output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolution did not finish")
	}
	current, _ := e.Store.GetTaskOperation(ctx, unknown.ID)
	if current.Status != "outcome_unknown" || current.Resolution != "acknowledged" || current.ResolvedByID == "" {
		t.Fatalf("lost uncertainty or acknowledgement: %+v", current)
	}
	out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "continue", Name: "file.write", Args: json.RawMessage(`{"path":"next.txt","content":"continued"}`)}, nil)
	marker, _ := os.ReadFile(filepath.Join(root, "marker.txt"))
	next, _ := os.ReadFile(filepath.Join(root, "next.txt"))
	if out.IsError || string(marker) != "marker" || string(next) != "continued" {
		t.Fatalf("resolution replayed or remained blocked: %+v marker=%q next=%q", out, marker, next)
	}
}

func TestTaskChildRequirementRevalidatedAfterApproval(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	user := persistTaskTestUser(t, e, th, turn, "Work within the approved parent scope")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	parentRoot, childRoot := t.TempDir(), t.TempDir()
	x := &childExecution{task: protocol.AgentTask{ID: "admitted-child-task", ThreadID: th.ID, RootTurnID: turn.ID, ParentRoot: parentRoot}, child: protocol.AgentChild{Role: protocol.AgentRoleImplementer}, workspace: ChildWorkspace{Root: childRoot}}
	ctx := tools.WithScope(withChildExecution(t.Context(), x), &tools.Scope{Root: childRoot, RestrictedChild: true})
	op := protocol.TaskOperation{Tool: "file.write", Args: json.RawMessage(`{"path":"notes.txt","content":"changed"}`)}
	initial, err := e.checkChildTaskAuthority(ctx, op)
	if err != nil || initial.Outcome != "allow" {
		t.Fatalf("initial authority: %+v %v", initial, err)
	}
	item, err := e.newItem(t.Context(), turn, protocol.ItemToolCall)
	if err != nil {
		t.Fatal(err)
	}
	item.Tool = &protocol.ToolCallData{Name: "file.write", Args: op.Args}
	if err := e.Store.SaveItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	done := make(chan protocol.AuthorityDecision, 1)
	go func() {
		approved, err := e.requestApproval(t.Context(), t.Context(), turn, item, "file.write", op.Args, tools.RiskRed, "review", "Write notes")
		if err != nil || !approved {
			done <- protocol.AuthorityDecision{Outcome: "deny"}
			return
		}
		d, _ := e.revalidateTaskAction(ctx, th, nil, op)
		done <- d
	}()
	pending := waitTaskReviewApproval(t, e)
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if _, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"no_actions"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), pending.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-done:
		if d.Outcome != "deny" {
			t.Fatalf("new parent restriction missed after approval: %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not finish")
	}
	if _, err := os.Stat(filepath.Join(childRoot, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("child effect ran")
	}
}

func TestTaskOpaqueDisconnectIsUncertain(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	user := persistTaskTestUser(t, e, th, turn, "Perform the external action once")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	tool := &engineTestTool{name: "external.submit", risk: tools.RiskGreen, err: io.EOF}
	e.Tools.Add(tool)
	e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "disconnect", Name: tool.Name(), Args: json.RawMessage(`{}`)}, nil)
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if len(snap.Operations) != 1 || snap.Operations[0].Status != "outcome_unknown" {
		t.Fatalf("disconnect permits duplicate action: %+v", snap.Operations)
	}
}

func TestTaskConfiguredReviewCannotUseAgentApproval(t *testing.T) {
	e, th, turn, st, g := workflowEngine(t)
	e.Cfg.Policy.RequireWorkflowApproval = true
	if err := st.UpdateWorkNode(t.Context(), g.NodeID, "approved", 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: g.WorkID, Kind: "task", Status: "ready", Content: json.RawMessage(`{"required":true}`)}); err != nil {
		t.Fatal(err)
	}
	user := persistTaskTestUser(t, e, th, turn, "Review the solution before implementation")
	if _, err := st.CreateExecutionTaskWithRequirements(t.Context(), protocol.ExecutionTask{ThreadID: th.ID, WorkID: g.WorkID, OriginItemID: user.ID, Objective: user.Text}, e.policyTaskRequirements()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := st.ActiveExecutionTask(t.Context(), th.ID)
	d, err := e.checkTaskAuthority(t.Context(), th, snapshot, protocol.TaskOperation{TaskID: snapshot.Task.ID, Tool: "file.write", Args: json.RawMessage(`{"path":"notes.txt"}`)})
	if err != nil || d.Outcome != "ask" {
		t.Fatalf("model approval satisfied configured human review: %+v %v", d, err)
	}
}
