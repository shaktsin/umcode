package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// Only the paid provider boundary is fake. Admission, hidden turns, tools,
// managed Git snapshots, review binding and integration use production code.
type workflowScriptProvider struct {
	mu     sync.Mutex
	counts map[string]int
	calls  int
	script func(context.Context, llm.Request, int) []llm.Event
}

// Reopening a durable snapshot of an approval wait must never execute it. The
// first engine is shut down after the snapshot, so no two engines own live work.
func TestDelegationWorkflowRestartDuringApproval(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	sub := &childSubscriber{make(chan childEvent, 256)}
	e.Bus.Add(sub)
	p := &workflowScriptProvider{script: func(_ context.Context, _ llm.Request, n int) []llm.Event {
		if n == 0 {
			return workflowCall("shell.run", map[string]any{"command": "printf unapproved > never.txt"})
		}
		return workflowDone()
	}}
	e.LLMs.Register(p)
	child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "waiting", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "Wait for permission"})
	if err != nil {
		t.Fatal(err)
	}
	var approval protocol.Approval
	deadline := time.After(15 * time.Second)
	for approval.ID == "" {
		select {
		case ev := <-sub.events:
			if ev.method == protocol.NotifyApprovalRequest {
				approval = ev.params.(protocol.ApprovalEvent).Approval
			}
		case <-deadline:
			t.Fatal("no approval request")
		}
	}
	before, err := e.Store.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil || before.State != protocol.AgentStateWaitingApproval {
		t.Fatalf("pending child %+v %v", before, err)
	}
	before.Result.Summary = "Recover partial findings"
	before.Result.Findings = []protocol.AgentFinding{{Message: "Retain inspection", Severity: "important"}}
	if err = e.Store.UpdateAgentChild(t.Context(), before, before.Version); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "restart.db")
	if _, err = e.Store.DB.ExecContext(t.Context(), `VACUUM INTO ?`, backup); err != nil {
		t.Fatal(err)
	}
	e.Shutdown(t.Context())
	workflowAwaitIdle(t, e)
	if p.callCount() != 1 {
		t.Fatal("shutdown continued paid execution")
	}
	e.mu.Lock()
	waiters := len(e.approvals)
	e.mu.Unlock()
	if waiters != 0 {
		t.Fatal("shutdown left approval waiters")
	}
	st, err := store.Open(t.Context(), backup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	noCalls := &workflowScriptProvider{script: func(context.Context, llm.Request, int) []llm.Event { return workflowDone() }}
	reg := llm.NewRegistry()
	reg.Register(noCalls)
	restarted, err := New(t.Context(), Options{Config: e.Cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Shutdown(context.Background()) })
	got, err := st.AgentTask(t.Context(), task.ID)
	if err != nil || got.State != protocol.AgentStateInterrupted {
		t.Fatalf("task auto-resumed %+v %v", got, err)
	}
	c, err := st.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil || c.State != protocol.AgentStateInterrupted || c.WorkspaceID != before.WorkspaceID || c.Result.Summary != before.Result.Summary || len(c.Result.Findings) != 1 {
		t.Fatalf("recovery lost artifacts %+v %v", c, err)
	}
	a, err := st.GetApproval(t.Context(), approval.ID)
	if err != nil || a.Status != "expired" || a.AgentTaskID != task.ID || a.AgentChildID != child.ID {
		t.Fatalf("obsolete approval %+v %v", a, err)
	}
	if _, err = restarted.RespondApproval(t.Context(), a.ID, true, false, "late"); err == nil {
		t.Fatal("obsolete approval accepted")
	}
	execution, err := st.AgentExecution(t.Context(), task.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := st.ListTurns(store.WithInternalThreads(t.Context()), execution.ThreadID)
	if err != nil || len(turns) != 1 || turns[0].Status != protocol.TurnInterrupted {
		t.Fatalf("hidden child turn not interrupted %+v %v", turns, err)
	}
	if _, _, err = restarted.DelegationCoordinator.Task(t.Context(), got, protocol.ExecutionLimits{}); err == nil {
		t.Fatal("restart automatically readmitted task")
	}
	if noCalls.callCount() != 0 {
		t.Fatal("restart reached paid provider")
	}
	w, err := st.AgentWorkspaceForChild(t.Context(), child.ID, "implementer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(w.Root, "never.txt")); !os.IsNotExist(err) {
		t.Fatal("unapproved command executed")
	}
}

func (*workflowScriptProvider) ID() string { return "claude" }
func (*workflowScriptProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *workflowScriptProvider) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	x := childExecutionFrom(ctx)
	key := "lead"
	if x != nil {
		key = fmt.Sprintf("%s/%d", x.child.ID, x.child.CorrectionRounds)
	}
	if p.counts == nil {
		p.counts = map[string]int{}
	}
	n := p.counts[key]
	p.counts[key]++
	p.calls++
	evs := p.script(ctx, req, n)
	ch := make(chan llm.Event, len(evs))
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)
	return ch, nil
}
func (p *workflowScriptProvider) callCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }
func workflowCall(name string, args any) []llm.Event {
	b, _ := json.Marshal(args)
	return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "workflow-call", Name: tools.ToWire(name), Args: b}}, {Type: llm.EventDone}}
}
func workflowDone() []llm.Event {
	return []llm.Event{{Type: llm.EventTextDelta, Text: "Retained result"}, {Type: llm.EventDone}}
}
func workflowParentContext(t *testing.T, e *Engine, th protocol.Thread, root string, task protocol.AgentTask) context.Context {
	t.Helper()
	return tools.WithScope(withDelegationTask(t.Context(), task), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
}
func workflowWorker(t *testing.T, e *Engine, task protocol.AgentTask, role protocol.AgentRole, call string) protocol.AgentChild {
	t.Helper()
	c, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, call, role, protocol.AgentAssignment{Prompt: "Work on the bounded assignment"})
	if err != nil {
		t.Fatal(err)
	}
	return workflowWait(t, e, task, c.ID)
}
func workflowWait(t *testing.T, e *Engine, task protocol.AgentTask, id string) protocol.AgentChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cs, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	return cs[0]
}
func workflowChildScript(mode string) func(context.Context, llm.Request, int) []llm.Event {
	return func(ctx context.Context, _ llm.Request, n int) []llm.Event {
		x := childExecutionFrom(ctx)
		if x == nil || n > 0 {
			return workflowDone()
		}
		if x.child.Role == protocol.AgentRoleImplementer {
			return workflowCall("file.write", map[string]any{"path": "result.txt", "content": fmt.Sprintf("submission %d", x.child.CorrectionRounds)})
		}
		report := map[string]any{"revision": x.workspace.Revision, "verdict": "approved", "findings": []protocol.AgentFinding{}}
		switch mode {
		case "missing":
			return workflowDone()
		case "stale":
			report["revision"] = "incorrect-revision"
		case "malformed":
			report["verdict"] = "looks_good"
		case "no findings":
			delete(report, "findings")
		case "unresolved":
			report["findings"] = []protocol.AgentFinding{{Message: "unresolved issue", Severity: "important"}}
		case "empty request":
			report["verdict"] = "changes_requested"
		case "empty message":
			report["verdict"] = "changes_requested"
			report["findings"] = []protocol.AgentFinding{{Severity: "important"}}
		}
		return workflowCall("agent.review_report", report)
	}
}

