package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// runtimeCancelTree runs, all blocked in their provider calls, a parent P
// (spawned by the lead) with its own child C, and a sibling S of P.
func runtimeCancelTree(t *testing.T) (env runtimeRunnerEnv, parent, child, sibling string) {
	t.Helper()
	env = runtimeRunnerFixture(t, runtimeV2On)
	started := make(chan struct{}, 8)
	env.p.script = blockUntilCancelled(started)
	env.approvePlan(t, protocol.DefaultPlanLimits(),
		protocol.PlanAgent{Name: "parent", Purpose: "coordinate", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, SpawnMax: 1}},
		protocol.PlanAgent{Name: "worker", Purpose: "look", Capabilities: readOnlyCaps})
	spawn := func(ctx context.Context, call, args string) string {
		t.Helper()
		out, err := callAgentTool(t, env.e, ctx, "agent.spawn", call, args)
		if err != nil {
			t.Fatal(err)
		}
		var res struct{ ID string }
		if err = json.Unmarshal([]byte(out), &res); err != nil || res.ID == "" {
			t.Fatalf("spawn %s: %q %v", call, out, err)
		}
		return res.ID
	}
	lead := env.agentToolCtx(t, "")
	parent = spawn(lead, "lead-p", `{"name":"P","planEntry":"parent","assignment":{"prompt":"coordinate"}}`)
	sibling = spawn(lead, "lead-s", `{"name":"S","planEntry":"worker","assignment":{"prompt":"look"}}`)
	child = spawn(env.agentToolCtx(t, parent), "p-c", `{"name":"C","planEntry":"worker","assignment":{"prompt":"look"}}`)
	t.Cleanup(func() { _ = env.e.AgentRuntime.Close(context.Background()) })
	eventually(t, "all three agents running", func() bool {
		st := agentStates(t, env.e, env.task.ID)
		return st["P"] == protocol.AgentStateRunning && st["S"] == protocol.AgentStateRunning && st["C"] == protocol.AgentStateRunning
	})
	return env, parent, child, sibling
}

func leadFree(t *testing.T, e *Engine, taskID string) float64 {
	t.Helper()
	all, err := e.Store.RuntimeAgents(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return agentrt.RebuildLedger(all).Free("", 1.0)
}

func TestCancelDelegationRuntimeAgentByID(t *testing.T) {
	env, parent, _, sibling := runtimeCancelTree(t)
	s, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, sibling)
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: env.th.ID, TaskID: env.task.ID, ChildID: parent})
	if err != nil {
		t.Fatalf("cancel runtime agent by id: %v", err)
	}
	if res.Task.ID != env.task.ID || res.Task.State != protocol.AgentStateRunning {
		t.Fatalf("task = %s %s, want the still running task", res.Task.ID, res.Task.State)
	}
	if len(res.Children) != 0 || res.Integration != nil {
		t.Fatalf("children %+v integration %+v, want none for a runtime agent with no revision", res.Children, res.Integration)
	}
	eventually(t, "parent and its child cancelled", func() bool {
		st := agentStates(t, env.e, env.task.ID)
		return st["P"] == protocol.AgentStateCancelled && st["C"] == protocol.AgentStateCancelled
	})
	if st := agentStates(t, env.e, env.task.ID); st["S"] != protocol.AgentStateRunning {
		t.Fatalf("sibling = %s, want untouched (running)", st["S"])
	}
	// The cancelled subtree's shares are free again: only S still holds one.
	if free := leadFree(t, env.e, env.task.ID); math.Abs(free-(1-s.BudgetShare)) > 1e-9 {
		t.Fatalf("lead free share = %v, want %v", free, 1-s.BudgetShare)
	}
	if got, err := env.e.Store.AgentTask(t.Context(), env.task.ID); err != nil || got.State != protocol.AgentStateRunning {
		t.Fatalf("task = %q %v, want running", got.State, err)
	}
}

