package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
)

// restart builds a second engine on the same store and data directory, as
// after a crash.
func (env rvEnv) restart(t *testing.T) *Engine {
	t.Helper()
	e, _ := env.restartLogged(t)
	return e
}

// lockedBuffer is a log sink safe for concurrent writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// restartLogged is restart that also returns what the restarted engine logs.
func (env rvEnv) restartLogged(t *testing.T) (*Engine, *lockedBuffer) {
	t.Helper()
	cfg := *env.e.Cfg
	reg := llm.NewRegistry()
	reg.Register(&delegationProvider{})
	logs := &lockedBuffer{}
	e, err := New(t.Context(), Options{Config: &cfg, Store: env.e.Store, Secrets: secrets.NewMemoryStore(), LLMs: reg, DisableMCP: true, DisableScheduler: true,
		Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { e.Shutdown(context.Background()) })
	return e, logs
}

const unsettledWarning = "agent integration left unsettled after restart"

func (env rvEnv) baseTree(t *testing.T) string {
	t.Helper()
	return delegationGit(t, env.root, "rev-parse", env.task.BaseRevision+"^{tree}")
}

func (env rvEnv) parentTree(t *testing.T) string {
	t.Helper()
	return delegationGit(t, env.root, "write-tree")
}

// markApplying leaves the agent's revision as a crash between "intent
// recorded" and "apply finished" does: applying, with the head it was
// applied onto (the head row exists, as IntegrateRuntime records it first).
func (env rvEnv) markApplying(t *testing.T, agentID string) {
	t.Helper()
	ctx, s := t.Context(), env.e.Store
	if err := s.SetAgentIntegrationHead(ctx, store.AgentIntegrationHead{TaskID: env.task.ID, HeadTree: env.baseTree(t)}, 0); err != nil {
		t.Fatal(err)
	}
	r := env.revision(t, agentID)
	r.State, r.TreeBefore = store.RevisionApplying, env.baseTree(t)
	if err := s.UpdateAgentRevision(ctx, r, r.Version); err != nil {
		t.Fatal(err)
	}
}

// stageInParent writes path=data in the project checkout and stages it.
func (env rvEnv) stageInParent(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(env.root, path), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, env.root, "add", path)
}

// recordIntegrationSteps records every integration step hook that fires.
func recordIntegrationSteps(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var steps []string
	t.Cleanup(delegation.SetRuntimeIntegrateHookForTest(func(stage string) {
		mu.Lock()
		steps = append(steps, stage)
		mu.Unlock()
	}))
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(steps)
	}
}

func TestStartupReconcilesApplyingRevisions(t *testing.T) {
	t.Run("parent unchanged", func(t *testing.T) {
		env := runtimeVerificationEnv(t)
		a := env.writer(t, "a", "a.txt", "from a\n")
		env.markApplying(t, a.ID)
		env.restart(t)
		if got := env.revision(t, a.ID); got.State != store.RevisionSubmitted {
			t.Fatalf("revision = %s %q; want submitted (retry allowed)", got.State, got.Reason)
		}
		if tree := env.parentTree(t); tree != env.baseTree(t) {
			t.Fatalf("parent tree %s, want the base tree", tree)
		}
		if h, _ := env.head(t); h.AppliedCount != 0 || h.HeadTree != env.baseTree(t) {
			t.Fatalf("head = %+v", h)
		}
	})
	t.Run("apply finished", func(t *testing.T) {
		env := runtimeVerificationEnv(t)
		a := env.writer(t, "a", "a.txt", "from a\n")
		env.markApplying(t, a.ID)
		// The crash came after git wrote the parent.
		env.stageInParent(t, "a.txt", "from a\n")
		after := env.parentTree(t)
		env.restart(t)
		got := env.revision(t, a.ID)
		if got.State != store.RevisionApplied || got.TreeAfter != after {
			t.Fatalf("revision = %s after=%s; want applied at %s", got.State, got.TreeAfter, after)
		}
		if h, _ := env.head(t); h.AppliedCount != 1 || h.HeadTree != after {
			t.Fatalf("head = %+v; want %s with one applied", h, after)
		}
		if tree := env.parentTree(t); tree != after {
			t.Fatalf("parent tree changed by the reconcile: %s", tree)
		}
	})
	t.Run("neither", func(t *testing.T) {
		env := runtimeVerificationEnv(t)
		a := env.writer(t, "a", "a.txt", "from a\n")
		env.markApplying(t, a.ID)
		env.stageInParent(t, "a.txt", "someone else\n")
		odd := env.parentTree(t)
		// The engine still starts; the revision stays blocked with a reason,
		// and the startup logs it.
		restarted, logs := env.restartLogged(t)
		if out := logs.String(); !strings.Contains(out, unsettledWarning) || !strings.Contains(out, env.task.ID) {
			t.Fatalf("startup did not log the unsettled task:\n%s", out)
		}
		if got := env.revision(t, a.ID); got.State != store.RevisionApplying {
			t.Fatalf("revision = %s; want applying (blocked)", got.State)
		}
		if tree := env.parentTree(t); tree != odd {
			t.Fatalf("parent tree changed by the reconcile: %s", tree)
		}
		err := restarted.DelegationWorkspaceService.ReconcileRuntimeIntegration(t.Context(), env.task.ID)
		if err == nil || !strings.Contains(err.Error(), "blocked in applying") || !strings.Contains(err.Error(), "a.txt") {
			t.Fatalf("reconcile error = %v; want the blocked reason naming a.txt", err)
		}
	})
}