// Friendly summaries, malformed reports, and invented revisions cannot approve.
func TestDelegationWorkflowRejectsInvalidReview(t *testing.T) {
	for _, mode := range []string{"missing", "stale", "malformed", "no findings", "unresolved", "empty request", "empty message"} {
		t.Run(mode, func(t *testing.T) {
			e, th, root, _ := delegationFixture(t)
			task := childExecutionTask(t, e, th)
			p := &workflowScriptProvider{script: workflowChildScript(mode)}
			e.LLMs.Register(p)
			impl := workflowWorker(t, e, task, protocol.AgentRoleImplementer, "impl")
			if impl.State != protocol.AgentStateCompleted {
				t.Fatalf("implementer %+v", impl)
			}
			review := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "review")
			if review.State != protocol.AgentStateFailed || review.Result.Blocker == "" {
				t.Fatalf("invalid review accepted %+v", review)
			}
			if _, err := e.delegationWorkflowCall(workflowParentContext(t, e, th, root, task), "integrate", json.RawMessage(`{}`)); err == nil {
				t.Fatal("invalid review integrated")
			}
			if got := delegationGit(t, root, "status", "--porcelain"); got != "" {
				t.Fatalf("parent changed: %s", got)
			}
			if _, err := e.Store.AgentWorkspaceForChild(t.Context(), impl.ID, "implementer"); err != nil {
				t.Fatal("failed review lost implementation", err)
			}
		})
	}
}

