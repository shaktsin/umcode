package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// stubVerificationTool replaces a built-in verification tool with a fixed
// result: the engine's bracket must record exactly what the tool returned.
type stubVerificationTool struct {
	name string
	mu   sync.Mutex
	out  string
}

func (s *stubVerificationTool) Name() string            { return s.name }
func (s *stubVerificationTool) Description() string     { return "stub " + s.name }
func (s *stubVerificationTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *stubVerificationTool) Assess(json.RawMessage) (tools.Risk, string) {
	return tools.RiskGreen, "stub"
}
func (s *stubVerificationTool) Call(context.Context, json.RawMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out, nil
}
func (s *stubVerificationTool) set(out string) { s.mu.Lock(); s.out = out; s.mu.Unlock() }

type rvEnv struct {
	e       *Engine
	task    protocol.AgentTask
	root    string
	ctx     context.Context // the lead's admitted tool context
	planner *stubVerificationTool
	runner  *stubVerificationTool
	browser *stubVerificationTool
}

const rvPlanTwo = `{"checks":[{"label":"unit","command":"go test ./...","kind":"test","reason":"go files changed"},{"label":"lint","command":"go vet ./...","kind":"lint","reason":"go files changed"}],"visual_qa":""}`

func runtimeVerificationEnv(t *testing.T) rvEnv {
	t.Helper()
	e, _, _, task, ctx := runtimeAdmitted(t)
	env := rvEnv{e: e, task: task, root: tools.ScopeFrom(ctx).Root, ctx: ctx,
		planner: &stubVerificationTool{name: "verification.plan", out: rvPlanTwo},
		runner:  &stubVerificationTool{name: "verification.run"},
		browser: &stubVerificationTool{name: "browser.verify"}}
	e.Tools.Add(env.planner)
	e.Tools.Add(env.runner)
	e.Tools.Add(env.browser)
	return env
}

