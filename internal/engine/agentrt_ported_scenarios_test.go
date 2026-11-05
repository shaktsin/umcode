package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/sandbox"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
)

// Plan 5 Task 6: the legacy delegation scenarios worth keeping (spec 6,
// phase 5), ported to the agent runtime before the legacy flow is deleted.
// Each test runs the real runtime, scheduler, runner, engine turn loop, tool
// gates, Git workspaces and integration; only the LLM provider, the
// verification tools and the sandbox signals are scripted. Each names the
// legacy tests it replaces.

// portedEvents records what the bus publishes to one chat, and what it
// publishes to admin clients (the app), which is how approvals are announced.
type portedEvents struct {
	thread string
	mu     sync.Mutex
	got    []portedEvent
}

type portedEvent struct {
	method string
	params any
}

func (*portedEvents) ID() string            { return "ported-events" }
func (*portedEvents) IsAdmin() bool         { return true }
func (s *portedEvents) Wants(t string) bool { return t == s.thread }
func (s *portedEvents) Notify(method string, params any) {
	s.mu.Lock()
	s.got = append(s.got, portedEvent{method, params})
	s.mu.Unlock()
}

func (s *portedEvents) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, ev := range s.got {
		out = append(out, ev.method)
	}
	return out
}

// approvalRequest waits for the first approval request published to the chat.
func (s *portedEvents) approvalRequest(t *testing.T) protocol.Approval {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, ev := range s.got {
			if ev.method == protocol.NotifyApprovalRequest {
				a := ev.params.(protocol.ApprovalEvent).Approval
				s.mu.Unlock()
				return a
			}
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no approval request reached the lead chat; saw %v", s.methods())
	return protocol.Approval{}
}

func watchThread(t *testing.T, e *Engine, threadID string) *portedEvents {
	t.Helper()
	ev := &portedEvents{thread: threadID}
	e.Bus.Add(ev)
	t.Cleanup(func() { e.Bus.Remove(ev.ID()) })
	return ev
}

func waitNoActiveTurns(t *testing.T, e *Engine, keep int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		live := len(e.activeTurns)
		e.mu.Unlock()
		if live <= keep {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d turns still active, want at most %d", live, keep)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// spawnWith plays the lead's agent.spawn with extra JSON fields (for example
// a budget share) and returns the new agent's ID or the refusal.
func (x *runtimeE2E) spawnWith(t *testing.T, callID, entry, extra string) (string, error) {
	t.Helper()
	args := `{"name":"` + entry + `","planEntry":"` + entry + `","assignment":{"prompt":"do your part"}` + extra + `}`
	out, err := callAgentTool(t, x.e, x.lead, "agent.spawn", callID, args)
	if err != nil {
		return "", err
	}
	var res struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal([]byte(out), &res); err != nil || res.ID == "" {
		t.Fatalf("spawn %s output %q: %v", entry, out, err)
	}
	return res.ID, nil
}

// TestPortedSequentialImplementThenReview replaces the legacy
// implementer-then-reviewer workflow: TestDelegationWorkflowEndToEnd,
// TestDelegationWorkflowOrderingAndCorrections,
// TestDelegationWorkflowLatestReviewAuthoritative and
// TestDelegationWorkflowRequiredChecks (engine), TestDelegationRPCEndToEnd
// (server) and TestChildExecutionReviewerPolicy. The roles are now plan
// entries: a files:rw writer and a read-only reviewer. The entry names are
// the old role words on purpose: authority comes from the approved
// capabilities only, so the "reviewer" cannot write.
func TestPortedSequentialImplementThenReview(t *testing.T) {
	x := newActivationE2E(t, nil)
	x.setAgent("implementer", fakeStep{Tool: "file.write", Args: `{"path":"a.go","content":` + jsonString(e2eFromA) + `}`}, fakeStep{Text: "implemented: A returns 1"})
	x.setAgent("reviewer",
		fakeStep{Tool: "file.write", Args: `{"path":"a.go","content":"package a\n"}`},
		fakeStep{Tool: "file.read", Args: `{"path":"a.go"}`},
		fakeStep{Text: "approved: A returns 1"},
	)
	plan := `{"agents":[` + planEntry("implementer", "change A", "read-write", "") + `,` + planEntry("reviewer", "review the integrated A", "read", "") + `]}`
	x.approvedFlow(t, plan, nil)

	// Implement first: the writer works in its own checkout on the base.
	impl := x.waitAgent(t, x.spawn(t, "implementer"))
	if impl.Capabilities.Files != protocol.FilesReadWrite || !slices.Equal(impl.Result.ChangedPaths, []string{"a.go"}) {
		t.Fatalf("implementer = %+v", impl)
	}
	if data, _ := os.ReadFile(filepath.Join(x.root, "a.go")); string(data) != e2eA {
		t.Fatalf("the writer changed the project checkout before integration: %q", data)
	}
	if out := x.mustIntegrate(t, `{"action":"accept","agent":"`+impl.ID+`"}`); out.Revision == nil || out.Revision.State != store.RevisionApplied {
		t.Fatalf("accept = %+v", out)
	}
	x.verify(t, "passed", "passed")

	// Then review: the read-only agent sees the integrated head and cannot write.
	rev := x.waitAgent(t, x.spawn(t, "reviewer"))
	if rev.Capabilities.Files != protocol.FilesRead || rev.Capabilities.Shell != protocol.ShellNone {
		t.Fatalf("reviewer granted %+v", rev.Capabilities)
	}
	if got := x.agentToolResult(t, rev.ID, "reviewer-0"); !strings.Contains(got, "not granted by this agent's capabilities") {
		t.Fatalf("reviewer write attempt = %q, want refused by capabilities", got)
	}
	if got := x.agentToolResult(t, rev.ID, "reviewer-1"); !strings.Contains(got, "return 1") {
		t.Fatalf("reviewer read %q, want the integrated change", got)
	}
	if len(rev.Result.ChangedPaths) != 0 || !strings.Contains(rev.Result.Summary, "approved") {
		t.Fatalf("reviewer result = %+v", rev.Result)
	}
	if _, err := x.e.Store.AgentRevision(t.Context(), x.task.ID, rev.ID); err == nil {
		t.Fatal("a read-only reviewer submitted a revision")
	}
	reqs, agents := x.p.snapshot()
	for i, id := range agents {
		if id == rev.ID && slices.Contains(requestToolNames(reqs[i]), "file.write") {
			t.Fatalf("reviewer was offered file.write: %v", requestToolNames(reqs[i]))
		}
	}

	// The lead completes on its own passed checks for the integrated head.
	done := x.mustIntegrate(t, `{"action":"complete"}`)
	if done.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v", done)
	}
	if data, _ := os.ReadFile(filepath.Join(x.root, "a.go")); string(data) != e2eFromA {
		t.Fatalf("project a.go = %q, want the reviewed change", data)
	}
	if st := delegationGit(t, x.root, "status", "--porcelain"); st != "M  a.go" {
		t.Fatalf("status = %q, want only the reviewed change staged", st)
	}
}

// TestPortedCancellationCascadeReturnsBudget replaces the legacy
// cancellation tests: TestCoordinatorActualChildCancellation,
// TestCoordinatorTaskCancellationStopsActualChild,
// TestCoordinatorCancelledPartialResultAndEvents,
// TestChildExecutionFollowupAndCancellation and
// TestDelegationRPCCancelReleasesApprovalAndWait. Cancelling an agent through
// delegation/cancel stops it and its descendants promptly, never starts a
// queued descendant, and returns their budget share to the lead.
func TestPortedCancellationCascadeReturnsBudget(t *testing.T) {
	limits := protocol.DefaultPlanLimits()
	limits.MaxConcurrent = 2
	limits.SliceShare = 1
	read := protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}
	parentCaps := read
	parentCaps.SpawnMax = 2
	x := newRuntimeE2EOpts(t, runtimeE2EOpts{limits: &limits, entries: []protocol.PlanAgent{
		{Name: "parent", Purpose: "split the work", Capabilities: parentCaps},
		{Name: "child", Purpose: "do a part", Capabilities: read},
		{Name: "next", Purpose: "do the rest", Capabilities: read},
	}},
		fakeStep{Agent: "parent", Tool: "agent.spawn", Args: `{"name":"child","planEntry":"child","assignment":{"prompt":"part one"},"budget":{"share":0.5}}`},
		fakeStep{Agent: "parent", Tool: "agent.spawn", Args: `{"name":"queued","planEntry":"child","assignment":{"prompt":"part two"},"budget":{"share":0.25}}`},
		fakeStep{Agent: "parent", Block: true},
		fakeStep{Agent: "child", Block: true},
	)
	parent, err := x.spawnWith(t, "spawn-parent", "parent", `,"budget":{"share":0.75}`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-x.entered:
		case <-time.After(30 * time.Second):
			all, _ := x.e.Store.RuntimeAgents(t.Context(), x.task.ID)
			reqs, ids := x.p.snapshot()
			for i, r := range reqs {
				for _, m := range r.Messages {
					if m.Role == llm.RoleTool {
						t.Logf("agent %s tool result: %s", ids[i], m.Result)
					}
				}
			}
			t.Fatalf("agents never reached the model: %+v", all)
		}
	}
	agents, err := x.e.Store.RuntimeAgents(t.Context(), x.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var child, queued protocol.RuntimeAgent
	for _, a := range agents {
		switch a.Name {
		case "child":
			child = a
		case "queued":
			queued = a
		}
	}
	if child.ParentAgentID != parent || child.Depth != 2 || child.State != protocol.AgentStateRunning {
		t.Fatalf("child = %+v", child)
	}
	if queued.ParentAgentID != parent || queued.State != protocol.AgentStateQueued {
		t.Fatalf("queued descendant = %+v (maxConcurrent 2)", queued)
	}
	// The parent holds 0.75 of the lead's budget: a larger share is refused.
	if _, err = x.spawnWith(t, "spawn-next-early", "next", `,"budget":{"share":0.5}`); err == nil || !strings.Contains(err.Error(), "budget.share") {
		t.Fatalf("spawn over the free share = %v, want a budget.share refusal", err)
	}

	start := time.Now()
	if _, err = x.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: x.th.ID, TaskID: x.task.ID, ChildID: parent}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent, child.ID, queued.ID} {
		waitAgentState(t, x.e, x.task.ID, id, protocol.AgentStateCancelled)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the cascade was not prompt")
	}
	// The lead's live turn is the only turn left; the queued descendant never ran.
	waitNoActiveTurns(t, x.e, 1)
	x.mu.Lock()
	childRuns := x.calls["child"]
	x.mu.Unlock()
	if childRuns != 1 {
		t.Fatalf("child plan entry reached the model %d times, want once (the queued one never ran)", childRuns)
	}
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); task.State != protocol.AgentStateRunning {
		t.Fatalf("cancelling one agent ended the task: %s", task.State)
	}
	// The whole share came back: the lead can spend all of it again.
	if _, err = x.spawnWith(t, "spawn-next", "next", `,"budget":{"share":1}`); err != nil {
		t.Fatalf("spawn at the returned share: %v", err)
	}
}