// Removing ordering, exact revision binding, or the two-round cap fails this.
func TestDelegationWorkflowOrderingAndCorrections(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	p := &workflowScriptProvider{script: workflowChildScript("approved")}
	e.LLMs.Register(p)
	if _, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "early", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "review"}); err == nil {
		t.Fatal("review admitted before implementation")
	}
	impl := workflowWorker(t, e, task, protocol.AgentRoleImplementer, "impl")
	if _, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "second", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "replacement"}); err == nil {
		t.Fatal("second implementer identity admitted")
	}
	review := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "review")
	if review.State != protocol.AgentStateCompleted || review.Result.Review == nil {
		t.Fatalf("valid review %+v", review)
	}
	if _, err := e.DelegationCoordinator.Followup(t.Context(), task.ID, review.ID, "retarget", "review another revision"); err == nil {
		t.Fatal("review identity retargeted")
	}
	ctx := workflowParentContext(t, e, th, root, task)
	if _, err := e.delegationWorkflowCall(ctx, "review_report", json.RawMessage(`{"revision":"forged","verdict":"approved","findings":[]}`)); err == nil {
		t.Fatal("lead forged reviewer provenance")
	}
	for round := 1; round <= 2; round++ {
		_, err := e.DelegationCoordinator.Followup(t.Context(), task.ID, impl.ID, fmt.Sprintf("fix%d", round), "Correct the implementation")
		if err != nil {
			t.Fatal(err)
		}
		corrected := workflowWait(t, e, task, impl.ID)
		if corrected.State != protocol.AgentStateCompleted || corrected.CorrectionRounds != round || corrected.Revision == impl.Revision {
			t.Fatalf("correction %+v", corrected)
		}
		if _, err = e.delegationWorkflowCall(ctx, "integrate", json.RawMessage(`{}`)); err == nil {
			t.Fatal("stale reviewer approved changed revision")
		}
	}
	calls := p.callCount()
	if _, err := e.DelegationCoordinator.Followup(t.Context(), task.ID, impl.ID, "third", "again"); err == nil {
		t.Fatal("third correction admitted")
	}
	if calls != p.callCount() {
		t.Fatal("rejected correction reached provider")
	}
	fresh := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "fresh-review")
	if fresh.ID == review.ID || fresh.State != protocol.AgentStateCompleted {
		t.Fatalf("fresh review %+v", fresh)
	}
	if _, err := e.delegationWorkflowCall(ctx, "complete", json.RawMessage(`{"checks":[{"status":"passed"}]}`)); err == nil {
		t.Fatal("model-supplied completion evidence accepted")
	}
	// A new user edit after review must not be overwritten by integration.
	if err := os.WriteFile(filepath.Join(root, "user.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.delegationWorkflowCall(ctx, "integrate", json.RawMessage(`{}`)); err == nil {
		t.Fatal("dirty parent integrated")
	}
	data, _ := os.ReadFile(filepath.Join(root, "user.txt"))
	if string(data) != "keep" {
		t.Fatal("user edit overwritten")
	}
}

// Partial output and actual token exhaustion remain failed, retain artifacts,
// and cannot trigger a reviewer or a new paid correction after budget stop.
func TestDelegationWorkflowFailedWorker(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprint(exhausted), func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			task := childExecutionTask(t, e, th)
			_, budget, err := e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			p := &workflowScriptProvider{script: func(context.Context, llm.Request, int) []llm.Event {
				usage := llm.Usage{InputTokens: 100, OutputTokens: 4096, Reported: true}
				if exhausted {
					usage.OutputTokens = 2_000_000
				}
				return []llm.Event{{Type: llm.EventTextDelta, Text: "Partial findings retained"}, {Type: llm.EventDone, StopReason: "max_output_tokens", Usage: usage}}
			}}
			e.LLMs.Register(p)
			child := workflowWorker(t, e, task, protocol.AgentRoleImplementer, "impl")
			if child.State != protocol.AgentStateFailed || !strings.Contains(child.Result.Blocker, "output limit") || !strings.Contains(child.Result.Summary, "Partial findings") || child.WorkspaceID == "" {
				t.Fatalf("failure evidence lost %+v", child)
			}
			if child.Usage.OutputTokens == 0 {
				t.Fatal("actual failed usage lost")
			}
			if _, err = e.DelegationCoordinator.Spawn(t.Context(), task.ID, "review", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "review"}); err == nil {
				t.Fatal("failed worker admitted review")
			}
			if exhausted {
				if budget.StopReason() == "" {
					t.Fatal("actual overrun not exhausted")
				}
				if _, err = e.DelegationCoordinator.Followup(t.Context(), task.ID, child.ID, "retry", "again"); err == nil {
					t.Fatal("paid retry after exhausted budget")
				}
			}
			if p.callCount() != 1 {
				t.Fatalf("unexpected paid retry %d", p.callCount())
			}
		})
	}
}

