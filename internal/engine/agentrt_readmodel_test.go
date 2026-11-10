package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// newReadModelE2E is newRuntimeE2E with the given plan entries: every agent
// answers "done", except a plan entry named "fixer", which writes x.go first.
func newReadModelE2E(t *testing.T, entries ...protocol.PlanAgent) *runtimeE2E {
	t.Helper()
	e, th, root, _ := delegationFixture(t)
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte(e2eBase), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "x.go")
	task := childExecutionTask(t, e, th)
	p := &runtimeScriptProvider{}
	e.LLMs.Register(p)
	env := runtimeRunnerEnv{e: e, th: th, parent: root, task: task, p: p}
	env.approvePlan(t, protocol.DefaultPlanLimits(), entries...)
	x := &runtimeE2E{runtimeRunnerEnv: env, entered: make(chan string, 8), calls: map[string]int{}}
	x.lead = env.agentToolCtx(t, "")
	p.script = func(_ int, ctx context.Context, _ llm.Request) []llm.Event {
		id, _ := runtimeAgentFrom(ctx)
		a, err := e.Store.RuntimeAgent(context.WithoutCancel(ctx), task.ID, id)
		if err != nil {
			return []llm.Event{{Type: llm.EventError, Err: err}}
		}
		x.mu.Lock()
		idx := x.calls[a.PlanEntry]
		x.calls[a.PlanEntry]++
		x.mu.Unlock()
		if a.PlanEntry == "fixer" && idx == 0 {
			return toolReply("fixer-0", "file.write", `{"path":"x.go","content":`+jsonString("package x\n\nfunc F() int { return 2 }\n")+`}`)
		}
		return textReply("done")
	}
	return x
}

func (x *runtimeE2E) waitAll(t *testing.T, ids ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(x.lead, 60*time.Second)
	defer cancel()
	raw, _ := json.Marshal(ids)
	if _, err := callAgentTool(t, x.e, ctx, "agent.wait", "wait-"+strings.Join(ids, "-"), `{"ids":`+string(raw)+`,"mode":"all"}`); err != nil {
		t.Fatal(err)
	}
}