func TestCancelDelegationRuntimeAgentRefusals(t *testing.T) {
	env, parent, _, _ := runtimeCancelTree(t)
	// Unknown id: the existing not-found error.
	_, err := env.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: env.th.ID, TaskID: env.task.ID, ChildID: "agent-unknown"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id err = %v, want not found", err)
	}
	// A chat that does not own the task is refused before any lookup.
	other, err := env.e.Store.CreateThread(t.Context(), protocol.Thread{Title: "other", ProjectID: env.th.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: other.ID, TaskID: env.task.ID, ChildID: parent}); err == nil {
		t.Fatal("cancel from a chat that does not own the task succeeded")
	}
	// A runtime agent's own context (a child) is refused too.
	if _, err = env.e.CancelDelegation(WithDelegationChild(t.Context()), protocol.DelegationCancelParams{ThreadID: env.th.ID, TaskID: env.task.ID, ChildID: parent}); err == nil {
		t.Fatal("cancel from a delegation child succeeded")
	}
	time.Sleep(50 * time.Millisecond)
	if st := agentStates(t, env.e, env.task.ID); st["P"] != protocol.AgentStateRunning || st["C"] != protocol.AgentStateRunning || st["S"] != protocol.AgentStateRunning {
		t.Fatalf("states after refused cancels = %v, want all running", st)
	}
}

func TestCancelDelegationTerminalRuntimeAgentIsNoOp(t *testing.T) {
	x := newRuntimeE2E(t, fakeStep{Agent: "scout", Text: "scout report"})
	id := x.spawn(t, "scout")
	ctx, cancel := context.WithTimeout(x.lead, 60*time.Second)
	defer cancel()
	if _, err := callAgentTool(t, x.e, ctx, "agent.wait", "wait", `{"ids":["`+id+`"],"mode":"all"}`); err != nil {
		t.Fatal(err)
	}
	before := x.agent(t, id)
	if before.State != protocol.AgentStateCompleted {
		t.Fatalf("scout = %s", before.State)
	}
	for range 2 {
		res, err := x.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: x.th.ID, TaskID: x.task.ID, ChildID: id})
		if err != nil {
			t.Fatalf("cancel of a completed agent: %v", err)
		}
		if res.Task.ID != x.task.ID || res.Task.State != protocol.AgentStateRunning || res.Integration != nil {
			t.Fatalf("result = %+v", res)
		}
	}
	if after := x.agent(t, id); after.State != protocol.AgentStateCompleted || after.Version != before.Version {
		t.Fatalf("agent after no-op cancel = %s v%d, was %s v%d", after.State, after.Version, before.State, before.Version)
	}
}