func TestRestartDoesNotAutoResumeIntegration(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	c := env.liveAgent(t, "c", protocol.AgentCapabilities{Files: protocol.FilesReadWrite})
	// a crashed after its intent was recorded, before git touched the parent;
	// b was submitted and never accepted.
	env.markApplying(t, a.ID)
	steps := recordIntegrationSteps(t)
	env.restart(t)
	if got := steps(); len(got) != 0 {
		t.Fatalf("restart ran integration steps %v; it must never apply", got)
	}
	if st := env.parentStatus(t); st != "" {
		t.Fatalf("parent changed by the restart:\n%s", st)
	}
	if tree := env.parentTree(t); tree != env.baseTree(t) {
		t.Fatalf("parent tree %s, want the base tree", tree)
	}
	for _, id := range []string{a.ID, b.ID} {
		if got := env.revision(t, id); got.State != store.RevisionSubmitted {
			t.Fatalf("agent %s revision = %s; want submitted, waiting for the lead", id, got.State)
		}
	}
	if h, _ := env.head(t); h.AppliedCount != 0 {
		t.Fatalf("head = %+v; nothing may be applied", h)
	}
	if got, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, c.ID); err != nil || got.State != protocol.AgentStateInterrupted {
		t.Fatalf("running agent after restart = %s %v; want interrupted", got.State, err)
	}
}

func (env rvEnv) cancel(t *testing.T) protocol.DelegationCancelResult {
	t.Helper()
	res, err := env.e.CancelDelegation(context.Background(), protocol.DelegationCancelParams{ThreadID: env.task.ThreadID, TaskID: env.task.ID})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	return res
}

func (env rvEnv) workspaceKept(t *testing.T, agentID, path, data string) {
	t.Helper()
	ws, err := env.e.Store.AgentWorkspace(t.Context(), env.revision(t, agentID).WorkspaceID)
	if err != nil || ws.Cleaned || ws.Cleaning {
		t.Fatalf("agent %s workspace = %+v %v; want retained", agentID, ws, err)
	}
	if got, err := os.ReadFile(filepath.Join(ws.Root, path)); err != nil || string(got) != data {
		t.Fatalf("agent %s workspace %s = %q %v", agentID, path, got, err)
	}
}