// Cancellation must reach an actual confined command and stop provider looping.
func TestDelegationWorkflowCancelDuringCommand(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	if err := e.Store.UpdateThread(t.Context(), th.ID, map[string]any{"approval_mode": "auto_all"}); err != nil {
		t.Fatal(err)
	}
	task := childExecutionTask(t, e, th)
	p := &workflowScriptProvider{script: func(_ context.Context, _ llm.Request, n int) []llm.Event {
		if n == 0 {
			return workflowCall("shell.run", map[string]any{"command": "printf started > command.started; while :; do sleep 1; done", "timeout_seconds": 60})
		}
		return workflowDone()
	}}
	e.LLMs.Register(p)
	child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "command", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "Run the bounded cancellable command"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var workspaceRoot string
	for time.Now().Before(deadline) {
		w, err := e.Store.AgentWorkspaceForChild(t.Context(), child.ID, "implementer")
		if err == nil {
			workspaceRoot = w.Root
			if _, err = os.Stat(filepath.Join(w.Root, "command.started")); err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = os.Stat(filepath.Join(workspaceRoot, "command.started")); err != nil {
		t.Fatalf("real command never started: %v", err)
	}
	result, err := e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: th.ID, TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.State != protocol.AgentStateCancelled {
		t.Fatalf("cancel result %+v", result)
	}
	workflowAwaitIdle(t, e)
	if p.callCount() != 1 {
		t.Fatalf("provider called after command cancellation: %d", p.callCount())
	}
	current, err := e.DelegationCoordinator.Status(t.Context(), task.ID, child.ID)
	if err != nil || current.State != protocol.AgentStateCancelled || current.WorkspaceID == "" {
		t.Fatalf("cancelled artifacts lost %+v %v", current, err)
	}
	if data, err := os.ReadFile(filepath.Join(workspaceRoot, "command.started")); err != nil || string(data) != "started" {
		t.Fatal("cancelled command output lost", err)
	}
	if _, err = e.DelegationCoordinator.Followup(t.Context(), task.ID, child.ID, "late", "continue"); err == nil {
		t.Fatal("cancelled task resumed")
	}
	if p.callCount() != 1 {
		t.Fatal("late correction made provider call")
	}
}
func workflowAwaitIdle(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		n := len(e.activeTurns)
		e.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actual execution did not stop")
}

// The planner's explicit required visual QA cannot disappear from completion.
func TestDelegationWorkflowRequiredVisualQA(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test:e2e":"playwright test"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "browser check fixture")
	task := childExecutionTask(t, e, th)
	base := workflowChildScript("approved")
	p := &workflowScriptProvider{script: func(ctx context.Context, req llm.Request, n int) []llm.Event {
		x := childExecutionFrom(ctx)
		if x != nil && x.child.Role == protocol.AgentRoleImplementer && n == 0 {
			return workflowCall("file.write", map[string]any{"path": "index.html", "content": "<p>Updated page</p>"})
		}
		return base(ctx, req, n)
	}}
	e.LLMs.Register(p)
	workflowWorker(t, e, task, protocol.AgentRoleImplementer, "impl")
	workflowWorker(t, e, task, protocol.AgentRoleReviewer, "review")
	ctx := workflowParentContext(t, e, th, root, task)
	if _, err := e.delegationWorkflowCall(ctx, "integrate", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	w, err := e.Store.AgentWorkflow(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Checks) != 2 || w.Checks[1].Kind != "visual" || w.Checks[1].Status != "blocked" {
		t.Fatalf("required visual QA was dropped: %+v", w)
	}
	// Preserve the actual browser service's not_run reason.
	before, err := e.beginDelegationVerification(ctx, "browser.verify")
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := e.Tools.Get("browser.verify")
	if !ok {
		t.Fatal("browser service missing")
	}
	raw, err := tool.Call(ctx, json.RawMessage(`{"framework":"playwright","command":"npm run test:e2e"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.finishDelegationVerification(ctx, "browser.verify", before, raw); err != nil {
		t.Fatal(err)
	}
	w, err = e.Store.AgentWorkflow(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Checks[0].Status != "not_run" || !strings.Contains(w.Checks[0].Summary, "requires isolated compute") {
		t.Fatalf("browser blocker lost: %+v", w.Checks[0])
	}
	if _, err = e.delegationWorkflowCall(ctx, "complete", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unverified required visual QA completed")
	}
}

// A later independent rejection revokes a prior approval of the same SHA.
func TestDelegationWorkflowLatestReviewAuthoritative(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	base := workflowChildScript("approved")
	reviews := 0
	p := &workflowScriptProvider{script: func(ctx context.Context, req llm.Request, n int) []llm.Event {
		x := childExecutionFrom(ctx)
		if x != nil && x.child.Role == protocol.AgentRoleReviewer && n == 0 {
			reviews++
			if reviews == 4 {
				return workflowDone()
			}
			if reviews == 2 {
				return workflowCall("agent.review_report", map[string]any{"revision": x.workspace.Revision, "verdict": "changes_requested", "findings": []protocol.AgentFinding{{Message: "New unresolved issue", Severity: "important"}}})
			}
		}
		return base(ctx, req, n)
	}}
	e.LLMs.Register(p)
	impl := workflowWorker(t, e, task, protocol.AgentRoleImplementer, "impl")
	first := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "approved")
	orphan, err := e.Store.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "Pre-binding attempt"}}, "pre-binding")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Store.AgentReviewEligible(t.Context(), task, impl, impl.Revision); err == nil {
		t.Fatal("pending pre-binding review reused historical approval")
	}
	orphan.State = protocol.AgentStateFailed
	if err = e.Store.UpdateAgentChild(t.Context(), orphan, orphan.Version); err != nil {
		t.Fatal(err)
	}
	second := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "rejected")
	if first.Result.Review == nil || first.Result.Review.Verdict != "approved" || second.Result.Review == nil || second.Result.Review.Verdict != "changes_requested" {
		t.Fatalf("review fixture failed: %+v / %+v", first, second)
	}
	ctx := workflowParentContext(t, e, th, root, task)
	if _, err := e.delegationWorkflowCall(ctx, "integrate", json.RawMessage(`{}`)); err == nil {
		t.Fatal("historical approval bypassed newer unresolved findings")
	}
	if got := delegationGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("rejected review changed parent: %s", got)
	}
	// Ambiguous completion timestamps must not choose the older approval by ID.
	if _, err := e.Store.DB.ExecContext(ctx, `UPDATE agent_children SET updated_at=? WHERE id=?`, store.FormatTime(first.UpdatedAt), second.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.AgentReviewEligible(ctx, task, impl, impl.Revision); err == nil {
		t.Fatal("tied approval/rejection selected an arbitrary winner")
	}
	if _, err := e.DelegationCoordinator.Followup(ctx, task.ID, impl.ID, "fix", "Resolve new findings"); err != nil {
		t.Fatal(err)
	}
	corrected := workflowWait(t, e, task, impl.ID)
	if corrected.Revision == impl.Revision || corrected.State != protocol.AgentStateCompleted {
		t.Fatalf("correction failed %+v", corrected)
	}
	fresh := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "fresh-approved")
	if fresh.Result.Review == nil || fresh.Result.Review.Revision != corrected.Revision {
		t.Fatalf("fresh approval missing %+v", fresh)
	}
	missing := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "missing-report")
	if missing.State != protocol.AgentStateFailed {
		t.Fatalf("missing report did not fail: %+v", missing)
	}
	if _, err := e.delegationWorkflowCall(ctx, "integrate", json.RawMessage(`{}`)); err == nil {
		t.Fatal("new failed bound review reused old approval")
	}
	retried := workflowWorker(t, e, task, protocol.AgentRoleReviewer, "retry-review")
	if retried.State != protocol.AgentStateCompleted || retried.Result.Review == nil || retried.Result.Review.Verdict != "approved" {
		t.Fatalf("explicit independent review retry failed: %+v", retried)
	}
	if _, err := e.delegationWorkflowCall(ctx, "integrate", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("corrected independent approval rejected: %v", err)
	}
}
