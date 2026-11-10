package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// unmarkApplying returns agentID's applying revision to submitted, as the
// reconcile of an apply that never touched the parent does.
func (env rvEnv) unmarkApplying(t *testing.T, agentID string) {
	t.Helper()
	rev := env.revision(t, agentID)
	rev.State = store.RevisionSubmitted
	if err := env.e.Store.UpdateAgentRevision(t.Context(), rev, rev.Version); err != nil {
		t.Fatal(err)
	}
}

// applyingRefusal reports whether err refuses because agentID's revision is
// applying and tells the lead to accept it again (not merely to retry).
func applyingRefusal(err error, agentID string) bool {
	return err != nil && strings.Contains(err.Error(), "an integration is being applied") &&
		strings.Contains(err.Error(), "accept agent "+agentID+" again") && !strings.Contains(err.Error(), "try again")
}

const spawnWorker = `{"planEntry":"worker","assignment":{"prompt":"look"}}`

func TestSpawnDuringApplyingRefused(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.markApplying(t, a.ID)
	_, err := callAgentTool(t, env.e, env.ctx, "agent.spawn", "during-apply", spawnWorker)
	if !applyingRefusal(err, a.ID) {
		t.Fatalf("spawn while applying: %v", err)
	}
	if agents, _ := env.e.Store.RuntimeAgents(t.Context(), env.task.ID); len(agents) != 1 {
		t.Fatalf("a refused spawn created an agent: %d agents", len(agents))
	}
	// The agent's own workspace preparation refuses as well.
	late := env.liveAgent(t, "late", readOnlyCaps)
	if _, err = env.e.DelegationWorkspaceService.PrepareRuntime(t.Context(), env.task, late, false); !applyingRefusal(err, a.ID) {
		t.Fatalf("prepare while applying: %v", err)
	}
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 100000, MaxCostUSD: 100, MaxDurationMinutes: 10}, protocol.UsageTotals{}, nil)
	ctx, cancel := context.WithTimeout(delegation.WithBudget(t.Context(), shared), 20*time.Second)
	defer cancel()
	if _, err = (runtimeRunner{engine: env.e}).Run(ctx, late); !applyingRefusal(err, a.ID) || !strings.Contains(err.Error(), "agent late: workspace not created: ") {
		t.Fatalf("runner while applying: %v", err)
	}
	env.unmarkApplying(t, a.ID)
	if _, err = callAgentTool(t, env.e, env.ctx, "agent.spawn", "after-apply", spawnWorker); err != nil {
		t.Fatalf("spawn once nothing is applying: %v", err)
	}
}

// N3: the spawn gate tells the lead to call resolved again for an
// interrupted manual resolution.
func TestSpawnDuringManualResolutionApplyingRefused(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	rev := env.revision(t, a.ID)
	rev.State, rev.Reason, rev.TreeBefore = store.RevisionApplying, "resolved manually by the lead", env.baseTree(t)
	if err := env.e.Store.UpdateAgentRevision(t.Context(), rev, rev.Version); err != nil {
		t.Fatal(err)
	}
	_, err := callAgentTool(t, env.e, env.ctx, "agent.spawn", "during-resolve", spawnWorker)
	if err == nil || !strings.Contains(err.Error(), `agent.integrate {action:"resolved", agent:"`+a.ID+`"}`) || strings.Contains(err.Error(), "accept agent") {
		t.Fatalf("spawn while a manual resolution is applying: %v", err)
	}
}

// Replaces Plan 3's TestSpawnAfterAcceptExplainsCleanCheckout: spawning after
// the first accept is allowed.
func TestSpawnAfterAcceptAllowed(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	if _, err := callAgentTool(t, env.e, env.ctx, "agent.spawn", "late", spawnWorker); err != nil {
		t.Fatalf("spawn after the first accept: %v", err)
	}
}

// Replaces Plan 3's TestAcceptRefusedWhileAnAgentHasNoWorkspace and
// TestAcceptWaitsForWorkspaceBeingPrepared: an agent without a ready
// workspace no longer blocks the first accept; it gets its workspace later,
// a view of the integrated state for a reader.
func TestAcceptWhileAnAgentHasNoWorkspace(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	waiting := env.liveAgent(t, "waiting", readOnlyCaps) // running, no checkout yet
	if _, err := env.e.Store.EnsureRuntimeAgentExecution(t.Context(), waiting); err != nil {
		t.Fatal(err)
	}
	out := env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	if out.Revision == nil || out.Revision.State != store.RevisionApplied {
		t.Fatalf("accept = %+v", out)
	}
	ws, err := managedChildWorkspaces{service: env.e.DelegationWorkspaceService}.ResolveRuntime(t.Context(), env.task, waiting, false)
	if err != nil {
		t.Fatalf("late reader workspace: %v", err)
	}
	if got := delegationGit(t, ws.Root, "rev-parse", "HEAD^{tree}"); got != out.HeadTree {
		t.Fatalf("late reader tree %s, want head %s", got, out.HeadTree)
	}
	if data, err := os.ReadFile(filepath.Join(ws.Root, "a.txt")); err != nil || string(data) != "from a\n" {
		t.Fatalf("late reader a.txt = %q %v", data, err)
	}
}