func TestReadDelegationFileRuntimeAgent(t *testing.T) {
	const fixed = "package x\n\nfunc F() int { return 2 }\n"
	// Workspaces refuse a base with symlinks escaping the checkout, so the
	// snapshot holds only an inner one; symlinks are never read either way.
	x := newRuntimeE2EWith(t, func(root string) {
		if err := os.Symlink("x.go", filepath.Join(root, "in-link")); err != nil {
			t.Fatal(err)
		}
	},
		fakeStep{Agent: "scout", Text: "scout report"},
		fakeStep{Agent: "fixer", Tool: "file.write", Args: `{"path":"x.go","content":` + jsonString(fixed) + `}`},
		fakeStep{Agent: "fixer", Text: "fixed"},
	)
	scoutID, fixerID := x.spawn(t, "scout"), x.spawn(t, "fixer")
	ctx, cancel := context.WithTimeout(x.lead, 60*time.Second)
	defer cancel()
	if _, err := callAgentTool(t, x.e, ctx, "agent.wait", "wait", `{"ids":["`+scoutID+`","`+fixerID+`"],"mode":"all"}`); err != nil {
		t.Fatal(err)
	}
	fixer := x.agent(t, fixerID)
	if fixer.State != protocol.AgentStateCompleted || fixer.Revision == "" || len(fixer.Result.ChangedPaths) != 1 || fixer.Result.ChangedPaths[0] != "x.go" {
		t.Fatalf("fixer = %s rev %q result %+v", fixer.State, fixer.Revision, fixer.Result)
	}
	hostRoots := []string{x.parent, x.workspaceRoot(t, fixerID), filepath.Dir(x.workspaceRoot(t, fixerID))}
	read := func(p protocol.DelegationFileParams) (protocol.ProjectReadFileResult, error) {
		t.Helper()
		got, err := x.e.ReadDelegationFile(t.Context(), p)
		if err != nil {
			for _, root := range hostRoots {
				if strings.Contains(err.Error(), root) {
					t.Fatalf("error exposes a host root %q: %v", root, err)
				}
			}
		}
		return got, err
	}
	base := protocol.DelegationFileParams{ThreadID: x.th.ID, TaskID: x.task.ID, ChildID: fixerID, Revision: fixer.Revision}

	// (e) A changed path of a finished agent, at its recorded revision.
	p := base
	p.Path = "x.go"
	got, err := read(p)
	if err != nil || got.Content != fixed || got.Path != "x.go" || got.Binary || got.Bytes != int64(len(fixed)) {
		t.Fatalf("read x.go = %+v %v", got, err)
	}
	// The project checkout is untouched by the read.
	if data, err := os.ReadFile(filepath.Join(x.parent, "x.go")); err != nil || string(data) != e2eBase {
		t.Fatalf("project x.go = %q %v", data, err)
	}

	// (f) Traversal, absolute, unclean, private and symlink paths are refused.
	for _, bad := range []string{"../x.go", "/etc/passwd", "./x.go", "a/../x.go", "", ".", "..", ".git/config", "in-link", "in-link/../x.go", "missing.go"} {
		p := base
		p.Path = bad
		if got, err := read(p); err == nil {
			t.Fatalf("path %q read: %+v", bad, got)
		}
	}

	// (g) A revision other than the agent's recorded one is refused.
	for _, rev := range []string{"", "HEAD", x.task.BaseRevision, strings.Repeat("0", 40)} {
		p := base
		p.Path, p.Revision = "x.go", rev
		if got, err := read(p); err == nil {
			t.Fatalf("revision %q read: %+v", rev, got)
		}
	}

	// An agent without a revision (read-only), an unknown id and a chat that
	// does not own the task read nothing.
	other, err := x.e.Store.CreateThread(t.Context(), protocol.Thread{Title: "other", ProjectID: x.th.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]protocol.DelegationFileParams{
		"read-only agent": {ThreadID: x.th.ID, TaskID: x.task.ID, ChildID: scoutID, Revision: fixer.Revision, Path: "x.go"},
		"unknown agent":   {ThreadID: x.th.ID, TaskID: x.task.ID, ChildID: "agent-unknown", Revision: fixer.Revision, Path: "x.go"},
		"foreign chat":    {ThreadID: other.ID, TaskID: x.task.ID, ChildID: fixerID, Revision: fixer.Revision, Path: "x.go"},
	} {
		if got, err := read(p); err == nil {
			t.Fatalf("%s read: %+v", name, got)
		}
	}

	// Once the retained workspace is cleaned, the snapshot is unavailable.
	w, err := x.e.Store.AgentWorkspace(t.Context(), fixer.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	w.Cleaned = true
	if err = x.e.Store.SaveAgentWorkspace(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	p = base
	p.Path = "x.go"
	if _, err = read(p); err == nil || !strings.Contains(err.Error(), "snapshot unavailable") {
		t.Fatalf("cleaned workspace read err = %v, want snapshot unavailable", err)
	}
}

// A cancel that settles a revision the agent had already recorded (its
// runner recorded it as it finished, and the cancel won the race) reports the
// changed integration state; the task goes on.
func TestCancelDelegationRuntimeAgentReportsSettledRevision(t *testing.T) {
	env := runtimeRunnerFixture(t, runtimeV2On)
	a := env.newRuntimeAgent(t, "writer", protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellNone}, "")
	if _, err := env.e.Store.SubmitAgentRevision(t.Context(), store.AgentRevision{TaskID: env.task.ID, AgentID: a.ID, WorkspaceID: "ws-writer",
		BaseRevision: env.task.BaseRevision, ResultRevision: strings.Repeat("a", 40), ResultTree: strings.Repeat("b", 40), ChangedPaths: []string{"x.go"}}); err != nil {
		t.Fatal(err)
	}
	res, err := env.e.CancelDelegation(t.Context(), protocol.DelegationCancelParams{ThreadID: env.th.ID, TaskID: env.task.ID, ChildID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, a.ID); err != nil || got.State != protocol.AgentStateCancelled {
		t.Fatalf("writer = %s, want cancelled", got.State)
	}
	if res.Task.State != protocol.AgentStateRunning || res.Integration == nil || len(res.Integration.Unintegrated) != 1 || res.Integration.Unintegrated[0] != a.ID || res.Integration.AppliedCount != 0 {
		t.Fatalf("result task %s integration %+v, want the writer's revision reported unintegrated", res.Task.State, res.Integration)
	}
	if !strings.Contains(res.Integration.Message, "workspaces are retained") {
		t.Fatalf("message = %q", res.Integration.Message)
	}
}
