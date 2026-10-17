package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDelegationWorkflowReachable(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	ctx := tools.WithScope(withDelegationTask(t.Context(), task), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
	for _, name := range []string{"agent.integrate", "agent.complete", "agent.review_report"} {
		if _, ok := e.Tools.Get(name); !ok {
			t.Errorf("missing actionable workflow tool %s", name)
		}
	}
	if got := e.systemPrompt(ctx, "fix this", nil, "", nil); !containsWorkflow(got) {
		t.Error("manual activation lacks sequential workflow instruction")
	}
	if got := e.systemPrompt(context.Background(), "fix this", nil, "", nil); containsWorkflow(got) {
		t.Error("ordinary prompt gained delegation authority")
	}
}
func containsWorkflow(s string) bool {
	for i := 0; i+len("agent.integrate") <= len(s); i++ {
		if s[i:i+len("agent.integrate")] == "agent.integrate" {
			return true
		}
	}
	return false
}

func TestDelegationWorkflowStartup(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	child, err := e.Store.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "work"}}, "orphan")
	if err != nil {
		t.Fatal(err)
	}
	// The startup seam must reconcile both task and child admission.
	if err = e.Store.ReconcileAgentChildren(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Store.AgentTask(t.Context(), task.ID)
	if got.State != protocol.AgentStateInterrupted {
		t.Errorf("task remains admitted after recovery: %s", got.State)
	}
	c, _ := e.Store.AgentChild(t.Context(), task.ID, child.ID)
	if c.State != protocol.AgentStateInterrupted {
		t.Errorf("child remains admitted: %s", c.State)
	}
}