// TestPortedBudgetStop replaces the legacy child budget coverage: the child
// usage half of TestChildExecutionFinalizationAndHistory (a child's spending
// lands on the shared task budget) and the "Budget stopped" child state the
// flat cards showed (Agents.test.ts). The shared-budget mechanics tests in
// delegation_budget_test.go stay. An agent whose budget slice runs out stops
// with a budget blocker; its spending counts against the shared task budget,
// the task stays active and the lead can still run other agents.
func TestPortedBudgetStop(t *testing.T) {
	limits := protocol.DefaultPlanLimits()
	limits.MaxConcurrent = 2
	use := llm.Usage{InputTokens: 15000, OutputTokens: 5000, Reported: true}
	x := newRuntimeE2EOpts(t, runtimeE2EOpts{
		limits:    &limits,
		configure: func(c *config.Config) { c.Models.ExecutionLimits.MaxTokens = 200000 },
	},
		fakeStep{Agent: "fixer", Tool: "file.write", Args: `{"path":"x.go","content":` + jsonString("package x\n\nfunc F() int { return 3 }\n") + `}`, Usage: use},
		fakeStep{Agent: "fixer", Tool: "file.read", Args: `{"path":"x.go"}`, Usage: use},
		fakeStep{Agent: "fixer", Tool: "file.read", Args: `{"path":"x.go"}`, Usage: use},
		fakeStep{Agent: "fixer", Tool: "file.read", Args: `{"path":"x.go"}`, Usage: use},
		fakeStep{Agent: "fixer", Text: "fixer never gets here", Usage: use},
		fakeStep{Agent: "scout", Text: "scout report"},
	)
	shared := x.e.runtimeTaskBudget(t.Context(), x.task.ID)
	if shared == nil {
		t.Fatal("no shared task budget for the admitted task")
	}
	fixerID, err := x.spawnWith(t, "spawn-fixer", "fixer", `,"budget":{"share":0.25}`)
	if err != nil {
		t.Fatal(err)
	}
	fixer := waitAgentState(t, x.e, x.task.ID, fixerID, protocol.AgentStateFailed)
	if fixer.Result.Blocker != errBudgetSliceExhausted.Error() {
		t.Fatalf("fixer blocker = %q, want %q", fixer.Result.Blocker, errBudgetSliceExhausted)
	}
	x.mu.Lock()
	calls := x.calls["fixer"]
	x.mu.Unlock()
	if calls < 2 || calls >= 5 {
		t.Fatalf("fixer made %d model calls; want it stopped by its slice after a few", calls)
	}
	if fixer.Usage.InputTokens == 0 || shared.Usage().InputTokens < fixer.Usage.InputTokens {
		t.Fatalf("slice spending not on the task budget: agent %+v task %+v", fixer.Usage, shared.Usage())
	}
	if reason := shared.StopReason(); reason != "" {
		t.Fatalf("one agent's slice stopped the whole task: %s", reason)
	}
	// Work done before the stop is retained in the agent's own checkout.
	if data, err := os.ReadFile(filepath.Join(x.workspaceRoot(t, fixerID), "x.go")); err != nil || !strings.Contains(string(data), "return 3") {
		t.Fatalf("fixer checkout x.go = %q %v", data, err)
	}
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); task.State != protocol.AgentStateRunning {
		t.Fatalf("task = %s after one agent's budget stop", task.State)
	}
	scout := x.spawn(t, "scout")
	if a := waitAgentState(t, x.e, x.task.ID, scout, protocol.AgentStateCompleted); !strings.Contains(a.Result.Summary, "scout report") {
		t.Fatalf("scout result = %+v", a.Result)
	}
}