// writer runs a completed runtime writer that writes path=data in its own
// checkout and records its revision.
func (env rvEnv) writer(t *testing.T, name, path, data string) protocol.RuntimeAgent {
	t.Helper()
	ctx, s, svc := t.Context(), env.e.Store, env.e.DelegationWorkspaceService
	a, err := s.CreateRuntimeAgent(ctx, protocol.RuntimeAgent{TaskID: env.task.ID, Name: name, Purpose: "edit",
		Assignment: protocol.AgentAssignment{Prompt: "edit"}, Capabilities: protocol.AgentCapabilities{Files: protocol.FilesReadWrite}}, "call-"+name)
	if err != nil {
		t.Fatal(err)
	}
	a.State = protocol.AgentStateRunning
	if err = s.UpdateRuntimeAgent(ctx, a, a.Version); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureRuntimeAgentExecution(ctx, a); err != nil {
		t.Fatal(err)
	}
	ref, err := svc.PrepareRuntime(ctx, env.task, a, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(ref.Root, path), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.SnapshotRuntime(ctx, env.task, a, ref)
	if err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	a.Revision, a.State = snap.ResultRevision, protocol.AgentStateCompleted
	if err = s.UpdateRuntimeAgent(ctx, a, a.Version); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	ws, err := s.AgentWorkspace(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RecordRuntimeRevision(ctx, env.task, a, ws); err != nil {
		t.Fatal(err)
	}
	return a
}

// accept integrates the agent's revision and plans verification for the new
// head, as the lead's accept (Task 5) does.
func (env rvEnv) accept(t *testing.T, agentID string) runtimeWorkflow {
	t.Helper()
	res, err := env.e.DelegationWorkspaceService.IntegrateRuntime(env.ctx, env.task.ID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Options) > 0 {
		t.Fatalf("unexpected conflict: %+v", res)
	}
	w, err := env.e.planRuntimeVerification(env.ctx, env.task)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// run brackets a real (stubbed) built-in call exactly as the turn loop does.
func (env rvEnv) run(ctx context.Context, name string, tool *stubVerificationTool) error {
	target, err := env.e.beginDelegationVerification(ctx, name)
	if err != nil {
		return err
	}
	raw, err := tool.Call(ctx, json.RawMessage(`{}`))
	if err != nil {
		return err
	}
	return env.e.finishDelegationVerification(ctx, name, target, raw)
}

func (env rvEnv) head(t *testing.T) (store.AgentIntegrationHead, runtimeWorkflow) {
	t.Helper()
	h, err := env.e.Store.AgentIntegrationHead(t.Context(), env.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err := store.DecodeRuntimeWorkflow(h.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	return h, w
}

func (env rvEnv) verified(t *testing.T) bool {
	t.Helper()
	st, err := env.e.DelegationWorkspaceService.RuntimeIntegrationStatus(t.Context(), env.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st.Verified
}

func runResults(status ...string) string {
	type r struct {
		Command string `json:"command"`
		Status  string `json:"status"`
		Output  string `json:"output"`
	}
	cmds := []string{"go test ./...", "go vet ./..."}
	var out struct {
		Results []r `json:"results"`
	}
	for i, s := range status {
		out.Results = append(out.Results, r{Command: cmds[i], Status: s, Output: "out " + s})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func checkStatuses(w runtimeWorkflow) []string {
	var s []string
	for _, c := range w.Checks {
		s = append(s, c.Status)
	}
	return s
}

func TestRuntimeVerificationPlansHead(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	w := env.accept(t, a.ID)
	h, stored := env.head(t)
	if w.HeadTree != h.HeadTree || stored.HeadTree != h.HeadTree {
		t.Fatalf("plan not keyed to head %s: returned %s stored %s", h.HeadTree, w.HeadTree, stored.HeadTree)
	}
	if got := strings.Join(checkStatuses(stored), ","); got != "not_run,not_run" {
		t.Fatalf("planned statuses %s", got)
	}
	if env.verified(t) {
		t.Fatal("unrun plan reported verified")
	}
	// Re-planning the same head resets recorded results.
	env.runner.set(runResults("passed", "passed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	if !env.verified(t) {
		t.Fatal("all passed for the head but not verified")
	}
	if _, err := env.e.planRuntimeVerification(env.ctx, env.task); err != nil {
		t.Fatal(err)
	}
	if _, w = env.head(t); strings.Join(checkStatuses(w), ",") != "not_run,not_run" || env.verified(t) {
		t.Fatalf("re-plan did not reset: %+v", w)
	}
}

func TestVerificationResetsWhenHeadChanges(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	first := env.accept(t, a.ID)
	env.runner.set(runResults("passed", "passed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	if !env.verified(t) {
		t.Fatal("first head not verified")
	}
	second := env.accept(t, b.ID)
	if second.HeadTree == first.HeadTree {
		t.Fatal("head did not move")
	}
	h, w := env.head(t)
	if w.HeadTree != h.HeadTree || strings.Join(checkStatuses(w), ",") != "not_run,not_run" {
		t.Fatalf("second accept did not reset checks: head %s workflow %+v", h.HeadTree, w)
	}
	if env.verified(t) {
		t.Fatal("new head inherited the previous verification")
	}
}

func TestVerificationBoundToHeadTree(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.accept(t, a.ID)
	before, _ := env.head(t)
	env.runner.set(runResults("passed", "passed"))

	// Parent changes after begin: finish refuses and records nothing.
	target, err := env.e.beginDelegationVerification(env.ctx, "verification.run")
	if err != nil || target == nil {
		t.Fatalf("begin: %v %v", target, err)
	}
	if err = os.WriteFile(filepath.Join(env.root, "a.txt"), []byte("edited by user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := env.runner.Call(env.ctx, nil)
	if err = env.e.finishDelegationVerification(env.ctx, "verification.run", target, raw); err == nil {
		t.Fatal("result recorded while the parent differs from the head")
	}
	// Begin also refuses while the parent differs.
	if err = env.run(env.ctx, "verification.run", env.runner); err == nil {
		t.Fatal("verification began while the parent differs from the head")
	}
	after, _ := env.head(t)
	if after.Version != before.Version || string(after.Workflow) != string(before.Workflow) {
		t.Fatalf("refused result changed the head: %s -> %s", before.Workflow, after.Workflow)
	}

	// Restore the parent; a result for another head tree is refused too.
	if err = os.WriteFile(filepath.Join(env.root, "a.txt"), []byte("from a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = env.e.recordRuntimeVerification(env.ctx, env.task.ID, before.HeadTree+"0", "verification.run", raw); err == nil {
		t.Fatal("result for another head tree recorded")
	}
	if after, _ = env.head(t); after.Version != before.Version {
		t.Fatal("refused stale result changed the head")
	}
	if err = env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	if !env.verified(t) {
		t.Fatal("matching parent did not record")
	}
}

func TestVerificationIgnoresModelClaims(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.accept(t, a.ID)
	before, _ := env.head(t)
	env.runner.set(runResults("passed", "passed"))

	// A runtime agent (child execution) running verification.run: no receipt.
	child := WithDelegationChild(env.ctx)
	if err := env.run(child, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	agentCtx := withChildExecution(store.WithInternalThreads(WithDelegationChild(env.ctx)), &childExecution{task: env.task})
	if err := env.run(agentCtx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	// Another tool whose output looks like check results: no receipt.
	shell := &stubVerificationTool{name: "shell.run", out: runResults("passed", "passed")}
	if err := env.run(env.ctx, "shell.run", shell); err != nil {
		t.Fatal(err)
	}
	// No bracket (model text): finishing without a begun target records nothing.
	if err := env.e.finishDelegationVerification(env.ctx, "verification.run", nil, runResults("passed", "passed")); err != nil {
		t.Fatal(err)
	}
	if after, _ := env.head(t); after.Version != before.Version || env.verified(t) {
		t.Fatalf("a model or agent claim created a receipt: %s", after.Workflow)
	}
	// The lead's real call records what the tool returned, not what was claimed.
	env.runner.set(runResults("failed", "passed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	if _, w := env.head(t); strings.Join(checkStatuses(w), ",") != "failed,passed" || w.Checks[0].Summary != "out failed" {
		t.Fatalf("lead receipt: %+v", w)
	}
}

func TestVerifiedMeansAllRequiredPassed(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	// Writers are prepared from a clean parent, so before the first accept.
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.accept(t, a.ID)
	for _, statuses := range [][]string{{"failed", "passed"}, {"passed", "blocked"}, {"not_run", "passed"}} {
		env.runner.set(runResults(statuses...))
		if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
			t.Fatal(err)
		}
		if env.verified(t) {
			t.Fatalf("%v reported verified", statuses)
		}
	}
	env.runner.set(runResults("passed", "passed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	if !env.verified(t) {
		t.Fatal("all passed but not verified")
	}

	// Required visual QA is planned blocked and keeps the head unverified.
	env.planner.set(`{"checks":[{"label":"unit","command":"go test ./...","kind":"test","reason":"r"}],"visual_qa":"required: page changed"}`)
	w := env.accept(t, b.ID)
	if len(w.Checks) != 2 || w.Checks[1].Kind != "visual" || w.Checks[1].Status != "blocked" {
		t.Fatalf("visual QA not planned blocked: %+v", w)
	}
	env.runner.set(runResults("passed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	if env.verified(t) {
		t.Fatal("blocked visual QA reported verified")
	}
	// browser.verify only updates browser checks, never the visual one.
	env.browser.set(`{"status":"passed","command":"visual QA"}`)
	if err := env.run(env.ctx, "browser.verify", env.browser); err != nil {
		t.Fatal(err)
	}
	if env.verified(t) {
		t.Fatal("browser result cleared blocked visual QA")
	}
}

// TestRuntimeVerificationRecording pins the shared check recording rules.
func TestRuntimeVerificationRecording(t *testing.T) {
	checks := []store.AgentWorkflowCheck{
		{Command: "go test ./...", Status: "not_run"},
		{Command: "npm run e2e", Kind: "browser", Status: "not_run"},
		{Command: "go test ./...", Directory: "sub", Status: "not_run"},
	}
	long := strings.Repeat("x", 5000)
	raw, _ := json.Marshal(map[string]any{"results": []map[string]string{
		{"command": "go test ./...", "status": "failed", "output": long},
		{"command": "npm run e2e", "status": "passed"},
	}})
	if err := recordVerificationResults(checks, "verification.run", string(raw)); err != nil {
		t.Fatal(err)
	}
	if checks[0].Status != "failed" || len(checks[0].Summary) != 4096 || checks[1].Status != "not_run" || checks[2].Status != "not_run" {
		t.Fatalf("verification.run rules changed: %+v", checks)
	}
	if err := recordVerificationResults(checks, "browser.verify", `{"command":"npm run e2e","status":"blocked","output":"o","reason":"r"}`); err != nil {
		t.Fatal(err)
	}
	if checks[1].Status != "blocked" || checks[1].Summary != "r" {
		t.Fatalf("browser.verify rules changed: %+v", checks[1])
	}
	if err := recordVerificationResults(checks, "browser.verify", `{"command":"npm run e2e","status":"bogus"}`); err == nil {
		t.Fatal("unknown status accepted")
	}

	env := runtimeVerificationEnv(t)
	// Without a runtime integration head no verification is bracketed.
	if target, err := env.e.beginDelegationVerification(env.ctx, "verification.run"); err != nil || target != nil {
		t.Fatalf("unplanned task bracketed: %+v %v", target, err)
	}
}