func TestCancelTaskKeepsAppliedRevisionsAndWorkspaces(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	staged, tree := env.parentStatus(t), env.parentTree(t)
	res := env.cancel(t)
	if res.Task.State != protocol.AgentStateCancelled {
		t.Fatalf("task = %s", res.Task.State)
	}
	// Nothing is reverted: a's work stays staged in the parent.
	if st := env.parentStatus(t); st != staged || st != "A  a.txt" {
		t.Fatalf("parent status %q, want %q", st, staged)
	}
	if got := env.parentTree(t); got != tree {
		t.Fatalf("parent tree %s, want %s", got, tree)
	}
	if got := env.revision(t, a.ID); got.State != store.RevisionApplied || got.TreeAfter != tree {
		t.Fatalf("a revision = %s %s", got.State, got.TreeAfter)
	}
	if got := env.revision(t, b.ID); got.State != store.RevisionSubmitted {
		t.Fatalf("b revision = %s", got.State)
	}
	// The unverified state is reported.
	r := res.Integration
	if r == nil {
		t.Fatal("cancel result has no integration report")
	}
	if r.HeadTree != tree || r.AppliedCount != 1 || r.Verified || !slices.Equal(r.Applied, []string{a.ID}) || !slices.Equal(r.Unintegrated, []string{b.ID}) || r.Blocked != "" {
		t.Fatalf("report = %+v", r)
	}
	for _, want := range []string{"not verified", "nothing was reverted", "staged", "retained"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("report message %q lacks %q", r.Message, want)
		}
	}
	env.workspaceKept(t, a.ID, "a.txt", "from a\n")
	env.workspaceKept(t, b.ID, "b.txt", "from b\n")
	// No further accept after the cancel.
	if _, err := env.e.DelegationWorkspaceService.IntegrateRuntime(t.Context(), env.task.ID, b.ID); err == nil {
		t.Fatal("accept after cancel succeeded")
	}
	if st := env.parentStatus(t); st != "A  a.txt" {
		t.Fatalf("parent status after refused accept %q", st)
	}
}

// TestCancelDuringApplyNeverHalfApplies cancels the task while an accept is
// in flight: just before the parent is written (the accept is refused and the
// parent is untouched) and once git is writing (the apply finishes, is
// recorded, and the cancel reports it, after waiting for it).
func TestCancelDuringApplyNeverHalfApplies(t *testing.T) {
	for _, tc := range []struct {
		stage   string
		applied bool
	}{{"guard2", false}, {"passed2", true}} {
		t.Run(tc.stage, func(t *testing.T) {
			env := runtimeVerificationEnv(t)
			a := env.writer(t, "a", "a.txt", "from a\n")
			before := env.parentTree(t)
			type cancelled struct {
				res protocol.DelegationCancelResult
				err error
			}
			done := make(chan cancelled, 1)
			var once sync.Once
			t.Cleanup(delegation.SetRuntimeIntegrateHookForTest(func(stage string) {
				if stage != tc.stage {
					return
				}
				once.Do(func() {
					go func() {
						res, err := env.e.CancelDelegation(context.Background(), protocol.DelegationCancelParams{ThreadID: env.task.ThreadID, TaskID: env.task.ID})
						done <- cancelled{res, err}
					}()
					eventually(t, "task cancelled", func() bool {
						got, err := env.e.Store.AgentTask(context.Background(), env.task.ID)
						return err == nil && got.State == protocol.AgentStateCancelled
					})
				})
			}))
			_, acceptErr := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+a.ID+`"}`)
			var c cancelled
			select {
			case c = <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("cancel did not return")
			}
			if c.err != nil {
				t.Fatalf("cancel: %v", c.err)
			}
			rev, tree, st := env.revision(t, a.ID), env.parentTree(t), env.parentStatus(t)
			r := c.res.Integration
			if r == nil {
				t.Fatal("cancel result has no integration report")
			}
			if !tc.applied {
				if acceptErr == nil {
					t.Fatal("accept succeeded after the task was cancelled before the write")
				}
				if rev.State != store.RevisionSubmitted || tree != before || st != "" {
					t.Fatalf("half applied: revision %s, parent tree %s (before %s), status %q", rev.State, tree, before, st)
				}
				if r.AppliedCount != 0 || len(r.Applied) != 0 || !slices.Equal(r.Unintegrated, []string{a.ID}) {
					t.Fatalf("report = %+v", r)
				}
				return
			}
			// Git had started writing: the apply completes and is recorded.
			if rev.State != store.RevisionApplied || rev.TreeAfter != tree || tree == before || st != "A  a.txt" {
				t.Fatalf("half applied: revision %s after=%s, parent tree %s, status %q", rev.State, rev.TreeAfter, tree, st)
			}
			if h, _ := env.head(t); h.HeadTree != tree || h.AppliedCount != 1 {
				t.Fatalf("head = %+v", h)
			}
			// The cancel waited for the apply and reports it, unverified.
			if r.HeadTree != tree || r.AppliedCount != 1 || r.Verified || !slices.Equal(r.Applied, []string{a.ID}) {
				t.Fatalf("report = %+v", r)
			}
		})
	}
}