func TestWriterAfterAcceptConflictsAreStillRecorded(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	// Spawned after the accept: its workspace is the task base.
	b := env.writer(t, "b", "a.txt", "from b\n")
	before := delegationGit(t, env.root, "write-tree")
	out := env.mustIntegrate(t, `{"action":"accept","agent":"`+b.ID+`"}`)
	if out.Revision == nil || out.Revision.State != store.RevisionConflict || !slices.Equal(out.Options, []string{"drop", "resolved"}) || !strings.HasPrefix(out.Message, "Conflict in a.txt. Nothing was applied.") {
		t.Fatalf("late conflicting writer = %+v", out)
	}
	if after := delegationGit(t, env.root, "write-tree"); after != before {
		t.Fatalf("conflict changed the parent index: %s -> %s", before, after)
	}
	env.mustIntegrate(t, `{"action":"drop","agent":"`+b.ID+`","reason":"conflicts with a"}`)
	// A late writer of another file integrates onto the head.
	c := env.writer(t, "c", "c.txt", "from c\n")
	out = env.mustIntegrate(t, `{"action":"accept","agent":"`+c.ID+`"}`)
	if out.Revision == nil || out.Revision.State != store.RevisionApplied || out.AppliedCount != 2 {
		t.Fatalf("late disjoint writer = %+v", out)
	}
	if st := env.parentStatus(t); !strings.Contains(st, "A  a.txt") || !strings.Contains(st, "A  c.txt") {
		t.Fatalf("parent status = %q", st)
	}
}

// Replaces Plan 3's TestRefusalsAfterApplyNeverSuggestNewAgent: spawning
// stays possible after an apply, so the refusal suggests a new agent.
func TestRefusalsAfterApplySuggestNewAgent(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	c := env.finishingWriter(t, "c", "c.txt", "from c\n")
	env.end(t, c.ID, protocol.AgentStateCancelled)
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	_, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+c.ID+`"}`)
	if err == nil || !strings.Contains(err.Error(), "was cancelled after submitting") || !strings.Contains(err.Error(), "start a new agent") {
		t.Fatalf("accept cancelled agent after an apply: %v", err)
	}
}

// g1Phrases are the Plan 3 texts explaining gap G1.
var g1Phrases = []string{"spawn every agent before the first accept", "before the first accept", "no new agent workspace can be created",
	"no agent workspace can be created", "no new agent can be spawned", "needs a clean checkout of the task base"}

func TestGapGuidanceRemoved(t *testing.T) {
	texts := map[string]string{
		"conflict text":       runtimeConflictText,
		"integrate tool":      tools.NewRuntimeIntegrateTool(nil, nil, nil).Description(),
		"spawn applying text": errRuntimeIntegrationApplying,
	}
	for name, text := range texts {
		for _, phrase := range g1Phrases {
			if strings.Contains(strings.ToLower(text), phrase) {
				t.Errorf("%s still explains G1 (%q): %s", name, phrase, text)
			}
		}
	}
	if !strings.Contains(runtimeConflictText, "works on the task base") {
		t.Errorf("conflict text does not say where a new writer starts: %s", runtimeConflictText)
	}
	if d := tools.NewRuntimeIntegrateTool(nil, nil, nil).Description(); !strings.Contains(d, "sees the integrated state") || !strings.Contains(d, "being applied") {
		t.Errorf("integrate description does not explain spawning after an accept: %s", d)
	}
	// Grep the non-test sources of the packages that carried the texts.
	for _, dir := range []string{".", "../delegation", "../tools"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(string(data))
			for _, phrase := range g1Phrases {
				if strings.Contains(lower, phrase) {
					t.Errorf("%s still contains %q", f, phrase)
				}
			}
		}
	}
}