// Removing the workflow from progressive selection must fail even when the
// submitted ordinary coding request contains no delegation keyword.
func TestDelegationWorkflowCatalog(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	task := childExecutionTask(t, e, th)
	ctx := tools.WithScope(withDelegationTask(t.Context(), task), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
	p, err := e.Projects.Get(ctx, th.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	s := e.startToolSelection(ctx, th, nil, &p, "fix the value")
	for _, name := range []string{"agent.spawn", "agent.wait", "agent.integrate", "agent.complete"} {
		found := false
		for _, spec := range s.specs() {
			if spec.Name == tools.ToWire(name) {
				found = true
			}
		}
		if !found {
			t.Errorf("activated catalog omitted %s", name)
		}
	}
}

type workflowProvider struct {
	goBin, goCache string
	e              *Engine
	t              *testing.T
	mu             sync.Mutex
	calls          map[string]int
	parentStep     int
	mode           string
}

func (*workflowProvider) ID() string { return "claude" }
func (*workflowProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *workflowProvider) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	events := []llm.Event{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventDone}}
	call := func(name string, args any) {
		b, _ := json.Marshal(args)
		events = []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: fmt.Sprintf("call-%s-%d", name, len(req.Messages)), Name: tools.ToWire(name), Args: b}}, {Type: llm.EventDone}}
	}
	if x := childExecutionFrom(ctx); x != nil {
		if containsWorkflow(req.System) {
			p.t.Error("child inherited lead integration instructions")
		}
		key := fmt.Sprintf("%s-%d", x.child.ID, x.child.CorrectionRounds)
		n := p.calls[key]
		p.calls[key] = n + 1
		if x.child.Role == protocol.AgentRoleImplementer && n == 0 {
			value := 1
			if x.child.CorrectionRounds > 0 {
				value = 2
			}
			call("file.write", map[string]any{"path": "value.go", "content": fmt.Sprintf("package fixture\nfunc Value()int{return %d}\n", value)})
		}
		if x.child.Role == protocol.AgentRoleImplementer && n == 1 {
			call("verification.run", map[string]any{"checks": []map[string]any{{"label": "worker compile", "command": "mkdir -p .cache && GOCACHE=" + taskFixtureQuote(p.goCache) + " " + taskFixtureQuote(p.goBin) + " tool compile -o .cache/value.o value.go", "required_command": "go"}}})
		}
		if x.child.Role == protocol.AgentRoleImplementer && n == 2 {
			last := req.Messages[len(req.Messages)-1]
			if !strings.Contains(last.Result, `"status":"passed"`) {
				p.t.Errorf("actual worker check did not pass: %s", last.Result)
			}
		}
		if x.child.Role == protocol.AgentRoleReviewer && n == 0 {
			verdict := "approved"
			findings := []protocol.AgentFinding{}
			impl, _ := p.e.workflowImplementer(ctx, x.task.ID)
			if impl.CorrectionRounds == 0 {
				verdict = "changes_requested"
				findings = append(findings, protocol.AgentFinding{Path: "value.go", Line: 2, Message: "Value must return 2", Severity: "important"})
			}
			call("agent.review_report", map[string]any{"revision": x.workspace.Revision, "verdict": verdict, "findings": findings})
		}
	} else if task, ok := DelegationTaskFromContext(ctx); ok {
		step := p.parentStep
		p.parentStep++
		if step == 0 {
			if !containsWorkflow(req.System) {
				p.t.Error("lead workflow prompt missing")
			}
			for _, name := range []string{"agent.spawn", "agent.integrate", "agent.complete"} {
				found := false
				for _, spec := range req.Tools {
					if spec.Name == tools.ToWire(name) {
						found = true
					}
				}
				if !found {
					p.t.Errorf("provider cannot discover %s", name)
				}
			}
		}
		for i := len(req.Messages) - 1; i >= 0; i-- {
			m := req.Messages[i]
			if m.Role == "tool" {
				if step == 12 {
					p.t.Logf("actual verification: %s", m.Result)
				}
				expectedError := step == 5 || step == 11 || (step == 13 && p.mode != "" && p.mode != "cleanup")
				if m.IsError != expectedError {
					p.t.Errorf("step %d last tool %s: %s", step, m.ToolName, m.Result)
				}
				break
			}
		}
		children, _ := p.e.Store.AgentChildren(ctx, task.ID)
		var impl, review protocol.AgentChild
		for _, c := range children {
			if c.Role == protocol.AgentRoleImplementer {
				impl = c
			} else if review.ID == "" || c.CreatedAt.After(review.CreatedAt) {
				review = c
			}
		}
		if step == 12 && p.mode == "mutated" {
			if err := os.WriteFile(filepath.Join(task.ParentRoot, "value.go"), []byte("package fixture\nfunc Value()int{return 99}\n"), 0600); err != nil {
				p.t.Fatal(err)
			}
		}
		if step == 12 && p.mode == "cleanup" {
			w, err := p.e.Store.AgentWorkspaceForChild(ctx, impl.ID, "implementer")
			if err != nil {
				return nil, err
			}
			if err = os.MkdirAll(filepath.Join(w.Root, ".cache"), 0700); err != nil {
				return nil, err
			}
			if err = os.WriteFile(filepath.Join(w.Root, ".cache", "keep"), []byte("retained output"), 0600); err != nil {
				return nil, err
			}
		}
		switch step {
		case 0:
			call("agent.spawn", map[string]any{"role": "implementer", "assignment": map[string]any{"prompt": "Set Value to 2", "acceptanceChecks": []string{"Value returns 2; tests pass"}}})
		case 1, 6:
			call("agent.wait", map[string]any{"ids": []string{impl.ID}})
		case 2, 7:
			call("agent.spawn", map[string]any{"role": "reviewer", "assignment": map[string]any{"prompt": "Independently review Value against requirements and report"}})
		case 3, 8:
			call("agent.wait", map[string]any{"ids": []string{review.ID}})
		case 4, 9:
			call("agent.integrate", map[string]any{})
		case 5:
			call("agent.followup", map[string]any{"id": impl.ID, "message": "Fix the reviewer finding: Value must return 2"})
		case 10, 12:
			call("agent.complete", map[string]any{})
		case 11:
			w, err := p.e.Store.AgentWorkflow(ctx, task.ID)
			if err != nil {
				return nil, err
			}
			var checks []map[string]any
			for _, c := range w.Checks {
				checks = append(checks, map[string]any{"label": c.Label, "command": c.Command, "required_command": c.RequiredCommand, "directory": c.Directory})
			}
			if p.mode == "subset" {
				checks = checks[:1]
			}
			call("verification.run", map[string]any{"checks": checks})
		}
	}
	ch := make(chan llm.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestDelegationWorkflowEndToEnd(t *testing.T) {
	testDelegationWorkflowEndToEnd(t, "")
}

func TestDelegationWorkflowRequiredChecks(t *testing.T) {
	for _, mode := range []string{"failed", "subset", "mutated", "cleanup"} {
		t.Run(mode, func(t *testing.T) { testDelegationWorkflowEndToEnd(t, mode) })
	}
}

func testDelegationWorkflowEndToEnd(t *testing.T, mode string) {
	goBin, goCache := taskFixtureGoCache(t)
	e, th, root, _ := delegationFixture(t)
	valueTest := "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value()!=2{t.Fatal(Value())}}\n"
	if mode == "failed" {
		valueTest = strings.ReplaceAll(valueTest, "Value()!=2", "Value()!=3")
	}
	for name, data := range map[string]string{"go.mod": "module fixture\ngo 1.24\n", "value.go": "package fixture\nfunc Value()int{return 0}\n", "value_test.go": valueTest, ".gitignore": ".cache/\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "fixture")
	if err := e.Store.UpdateThread(t.Context(), th.ID, map[string]any{"approval_mode": "auto_all"}); err != nil {
		t.Fatal(err)
	}
	p := &workflowProvider{e: e, t: t, calls: map[string]int{}, mode: mode, goBin: goBin, goCache: goCache}
	e.LLMs.Register(p)
	sub := &childSubscriber{make(chan childEvent, 1024)}
	e.Bus.Add(sub)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan protocol.Turn, 1)
	tr, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "Fix the value", DelegationActivationToken: token.ActivationToken}, wait: done})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case turn := <-done:
		wantStatus := protocol.TurnCompleted
		if mode != "" && mode != "cleanup" {
			wantStatus = protocol.TurnFailed
		}
		if turn.Status != wantStatus {
			t.Fatalf("lead turn status=%s want=%s: %+v", turn.Status, wantStatus, turn)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("workflow timeout")
	}
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if (mode == "" || mode == "cleanup") && task.State != protocol.AgentStateCompleted {
		t.Errorf("workflow not complete: %+v", task)
	}
	children, _ := e.Store.AgentChildren(t.Context(), task.ID)
	if len(children) != 3 {
		t.Fatalf("want same implementer and two independent reviewers, got %+v", children)
	}
	impl, err := e.workflowImplementer(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if impl.CorrectionRounds != 1 {
		t.Errorf("correction not delivered to original implementer %+v", impl)
	}
	integration, err := e.Store.AgentIntegration(t.Context(), task.ID, impl.ID)
	if mode != "" && mode != "cleanup" {
		if err != nil || !integration.Applied || integration.Verified || task.State == protocol.AgentStateCompleted {
			t.Fatalf("incomplete checks completed task: %+v %+v %v", task, integration, err)
		}
		w, err := e.Store.AgentWorkflow(t.Context(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := "failed"
		if mode == "subset" || mode == "mutated" {
			want = "passed"
		}
		wantSecond := "not_run"
		if mode == "mutated" {
			wantSecond = "passed"
		}
		if len(w.Checks) != 2 || w.Checks[0].Status != want || w.Checks[1].Status != wantSecond {
			t.Fatalf("required receipts dropped or forged: %+v", w)
		}
		return
	}
	if err != nil || !integration.Applied || !integration.Verified || len(integration.CheckResults) != 2 {
		t.Errorf("lead evidence %+v %v", integration, err)
	}
	if mode == "cleanup" {
		w, err := e.Store.AgentWorkflow(t.Context(), task.ID)
		if err != nil || w.CleanupWarning == "" {
			t.Fatalf("cleanup refusal hidden %+v %v", w, err)
		}
		ws, err := e.Store.AgentWorkspaceForChild(t.Context(), impl.ID, "implementer")
		if err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(filepath.Join(ws.Root, ".cache", "keep")); err != nil || string(b) != "retained output" {
			t.Fatal("ignored artifact deleted", err)
		}
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	integratedThroughPolicy := false
	for _, it := range items {
		if it.Tool != nil && it.Tool.Name == "agent.integrate" && it.Status == protocol.ItemCompleted {
			integratedThroughPolicy = it.Tool.Risk == string(tools.RiskYellow)
		}
	}
	if !integratedThroughPolicy {
		t.Error("integration bypassed normal Yellow action policy")
	}
	if got := delegationGit(t, root, "diff", "--cached", "--", "value.go"); !strings.Contains(got, "return 2") {
		t.Error("corrected implementation was not integrated")
	}
	found := false
	for len(sub.events) > 0 {
		ev := <-sub.events
		if ev.method != protocol.NotifyDelegationUpdated {
			continue
		}
		b, _ := json.Marshal(ev.params)
		var update struct {
			Task     *protocol.AgentTask `json:"task"`
			Workflow *struct {
				Revision string `json:"revision"`
			} `json:"workflow"`
			Child json.RawMessage `json:"child"`
		}
		if err := json.Unmarshal(b, &update); err != nil {
			t.Fatal(err)
		}
		if update.Task != nil && update.Task.State == protocol.AgentStateCompleted {
			found = update.Workflow != nil && update.Workflow.Revision == impl.Revision && len(update.Child) == 0
		}
	}
	if !found {
		t.Error("verified completion did not publish a task-only workflow update")
	}
}

// Use the real Go binary with a permitted temporary build cache. The app's
// sanitized command environment intentionally does not import GOCACHE.
func taskFixtureGoCache(t *testing.T) (string, string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	cache := os.Getenv("GOCACHE")
	if cache == "" || (!strings.HasPrefix(cache, os.TempDir()+string(os.PathSeparator)) && !strings.HasPrefix(cache, "/tmp/")) {
		cache = filepath.Join(os.TempDir(), "umcode-task-fixture-go-cache")
	}
	shim := t.TempDir()
	quote := taskFixtureQuote
	script := "#!/bin/sh\nexport GOCACHE=" + quote(cache) + "\nexec " + quote(goBin) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "go"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	return goBin, cache
}
func taskFixtureQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