// A cancel whose project folder is gone still succeeds and still reports.
func TestCancelTaskWithMissingProjectReports(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	moved := env.root + ".moved"
	if err := os.Rename(env.root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Rename(moved, env.root) })
	res := env.cancel(t)
	if res.Task.State != protocol.AgentStateCancelled {
		t.Fatalf("task = %s", res.Task.State)
	}
	if r := res.Integration; r == nil || r.Blocked == "" || !strings.Contains(r.Message, "nothing was reverted") {
		t.Fatalf("report = %+v", r)
	}
}

func TestStartupRepairsHeadThatMissedItsMove(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	after := env.parentTree(t)
	// A crash between "revision applied" and "head moved".
	h, _ := env.head(t)
	if err := env.e.Store.SetAgentIntegrationHead(t.Context(), store.AgentIntegrationHead{TaskID: env.task.ID, HeadTree: env.baseTree(t)}, h.Version); err != nil {
		t.Fatal(err)
	}
	env.restart(t)
	if h, _ := env.head(t); h.HeadTree != after || h.AppliedCount != 1 {
		t.Fatalf("head = %+v; want %s with one applied", h, after)
	}
	if got := env.revision(t, a.ID); got.State != store.RevisionApplied || got.TreeAfter != after {
		t.Fatalf("revision = %s %s", got.State, got.TreeAfter)
	}
	if tree := env.parentTree(t); tree != after {
		t.Fatalf("parent tree changed: %s", tree)
	}
}

func TestStartupWithMissingProjectFolder(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.markApplying(t, a.ID)
	moved := env.root + ".moved"
	if err := os.Rename(env.root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Rename(moved, env.root) })
	_, logs := env.restartLogged(t)
	if got := env.revision(t, a.ID); got.State != store.RevisionApplying {
		t.Fatalf("revision = %s; want left applying", got.State)
	}
	if out := logs.String(); !strings.Contains(out, unsettledWarning) || !strings.Contains(out, env.task.ID) {
		t.Fatalf("startup did not log the unsettled task:\n%s", out)
	}
}

// A revision blocked in applying may be partly in the checkout: the cancel
// report must say so, list it apart from never-applied work and not offer a
// retry the cancelled task can never run.
func TestCancelTaskWithBlockedApplyReports(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.markApplying(t, a.ID)
	// Git wrote a, then the user edited the staged file before the restart.
	env.stageInParent(t, "a.txt", "someone else\n")
	tree := env.parentTree(t)
	res := env.cancel(t)
	if res.Task.State != protocol.AgentStateCancelled {
		t.Fatalf("task = %s", res.Task.State)
	}
	r := res.Integration
	if r == nil {
		t.Fatal("cancel result has no integration report")
	}
	if r.AppliedCount != 0 || len(r.Applied) != 0 || !slices.Equal(r.BlockedAgents, []string{a.ID}) || !slices.Equal(r.Unintegrated, []string{b.ID}) {
		t.Fatalf("report = %+v", r)
	}
	if !strings.Contains(r.Blocked, "blocked in applying") {
		t.Fatalf("blocked reason = %q", r.Blocked)
	}
	for _, text := range []string{r.Blocked, r.Message} {
		if strings.Contains(text, "retry") || strings.Contains(text, "call resolved") {
			t.Fatalf("offers an action the cancelled task cannot take: %q", text)
		}
	}
	if strings.Contains(r.Message, "did not change") || strings.Contains(r.Message, "No agent work was integrated") {
		t.Fatalf("message claims the checkout is untouched: %q", r.Message)
	}
	for _, want := range []string{"partly integrated", a.ID, "inspect", "nothing was reverted", "retained"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("report message %q lacks %q", r.Message, want)
		}
	}
	if got := env.revision(t, a.ID); got.State != store.RevisionApplying {
		t.Fatalf("revision = %s", got.State)
	}
	if got := env.parentTree(t); got != tree {
		t.Fatalf("parent tree changed: %s", got)
	}
}

// Only a task the cancel actually left cancelled gets a report: cancelling a
// completed task (whose applied checkouts were cleaned up) reports nothing.
func TestCancelCompletedTaskHasNoIntegrationReport(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	if out := env.mustIntegrate(t, `{"action":"complete"}`); out.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v", out)
	}
	res, err := env.e.CancelDelegation(context.Background(), protocol.DelegationCancelParams{ThreadID: env.task.ThreadID, TaskID: env.task.ID})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if res.Task.State != protocol.AgentStateCompleted || res.Integration != nil {
		t.Fatalf("cancel of a completed task = %s %+v", res.Task.State, res.Integration)
	}
}