// Scripted end to end: alpha (writer) is accepted and verified, then a
// read-only reviewer is spawned and reads alpha's file from its view of the
// integrated state.
func TestSpawnReviewerAfterAcceptEndToEnd(t *testing.T) {
	const fromAlpha = "package a\n\nfunc A() int { return 1 }\n"
	e, th, root, _ := delegationFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(twoWritersBase), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "a.go")
	task := childExecutionTask(t, e, th)
	p := &runtimeScriptProvider{}
	e.LLMs.Register(p)
	env := runtimeRunnerEnv{e: e, th: th, parent: root, task: task, p: p}
	env.approvePlan(t, protocol.DefaultPlanLimits(),
		protocol.PlanAgent{Name: "alpha", Purpose: "change A", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellNone}},
		protocol.PlanAgent{Name: "reviewer", Purpose: "review the integrated A", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}})
	planner := &stubVerificationTool{name: "verification.plan", out: rvPlanTwo}
	runner := &stubVerificationTool{name: "verification.run"}
	e.Tools.Add(planner)
	e.Tools.Add(runner)
	x := &runtimeE2E{runtimeRunnerEnv: env, entered: make(chan string, 8), calls: map[string]int{}}
	x.lead = env.agentToolCtx(t, "")
	seen := make(chan string, 1)
	p.script = func(_ int, ctx context.Context, req llm.Request) []llm.Event {
		id, _ := runtimeAgentFrom(ctx)
		a, err := e.Store.RuntimeAgent(context.WithoutCancel(ctx), task.ID, id)
		if err != nil {
			return []llm.Event{{Type: llm.EventError, Err: err}}
		}
		x.mu.Lock()
		idx := x.calls[a.PlanEntry]
		x.calls[a.PlanEntry]++
		x.mu.Unlock()
		switch {
		case a.PlanEntry == "alpha" && idx == 0:
			return toolReply("alpha-0", "file.write", `{"path":"a.go","content":`+jsonString(fromAlpha)+`}`)
		case a.PlanEntry == "reviewer" && idx == 0:
			return toolReply("reviewer-0", "file.read", `{"path":"a.go"}`)
		case a.PlanEntry == "reviewer" && idx == 1:
			raw, _ := json.Marshal(req.Messages)
			select {
			case seen <- string(raw):
			default:
			}
			return textReply("reviewed")
		}
		return textReply(a.PlanEntry + " done")
	}
	wait := func(id string) protocol.RuntimeAgent {
		ctx, cancel := context.WithTimeout(x.lead, 60*time.Second)
		defer cancel()
		if _, err := callAgentTool(t, e, ctx, "agent.wait", "wait-"+id, `{"ids":["`+id+`"],"mode":"all"}`); err != nil {
			t.Fatal(err)
		}
		a := x.agent(t, id)
		if a.State != protocol.AgentStateCompleted {
			t.Fatalf("%s ended %s: %+v", a.Name, a.State, a.Result)
		}
		return a
	}
	call := func(args string) integrateOut {
		raw, err := callAgentTool(t, e, x.lead, "agent.integrate", "e2e-"+args, args)
		if err != nil {
			t.Fatalf("agent.integrate %s: %v", args, err)
		}
		var out integrateOut
		if err = json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	alpha := wait(x.spawn(t, "alpha"))
	out := call(`{"action":"accept","agent":"` + alpha.ID + `"}`)
	if out.Revision == nil || out.Revision.State != store.RevisionApplied {
		t.Fatalf("accept alpha = %+v", out)
	}
	runner.set(runResults("passed", "passed"))
	target, err := e.beginDelegationVerification(x.lead, "verification.run")
	if err != nil || target == nil {
		t.Fatalf("begin verification = %v %v", target, err)
	}
	if err = e.finishDelegationVerification(x.lead, "verification.run", target, runner.out); err != nil {
		t.Fatal(err)
	}
	statusBefore := delegationGit(t, root, "status", "--porcelain")
	headBefore := delegationGit(t, root, "rev-parse", "HEAD")
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	reviewer := wait(x.spawn(t, "reviewer"))
	select {
	case msgs := <-seen:
		if !strings.Contains(msgs, "return 1") {
			t.Fatalf("reviewer did not read alpha's a.go: %s", msgs)
		}
	default:
		t.Fatal("reviewer never got its file.read result")
	}
	w, err := e.Store.AgentWorkspace(t.Context(), reviewer.WorkspaceID)
	if err != nil || w.Kind != "reviewer" || w.SourceID != "runtime_head" || w.ResultTree != out.HeadTree {
		t.Fatalf("reviewer workspace = %+v %v", w, err)
	}
	if head := delegationGit(t, root, "rev-parse", "HEAD"); head != headBefore {
		t.Fatalf("parent HEAD moved")
	}
	if indexAfter, err := os.ReadFile(filepath.Join(root, ".git", "index")); err != nil || string(indexAfter) != string(indexBefore) {
		t.Fatalf("parent index bytes changed by the reviewer: %v", err)
	}
	if st := delegationGit(t, root, "status", "--porcelain"); st != statusBefore || st != "M  a.go" {
		t.Fatalf("parent status %q (before %q)", st, statusBefore)
	}
	if out = call(`{"action":"complete"}`); out.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v", out)
	}
}