// TestPortedRestartInterruptsAndNeverApplies replaces the legacy restart
// recovery tests: TestReconcileAgentChildrenPreservesArtifacts (store),
// TestDelegationWorkflowRestartDuringApproval,
// TestCoordinatorRestoredDurationAndDetachedAdmission and
// TestChildExecutionFinalizationAndHistory (restart part). After a crash the
// restarted engine marks a running agent interrupted, never runs
// it again, and never applies submitted work by itself.
func TestPortedRestartInterruptsAndNeverApplies(t *testing.T) {
	const fixed = "package x\n\nfunc F() int { return 2 }\n"
	x := newRuntimeE2E(t,
		fakeStep{Agent: "fixer", Tool: "file.write", Args: `{"path":"x.go","content":` + jsonString(fixed) + `}`},
		fakeStep{Agent: "fixer", Text: "fixed"},
		fakeStep{Agent: "scout", Block: true},
	)
	fixer := x.spawn(t, "fixer")
	waitAgentState(t, x.e, x.task.ID, fixer, protocol.AgentStateCompleted)
	if r, err := x.e.Store.AgentRevision(t.Context(), x.task.ID, fixer); err != nil || r.State != store.RevisionSubmitted {
		t.Fatalf("fixer revision = %+v %v", r, err)
	}
	scout := x.spawn(t, "scout")
	select {
	case <-x.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("scout never reached the model")
	}
	before := rtCheckoutState(t, x.parent)

	// The crash: a second engine opens the same store while the rows still
	// say running (the first engine never got to write anything).
	cfg := *x.e.Cfg
	var restartedCalls atomic.Int32
	p := &runtimeScriptProvider{script: func(int, context.Context, llm.Request) []llm.Event {
		restartedCalls.Add(1)
		return textReply("must not run")
	}}
	reg := llm.NewRegistry()
	reg.Register(p)
	restarted, err := New(t.Context(), Options{Config: &cfg, Store: x.e.Store, Secrets: secrets.NewMemoryStore(), LLMs: reg, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { restarted.Shutdown(context.Background()) })

	if a, _ := restarted.Store.RuntimeAgent(t.Context(), x.task.ID, scout); a.State != protocol.AgentStateInterrupted {
		t.Fatalf("running scout after restart = %s, want interrupted", a.State)
	}
	if a, _ := restarted.Store.RuntimeAgent(t.Context(), x.task.ID, fixer); a.State != protocol.AgentStateCompleted {
		t.Fatalf("finished fixer after restart = %s, want completed", a.State)
	}
	// Nothing resumes and nothing is applied: the submitted revision waits
	// for the lead's agent.integrate, and the project checkout is untouched.
	time.Sleep(300 * time.Millisecond)
	if n := restartedCalls.Load(); n != 0 {
		t.Fatalf("the restarted engine called the model %d times", n)
	}
	if a, _ := restarted.Store.RuntimeAgent(t.Context(), x.task.ID, scout); a.State != protocol.AgentStateInterrupted {
		t.Fatalf("interrupted scout became %s", a.State)
	}
	if r, err := restarted.Store.AgentRevision(t.Context(), x.task.ID, fixer); err != nil || r.State != store.RevisionSubmitted {
		t.Fatalf("fixer revision after restart = %+v %v, want still submitted", r, err)
	}
	if after := rtCheckoutState(t, x.parent); after != before {
		t.Fatalf("restart changed the project checkout:\n%s\n---\n%s", before, after)
	}
	if data, _ := os.ReadFile(filepath.Join(x.parent, "x.go")); string(data) != e2eBase {
		t.Fatalf("project x.go = %q, want the base (no auto-apply)", data)
	}
	// The retained work is still in the writer's checkout for the lead.
	if data, err := os.ReadFile(filepath.Join(x.workspaceRoot(t, fixer), "x.go")); err != nil || string(data) != fixed {
		t.Fatalf("fixer checkout x.go = %q %v", data, err)
	}
}

// TestPortedApprovalInsideAgentSurfacesOnThread replaces the legacy child
// approval tests: TestChildExecutionApproval (both the declined and the
// cancelled-while-waiting cases) and the approval half of
// TestDelegationRPCCancelReleasesApprovalAndWait (server). An
// agent's action that needs approval asks in the lead chat, attributed to the
// task and the agent, and the agent waits for it.
func TestPortedApprovalInsideAgentSurfacesOnThread(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		name := "declined"
		if cancel {
			name = "cancelled while waiting"
		}
		t.Run(name, func(t *testing.T) {
			// The shell is granted while the sandbox is there; it goes away
			// when the agent starts, so the next command is unconfined and
			// needs the user's approval (the lead chat's normal mode).
			var lost atomic.Bool
			x := newRuntimeE2E(t,
				fakeStep{Agent: "fixer", Tool: "shell.run", Args: `{"command":"touch made-by-agent"}`},
				fakeStep{Agent: "fixer", Text: "the command was not allowed"},
			)
			x.e.agentSandbox = func() sandbox.Sandbox {
				if lost.Load() {
					return nil
				}
				return fakeSandbox{}
			}
			script := x.p.script
			x.p.script = func(n int, ctx context.Context, req llm.Request) []llm.Event {
				lost.Store(true)
				return script(n, ctx, req)
			}
			events := watchThread(t, x.e, x.th.ID)
			fixer := x.spawn(t, "fixer")
			if a := x.agent(t, fixer); a.Capabilities.Shell != protocol.ShellReadWrite {
				t.Fatalf("fixer granted %+v, want a shell", a.Capabilities)
			}

			approval := events.approvalRequest(t)
			if approval.ThreadID != x.th.ID || approval.AgentTaskID != x.task.ID || approval.AgentChildID != fixer || approval.Tool != "shell.run" {
				t.Fatalf("approval = %+v, want the lead chat, task %s and agent %s", approval, x.task.ID, fixer)
			}
			if stored, err := x.e.Store.GetApproval(t.Context(), approval.ID); err != nil || stored.Status != "pending" || stored.ThreadID != x.th.ID || stored.AgentChildID != fixer {
				t.Fatalf("stored approval = %+v %v", stored, err)
			}
			waitAgentState(t, x.e, x.task.ID, fixer, protocol.AgentStateWaitingApproval)

			if cancel {
				if _, err := x.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: x.th.ID, TaskID: x.task.ID, ChildID: fixer}); err != nil {
					t.Fatal(err)
				}
				waitAgentState(t, x.e, x.task.ID, fixer, protocol.AgentStateCancelled)
				deadline := time.Now().Add(10 * time.Second)
				for {
					stored, err := x.e.Store.GetApproval(t.Context(), approval.ID)
					if err != nil {
						t.Fatal(err)
					}
					if stored.Status != "pending" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("the cancelled agent's approval is still pending")
					}
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				if _, err := x.e.RespondApproval(t.Context(), approval.ID, false, false, "test"); err != nil {
					t.Fatal(err)
				}
				a := waitAgentState(t, x.e, x.task.ID, fixer, protocol.AgentStateCompleted)
				if !strings.Contains(a.Result.Summary, "not allowed") {
					t.Fatalf("fixer result = %+v", a.Result)
				}
			}
			if _, err := os.Stat(filepath.Join(x.workspaceRoot(t, fixer), "made-by-agent")); err == nil {
				t.Fatal("the unapproved command ran")
			}
			// The agent's own transcript stays off the lead chat: only the
			// approval and delegation updates reach it.
			for _, m := range events.methods() {
				if m == protocol.NotifyItemDelta || m == protocol.NotifyItemStarted || m == protocol.NotifyTurnStarted {
					t.Fatalf("agent turn event %s leaked to the lead chat", m)
				}
			}
		})
	}
}