func (x *runtimeE2E) snapshot(t *testing.T) protocol.AgentSnapshot {
	t.Helper()
	res, err := x.e.ListDelegation(t.Context(), protocol.DelegationListParams{ThreadID: x.th.ID, TaskID: x.task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 1 {
		t.Fatalf("tasks = %+v", res.Tasks)
	}
	return res.Tasks[0]
}

// agentUpdates returns the delegation/updated events for taskID that carry
// a runtime agent, in arrival order.
func (s *recordingSub) agentUpdates(taskID string) []protocol.DelegationUpdated {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.DelegationUpdated
	for _, ev := range s.events {
		if d, ok := ev.params.(protocol.DelegationUpdated); ok && ev.method == protocol.NotifyDelegationUpdated && d.TaskID == taskID && d.Agent != nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *recordingSub) integrationUpdates(taskID string) []protocol.DelegationUpdated {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.DelegationUpdated
	for _, ev := range s.events {
		if d, ok := ev.params.(protocol.DelegationUpdated); ok && ev.method == protocol.NotifyDelegationUpdated && d.TaskID == taskID && d.Integration != nil {
			out = append(out, d)
		}
	}
	return out
}

var readerCaps = protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}

func TestListDelegationRuntimeSnapshot(t *testing.T) {
	x := newReadModelE2E(t,
		protocol.PlanAgent{Name: "scout", Purpose: "read", Capabilities: readerCaps, Model: "claude/deterministic"},
		protocol.PlanAgent{Name: "fixer", Purpose: "fix", Capabilities: writeCaps, Model: "claude/deterministic"},
	)
	scout, fixer := x.spawn(t, "scout"), x.spawn(t, "fixer")
	x.waitAll(t, scout, fixer)

	snap := x.snapshot(t)
	if len(snap.Children) != 0 {
		t.Fatalf("runtime task carries legacy children: %+v", snap.Children)
	}
	if snap.Plan == nil || snap.Plan.State != protocol.PlanApproved || len(snap.Plan.Agents) != 2 {
		t.Fatalf("plan = %+v", snap.Plan)
	}
	if len(snap.Agents) != 2 {
		t.Fatalf("agents = %+v", snap.Agents)
	}
	byID := map[string]protocol.RuntimeAgent{}
	for _, a := range snap.Agents {
		byID[a.ID] = a
		if a.WorkspaceID != "" {
			t.Fatalf("agent %s exposes its workspace ID", a.ID)
		}
		if a.ModelRequested != "claude/deterministic" || a.State != protocol.AgentStateCompleted {
			t.Fatalf("agent = %+v", a)
		}
	}
	if byID[scout].Capabilities != readerCaps || byID[fixer].Capabilities != writeCaps {
		t.Fatalf("capabilities: scout %+v fixer %+v", byID[scout].Capabilities, byID[fixer].Capabilities)
	}
	// The fixer submitted a revision: the integration status shows it.
	if snap.Integration == nil || len(snap.Integration.Revisions) != 1 || snap.Integration.Revisions[0].AgentID != fixer || snap.Integration.Revisions[0].State != store.RevisionSubmitted {
		t.Fatalf("integration = %+v", snap.Integration)
	}
}

func TestRuntimeAgentStateChangePublishes(t *testing.T) {
	x := newReadModelE2E(t, protocol.PlanAgent{Name: "scout", Purpose: "read", Capabilities: readerCaps})
	sub := &recordingSub{}
	x.e.Bus.Add(sub)
	defer x.e.Bus.Remove(sub.ID())
	id := x.spawn(t, "scout")
	x.waitAll(t, id)

	// The completion event is published right after the store write that
	// wakes the wait; give it a moment.
	deadline := time.Now().Add(5 * time.Second)
	var updates []protocol.DelegationUpdated
	for {
		updates = sub.agentUpdates(x.task.ID)
		if n := len(updates); n > 0 && updates[n-1].Agent.State == protocol.AgentStateCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no completed agent update: %+v", updates)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var last int64
	var lastVersion int64
	for _, u := range updates {
		if u.ThreadID != x.th.ID || u.Agent.ID != id {
			t.Fatalf("update = %+v", u)
		}
		if u.Sequence <= last {
			t.Fatalf("sequence not increasing: %d after %d", u.Sequence, last)
		}
		if u.Agent.Version < lastVersion {
			t.Fatalf("agent version went back: %d after %d", u.Agent.Version, lastVersion)
		}
		if u.Agent.WorkspaceID != "" {
			t.Fatal("update exposes the workspace ID")
		}
		last, lastVersion = u.Sequence, u.Agent.Version
	}
}

func TestFallbackIsVisible(t *testing.T) {
	x := newReadModelE2E(t, protocol.PlanAgent{Name: "scout", Purpose: "read", Capabilities: readerCaps, Model: "missing-model"})
	id := x.spawn(t, "scout")
	x.waitAll(t, id)
	snap := x.snapshot(t)
	if len(snap.Agents) != 1 {
		t.Fatalf("agents = %+v", snap.Agents)
	}
	a := snap.Agents[0]
	if a.ModelRequested != "missing-model" || a.ModelActual == "" || a.ModelActual == a.ModelRequested || len(a.RouteTrail) == 0 {
		t.Fatalf("fallback not visible: requested %q actual %q trail %v", a.ModelRequested, a.ModelActual, a.RouteTrail)
	}
}

func TestRuntimeRevisionSettlePublishesIntegration(t *testing.T) {
	x := newReadModelE2E(t, protocol.PlanAgent{Name: "fixer", Purpose: "fix", Capabilities: writeCaps})
	x.e.Tools.Add(&stubVerificationTool{name: "verification.plan", out: rvPlanTwo})
	runner := &stubVerificationTool{name: "verification.run"}
	x.e.Tools.Add(runner)
	sub := &recordingSub{}
	x.e.Bus.Add(sub)
	defer x.e.Bus.Remove(sub.ID())
	id := x.spawn(t, "fixer")
	x.waitAll(t, id)

	hasRevision := func(state string) bool {
		for _, u := range sub.integrationUpdates(x.task.ID) {
			for _, r := range u.Integration.Revisions {
				if r.AgentID == id && r.State == state {
					return true
				}
			}
		}
		return false
	}
	if !hasRevision(store.RevisionSubmitted) {
		t.Fatalf("no integration update for the submitted revision: %+v", sub.integrationUpdates(x.task.ID))
	}
	if _, err := callAgentTool(t, x.e, x.lead, "agent.integrate", "accept", `{"action":"accept","agent":"`+id+`"}`); err != nil {
		t.Fatal(err)
	}
	if !hasRevision(store.RevisionApplied) {
		t.Fatalf("no integration update for the applied revision: %+v", sub.integrationUpdates(x.task.ID))
	}
	updates := sub.integrationUpdates(x.task.ID)
	lastUpdate := updates[len(updates)-1]
	if in := lastUpdate.Integration; in.AppliedCount != 1 || in.HeadTree == "" || in.Verified {
		t.Fatalf("integration after accept = %+v", in)
	}
	if lastUpdate.ThreadID != x.th.ID || lastUpdate.Sequence == 0 {
		t.Fatalf("update = %+v", lastUpdate)
	}
	snap := x.snapshot(t)
	if snap.Integration == nil || snap.Integration.AppliedCount != 1 || snap.Integration.Revisions[0].State != store.RevisionApplied {
		t.Fatalf("snapshot integration = %+v", snap.Integration)
	}

	// Passing checks verify the head: that is announced too.
	runner.set(runResults("passed", "passed"))
	target, err := x.e.beginDelegationVerification(x.lead, "verification.run")
	if err != nil || target == nil {
		t.Fatalf("begin verification = %v %v", target, err)
	}
	if err = x.e.finishDelegationVerification(x.lead, "verification.run", target, runner.out); err != nil {
		t.Fatal(err)
	}
	updates = sub.integrationUpdates(x.task.ID)
	if in := updates[len(updates)-1].Integration; !in.Verified || in.AppliedCount != 1 {
		t.Fatalf("integration after verification = %+v", in)
	}
}

func TestRuntimeCancelPublishesIntegration(t *testing.T) {
	x := newReadModelE2E(t, protocol.PlanAgent{Name: "fixer", Purpose: "fix", Capabilities: writeCaps})
	id := x.spawn(t, "fixer")
	x.waitAll(t, id)
	sub := &recordingSub{}
	x.e.Bus.Add(sub)
	defer x.e.Bus.Remove(sub.ID())
	res, err := x.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: x.th.ID, TaskID: x.task.ID})
	if err != nil || res.Integration == nil {
		t.Fatalf("cancel = %+v %v", res, err)
	}
	updates := sub.integrationUpdates(x.task.ID)
	if len(updates) == 0 {
		t.Fatal("cancel published no integration status")
	}
	in := updates[len(updates)-1].Integration
	if len(in.Revisions) != 1 || in.Revisions[0].AgentID != id || in.Revisions[0].State != store.RevisionSubmitted {
		t.Fatalf("integration after cancel = %+v", in)
	}
}
