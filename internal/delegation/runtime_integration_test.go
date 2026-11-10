package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
)

// rtAgent is a stopped runtime agent and the workspace it worked in.
type rtAgent struct {
	agent protocol.RuntimeAgent
	ws    store.AgentWorkspace
}

func rtFixture(t *testing.T) (*WorkspaceService, *store.Store, protocol.AgentTask) {
	return rtFixtureFiles(t, nil)
}

// rtFixtureFiles is workspaceFixture (without a legacy child) with extra
// files in the base commit.
func rtFixtureFiles(t *testing.T, files map[string]string) (*WorkspaceService, *store.Store, protocol.AgentTask) {
	t.Helper()
	if len(files) == 0 {
		svc, s, task, _ := workspaceFixture(t)
		return svc, s, task
	}
	root := t.TempDir()
	workspaceGit(t, root, "init", "-q")
	workspaceGit(t, root, "config", "user.name", "Test")
	workspaceGit(t, root, "config", "user.email", "test@invalid")
	workspaceWrite(t, root, "tracked", "base\n")
	workspaceWrite(t, root, "removed", "remove\n")
	for name, data := range files {
		workspaceWrite(t, root, name, data)
	}
	workspaceGit(t, root, "add", ".")
	workspaceGit(t, root, "commit", "-qm", "base")
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	tr := protocol.Turn{ID: "root", ThreadID: th.ID, Status: protocol.TurnCompleted, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), tr); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "task", ThreadID: th.ID, RootTurnID: tr.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRoot: root, ParentRoot: root, BaseRevision: workspaceGit(t, root, "rev-parse", "HEAD")}
	if err = s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	return NewWorkspaceService(s, worktree.New(t.TempDir()), nil, nil), s, task
}

// rtRun creates a runtime agent, gives it its checkout, lets edit change it,
// snapshots it and moves the agent to final.
func rtRun(t *testing.T, svc *WorkspaceService, s *store.Store, task protocol.AgentTask, name string, files protocol.FileAccess, final protocol.AgentState, edit func(root string)) rtAgent {
	t.Helper()
	ctx := t.Context()
	a, err := s.CreateRuntimeAgent(ctx, protocol.RuntimeAgent{TaskID: task.ID, Name: name, Purpose: "edit",
		Assignment: protocol.AgentAssignment{Prompt: "edit"}, Capabilities: protocol.AgentCapabilities{Files: files}}, "call-"+name)
	if err != nil {
		t.Fatal(err)
	}
	a.State = protocol.AgentStateRunning
	if err = s.UpdateRuntimeAgent(ctx, a, a.Version); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureRuntimeAgentExecution(ctx, a); err != nil {
		t.Fatal(err)
	}
	ref, err := svc.PrepareRuntime(ctx, task, a, files == protocol.FilesReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(ref.Root)
	}
	if a, err = s.RuntimeAgent(ctx, task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.SnapshotRuntime(ctx, task, a, ref)
	if err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	a.Revision = snap.ResultRevision
	a.State = final
	if err = s.UpdateRuntimeAgent(ctx, a, a.Version); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	ws, err := s.AgentWorkspace(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rtAgent{agent: a, ws: ws}
}

// rtWriter is a completed writer whose edit is recorded as a revision.
func rtWriter(t *testing.T, svc *WorkspaceService, s *store.Store, task protocol.AgentTask, name string, edit func(root string)) rtAgent {
	t.Helper()
	w := rtRun(t, svc, s, task, name, protocol.FilesReadWrite, protocol.AgentStateCompleted, edit)
	r, err := svc.RecordRuntimeRevision(t.Context(), task, w.agent, w.ws)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != store.RevisionSubmitted {
		t.Fatalf("revision state %q, want submitted", r.State)
	}
	return w
}

func rtWrite(t *testing.T, path, data string) func(string) {
	return func(root string) { workspaceWrite(t, root, path, data) }
}

// rtVerify records passing checks for the current head tree (Task 4's job).
func rtVerify(t *testing.T, s *store.Store, taskID string) {
	t.Helper()
	h, err := s.AgentIntegrationHead(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	h.Workflow, err = json.Marshal(store.RuntimeWorkflow{HeadTree: h.HeadTree, Checks: []store.AgentWorkflowCheck{{Label: "tests", Command: "go test ./...", Status: "passed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAgentIntegrationHead(t.Context(), h, h.Version); err != nil {
		t.Fatal(err)
	}
}

func rtRevision(t *testing.T, s *store.Store, taskID, agentID string) store.AgentRevision {
	t.Helper()
	r, err := s.AgentRevision(t.Context(), taskID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// rtParent fingerprints the parent: HEAD, index tree, status and file bytes.
func rtParent(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(workspaceGit(t, root, "rev-parse", "HEAD") + "\n")
	b.WriteString(workspaceGit(t, root, "write-tree") + "\n")
	b.WriteString(workspaceGit(t, root, "status", "--porcelain", "--untracked-files=all") + "\n")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == ".git" || e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(e.Name() + "=" + string(data) + "\n")
	}
	return b.String()
}

func TestRecordRuntimeRevisionOnlyForCompletedWriter(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	w := rtRun(t, svc, s, task, "writer", protocol.FilesReadWrite, protocol.AgentStateCompleted, func(root string) {
		workspaceWrite(t, root, "tracked", "writer\n")
		if err := os.Remove(filepath.Join(root, "removed")); err != nil {
			t.Fatal(err)
		}
		workspaceWrite(t, root, "added", "new\n")
	})
	r, err := svc.RecordRuntimeRevision(ctx, task, w.agent, w.ws)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != store.RevisionSubmitted || r.AgentID != w.agent.ID || r.WorkspaceID != w.ws.ID || r.BaseRevision != task.BaseRevision || r.ResultRevision != w.agent.Revision {
		t.Fatalf("revision = %+v", r)
	}
	if want := workspaceGit(t, w.ws.Root, "rev-parse", w.agent.Revision+"^{tree}"); r.ResultTree != want {
		t.Fatalf("result tree %s, want %s", r.ResultTree, want)
	}
	if !reflect.DeepEqual(r.ChangedPaths, []string{"added", "removed", "tracked"}) {
		t.Fatalf("changed paths %v", r.ChangedPaths)
	}
	again, err := svc.RecordRuntimeRevision(ctx, task, w.agent, w.ws)
	if err != nil || again.ID != r.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}

	reader := rtRun(t, svc, s, task, "reader", protocol.FilesRead, protocol.AgentStateCompleted, nil)
	cancelled := rtRun(t, svc, s, task, "cancelled", protocol.FilesReadWrite, protocol.AgentStateCancelled, rtWrite(t, "tracked", "partial\n"))
	unchanged := rtRun(t, svc, s, task, "unchanged", protocol.FilesReadWrite, protocol.AgentStateCompleted, nil)
	for _, a := range []rtAgent{reader, cancelled, unchanged} {
		got, err := svc.RecordRuntimeRevision(ctx, task, a.agent, a.ws)
		if err != nil || got.ID != "" {
			t.Fatalf("%s: recorded %+v, %v", a.agent.Name, got, err)
		}
		if _, err = s.AgentRevision(ctx, task.ID, a.agent.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: revision row exists: %v", a.agent.Name, err)
		}
	}
	if _, err = os.Stat(cancelled.ws.Root); err != nil {
		t.Fatalf("cancelled writer workspace not retained: %v", err)
	}

	// The agent checkout's committed result is authoritative only if it still
	// matches the recorded workspace evidence.
	tampered := rtRun(t, svc, s, task, "tampered", protocol.FilesReadWrite, protocol.AgentStateCompleted, rtWrite(t, "tracked", "x\n"))
	tampered.ws.ResultTree = strings.Repeat("0", 40)
	if err = s.SaveAgentWorkspace(ctx, tampered.ws); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RecordRuntimeRevision(ctx, task, tampered.agent, tampered.ws); err == nil {
		t.Fatal("recorded a revision whose tree differs from the workspace evidence")
	}
}

func TestIntegrateRuntimeSequential(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	b := rtWriter(t, svc, s, task, "b", func(root string) {
		workspaceWrite(t, root, "added", "from b\n")
		if err := os.Remove(filepath.Join(root, "removed")); err != nil {
			t.Fatal(err)
		}
	})
	base := workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD")
	st, err := svc.RuntimeIntegrationStatus(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.HeadTree != workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD^{tree}") || st.AppliedCount != 0 || st.Verified || st.Settled || len(st.Revisions) != 2 {
		t.Fatalf("initial status %+v", st)
	}
	if err = svc.ValidateRuntimeHead(ctx, task.ID, st.HeadTree); err != nil {
		t.Fatal(err)
	}

	first, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.State != store.RevisionApplied || !first.NeedsVerification || len(first.Options) != 0 || first.Revision.TreeBefore != st.HeadTree || first.HeadTree != first.Revision.TreeAfter {
		t.Fatalf("first = %+v", first)
	}
	if got := workspaceGit(t, task.ParentRoot, "write-tree"); got != first.HeadTree {
		t.Fatalf("parent tree %s, head %s", got, first.HeadTree)
	}
	if err = svc.ValidateRuntimeHead(ctx, task.ID, first.HeadTree); err != nil {
		t.Fatal(err)
	}
	if err = svc.ValidateRuntimeHead(ctx, task.ID, st.HeadTree); err == nil {
		t.Fatal("stale head validated")
	}

	t.Run("TestIntegrateRequiresPreviousVerified", func(t *testing.T) {
		before := rtParent(t, task.ParentRoot)
		if _, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID); err == nil || !strings.Contains(err.Error(), "verified") {
			t.Fatalf("unverified head allowed next accept: %v", err)
		}
		if rtParent(t, task.ParentRoot) != before || rtRevision(t, s, task.ID, b.agent.ID).State != store.RevisionSubmitted {
			t.Fatal("refused accept changed state")
		}
		// Checks recorded for another tree do not count.
		h, _ := s.AgentIntegrationHead(ctx, task.ID)
		h.Workflow, _ = json.Marshal(store.RuntimeWorkflow{HeadTree: st.HeadTree, Checks: []store.AgentWorkflowCheck{{Label: "t", Command: "t", Status: "passed"}}})
		if err := s.SetAgentIntegrationHead(ctx, h, h.Version); err != nil {
			t.Fatal(err)
		}
		if st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); st.Verified {
			t.Fatal("checks of another tree verified the head")
		}
		if _, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID); err == nil {
			t.Fatal("checks of another tree allowed next accept")
		}
	})

	rtVerify(t, s, task.ID)
	if st, _ = svc.RuntimeIntegrationStatus(ctx, task.ID); !st.Verified || st.AppliedCount != 1 {
		t.Fatalf("verified status %+v", st)
	}
	second, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision.State != store.RevisionApplied || second.Revision.TreeBefore != first.HeadTree || !second.NeedsVerification {
		t.Fatalf("second = %+v", second)
	}
	// The head equals a manual merge of both revisions on the base.
	manual := t.TempDir()
	workspaceGit(t, manual, "clone", "-q", task.ParentRoot, ".")
	workspaceGit(t, manual, "checkout", "-q", base)
	workspaceWrite(t, manual, "tracked", "from a\n")
	workspaceWrite(t, manual, "added", "from b\n")
	workspaceGit(t, manual, "rm", "-q", "removed")
	workspaceGit(t, manual, "add", "-A")
	if want := workspaceGit(t, manual, "write-tree"); second.HeadTree != want {
		t.Fatalf("head %s, manual merge %s", second.HeadTree, want)
	}
	if workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD") != base {
		t.Fatal("parent HEAD moved")
	}
	if other := workspaceGit(t, task.ParentRoot, "ls-files", "--others", "--exclude-standard"); other != "" {
		t.Fatalf("untracked files %q", other)
	}
	st, err = svc.RuntimeIntegrationStatus(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.HeadTree != second.HeadTree || st.AppliedCount != 2 || st.Verified || !st.Settled {
		t.Fatalf("final status %+v", st)
	}
}

func TestIntegrateRuntimeConflictLeavesBothIntact(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	b := rtWriter(t, svc, s, task, "b", rtWrite(t, "tracked", "from b\n"))
	c := rtWriter(t, svc, s, task, "c", rtWrite(t, "added", "from c\n"))
	if _, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	before := rtParent(t, task.ParentRoot)
	got, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision.State != store.RevisionConflict || !reflect.DeepEqual(got.Revision.Conflict, []string{"tracked"}) || !reflect.DeepEqual(got.Options, []string{"drop", "resolved"}) {
		t.Fatalf("conflict = %+v", got)
	}
	if rtParent(t, task.ParentRoot) != before {
		t.Fatal("conflict changed the parent")
	}
	if r := rtRevision(t, s, task.ID, b.agent.ID); r.State != store.RevisionConflict || !reflect.DeepEqual(r.Conflict, []string{"tracked"}) {
		t.Fatalf("stored conflict %+v", r)
	}
	if data, err := os.ReadFile(filepath.Join(b.ws.Root, "tracked")); err != nil || string(data) != "from b\n" {
		t.Fatalf("conflicting workspace not retained: %q %v", data, err)
	}
	if st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); st.Settled {
		t.Fatal("conflict reported settled")
	}
	// An open conflict blocks other accepts until the lead decides.
	if _, err = svc.IntegrateRuntime(ctx, task.ID, c.agent.ID); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("accept during open conflict: %v", err)
	}
	if rtParent(t, task.ParentRoot) != before {
		t.Fatal("blocked accept changed the parent")
	}
	if _, err = svc.DropRuntime(ctx, task.ID, b.agent.ID, "superseded by a"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.IntegrateRuntime(ctx, task.ID, c.agent.ID); err != nil {
		t.Fatalf("drop did not clear the block: %v", err)
	}
	if st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); !st.Settled || st.AppliedCount != 2 {
		t.Fatalf("status %+v", st)
	}
}

func TestIntegrateRuntimeIdempotentReplay(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	first, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent := rtParent(t, task.ParentRoot)
	again, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("replay differs:\n%+v\n%+v", first, again)
	}
	if rtParent(t, task.ParentRoot) != parent {
		t.Fatal("replay touched the parent")
	}
	if st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); st.AppliedCount != 1 {
		t.Fatalf("applied %d times", st.AppliedCount)
	}
}

func TestIntegrateRuntimeParentEditedRefusal(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	b := rtWriter(t, svc, s, task, "b", rtWrite(t, "added", "from b\n"))
	if _, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	workspaceWrite(t, task.ParentRoot, "removed", "user edit\n")
	before := rtParent(t, task.ParentRoot)
	revBefore := rtRevision(t, s, task.ID, b.agent.ID)
	_, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID)
	if err == nil || !errors.Is(err, worktree.ErrParentChanged) || !strings.Contains(err.Error(), "parent changed") || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("parent edit not refused clearly: %v", err)
	}
	if rtParent(t, task.ParentRoot) != before {
		t.Fatal("refused accept changed the parent")
	}
	if r := rtRevision(t, s, task.ID, b.agent.ID); r.State != store.RevisionSubmitted || r.Version != revBefore.Version {
		t.Fatalf("refused accept changed the revision: %+v", r)
	}
}

func TestReconcileRuntimeIntegration(t *testing.T) {
	cases := []struct {
		name   string
		parent func(t *testing.T, svc *WorkspaceService, task protocol.AgentTask, w rtAgent, head string)
		want   string
	}{
		{"parent tree is before", func(*testing.T, *WorkspaceService, protocol.AgentTask, rtAgent, string) {}, store.RevisionSubmitted},
		{"parent tree is after", func(t *testing.T, svc *WorkspaceService, task protocol.AgentTask, w rtAgent, head string) {
			// The apply finished but the process died before recording it.
			if _, err := svc.Manager.ApplyOnIntegrated(t.Context(), w.ws.ID, task.ParentRoot, task.BaseRevision, head, w.agent.Revision); err != nil {
				t.Fatal(err)
			}
		}, store.RevisionApplied},
		{"parent tree is neither", func(t *testing.T, _ *WorkspaceService, task protocol.AgentTask, _ rtAgent, _ string) {
			workspaceWrite(t, task.ParentRoot, "unrelated", "x\n")
			workspaceGit(t, task.ParentRoot, "add", "unrelated")
		}, store.RevisionApplying},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, s, task := rtFixture(t)
			ctx := t.Context()
			w := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
			st, err := svc.RuntimeIntegrationStatus(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			r := rtRevision(t, s, task.ID, w.agent.ID)
			r.State, r.TreeBefore = store.RevisionApplying, st.HeadTree
			if err = s.UpdateAgentRevision(ctx, r, r.Version); err != nil {
				t.Fatal(err)
			}
			tc.parent(t, svc, task, w, st.HeadTree)
			err = svc.ReconcileRuntimeIntegration(ctx, task.ID)
			got := rtRevision(t, s, task.ID, w.agent.ID)
			if got.State != tc.want {
				t.Fatalf("state %q, want %q (err %v)", got.State, tc.want, err)
			}
			switch tc.want {
			case store.RevisionSubmitted:
				if err != nil {
					t.Fatal(err)
				}
				if _, err = svc.IntegrateRuntime(ctx, task.ID, w.agent.ID); err != nil {
					t.Fatalf("retry after reconcile: %v", err)
				}
			case store.RevisionApplied:
				if err != nil {
					t.Fatal(err)
				}
				tree := workspaceGit(t, task.ParentRoot, "write-tree")
				st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
				if got.TreeAfter != tree || st.HeadTree != tree || st.AppliedCount != 1 {
					t.Fatalf("reconciled to %+v / %+v", got, st)
				}
				// Never double-applied.
				if _, err = svc.IntegrateRuntime(ctx, task.ID, w.agent.ID); err != nil || workspaceGit(t, task.ParentRoot, "write-tree") != tree {
					t.Fatalf("replay after reconcile: %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "unrelated") {
					t.Fatalf("reconcile did not block naming the mismatch: %v", err)
				}
				if tree := workspaceGit(t, task.ParentRoot, "write-tree"); !strings.Contains(err.Error(), tree) || !strings.Contains(err.Error(), st.HeadTree) {
					t.Fatalf("blocking error does not name both trees: %v", err)
				}
				if _, err = svc.IntegrateRuntime(ctx, task.ID, w.agent.ID); err == nil || !strings.Contains(err.Error(), "unrelated") {
					t.Fatalf("integrate did not block naming the mismatch: %v", err)
				}
				// The restore the error suggests works for a staged-only parent.
				workspaceGit(t, task.ParentRoot, "read-tree", "-m", "-u", st.HeadTree)
				if err = svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
					t.Fatal(err)
				}
				if got := rtRevision(t, s, task.ID, w.agent.ID); got.State != store.RevisionSubmitted {
					t.Fatalf("after restore: %+v", got)
				}
				if _, statErr := os.Stat(filepath.Join(task.ParentRoot, "unrelated")); !os.IsNotExist(statErr) {
					t.Fatal("restore left the unrelated file")
				}
				if _, err = svc.IntegrateRuntime(ctx, task.ID, w.agent.ID); err != nil {
					t.Fatalf("retry after restore: %v", err)
				}
			}
		})
	}
}

func TestDropRuntimeIsExplicitAndReasoned(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
	before := rtParent(t, task.ParentRoot)
	if _, err := svc.DropRuntime(ctx, task.ID, a.agent.ID, "  "); err == nil {
		t.Fatal("drop without reason accepted")
	}
	r, err := svc.DropRuntime(ctx, task.ID, a.agent.ID, "not needed")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != store.RevisionDropped || r.Reason != "not needed" {
		t.Fatalf("dropped = %+v", r)
	}
	if again, err := svc.DropRuntime(ctx, task.ID, a.agent.ID, "not needed"); err != nil || again.ID != r.ID {
		t.Fatalf("drop replay: %+v %v", again, err)
	}
	if _, err = svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err == nil {
		t.Fatal("dropped revision applied")
	}
	after, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
	if rtParent(t, task.ParentRoot) != before || after.HeadTree != st.HeadTree || after.AppliedCount != 0 || !after.Settled {
		t.Fatalf("drop changed head or parent: %+v", after)
	}
	if _, err = os.Stat(a.ws.Root); err != nil {
		t.Fatal("dropped workspace not retained")
	}
}

func testResolveManuallyRecordsCurrentTreeAsHead(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	b := rtWriter(t, svc, s, task, "b", rtWrite(t, "tracked", "from b\n"))
	if _, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	if got, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID); err != nil || got.Revision.State != store.RevisionConflict {
		t.Fatalf("expected conflict: %+v %v", got, err)
	}
	// Unstaged fix: refused.
	workspaceWrite(t, task.ParentRoot, "tracked", "from a and b\n")
	if _, err := svc.ResolveRuntimeManually(ctx, task.ID, b.agent.ID); err == nil {
		t.Fatal("unstaged resolution accepted")
	}
	workspaceGit(t, task.ParentRoot, "add", "tracked")
	// Untracked file: refused.
	workspaceWrite(t, task.ParentRoot, "stray", "x\n")
	if _, err := svc.ResolveRuntimeManually(ctx, task.ID, b.agent.ID); err == nil {
		t.Fatal("resolution with untracked files accepted")
	}
	if err := os.Remove(filepath.Join(task.ParentRoot, "stray")); err != nil {
		t.Fatal(err)
	}
	// HEAD moved away from the base: refused.
	workspaceGit(t, task.ParentRoot, "commit", "-qm", "lead commit")
	if _, err := svc.ResolveRuntimeManually(ctx, task.ID, b.agent.ID); err == nil {
		t.Fatal("resolution with a moved HEAD accepted")
	}
	workspaceGit(t, task.ParentRoot, "reset", "-q", "--soft", task.BaseRevision)
	if r := rtRevision(t, s, task.ID, b.agent.ID); r.State != store.RevisionConflict {
		t.Fatalf("refused resolutions changed the revision: %+v", r)
	}
	got, err := svc.ResolveRuntimeManually(ctx, task.ID, b.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	tree := workspaceGit(t, task.ParentRoot, "write-tree")
	if got.HeadTree != tree || !got.NeedsVerification || got.Revision.State != store.RevisionApplied || got.Revision.TreeAfter != tree || len(got.Options) != 0 {
		t.Fatalf("resolved = %+v", got)
	}
	st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
	if st.HeadTree != tree || st.Verified || !st.Settled || st.AppliedCount != 2 {
		t.Fatalf("status %+v", st)
	}
	if err = svc.ValidateRuntimeHead(ctx, task.ID, tree); err != nil {
		t.Fatal(err)
	}
	if workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD") != task.BaseRevision {
		t.Fatal("parent HEAD moved")
	}
}

func testIntegrationRefusesWhenTaskCancelled(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	before := rtParent(t, task.ParentRoot)
	if err := s.UpdateAgentTaskState(ctx, task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err == nil {
		t.Fatal("cancelled task integrated")
	}
	if _, err := svc.DropRuntime(ctx, task.ID, a.agent.ID, "x"); err == nil {
		t.Fatal("cancelled task dropped")
	}
	if rtParent(t, task.ParentRoot) != before || rtRevision(t, s, task.ID, a.agent.ID).State != store.RevisionSubmitted {
		t.Fatal("cancelled task changed state")
	}
}

func testIntegrationUsesAuthorityRecheck(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	before := rtParent(t, task.ParentRoot)
	setRuntimeIntegrateHook(func(stage string) {
		if stage == "intent" {
			if err := s.UpdateThread(ctx, task.ThreadID, map[string]any{"workspace_mode": "worktree"}); err != nil {
				t.Error(err)
			}
		}
	})
	t.Cleanup(func() { setRuntimeIntegrateHook(nil) })
	if _, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err == nil || !strings.Contains(err.Error(), "parent checkout changed") {
		t.Fatalf("authority change not caught by the guard: %v", err)
	}
	if rtParent(t, task.ParentRoot) != before {
		t.Fatal("guard abort changed the parent")
	}
	if r := rtRevision(t, s, task.ID, a.agent.ID); r.State != store.RevisionSubmitted {
		t.Fatalf("revision after guard abort: %+v", r)
	}
	// An agent whose submission changed is refused too (follow-up re-queued it).
	setRuntimeIntegrateHook(nil)
	if err := s.UpdateThread(ctx, task.ThreadID, map[string]any{"workspace_mode": "local"}); err != nil {
		t.Fatal(err)
	}
	ag, _ := s.RuntimeAgent(ctx, task.ID, a.agent.ID)
	ag.State = protocol.AgentStateQueued
	if err := s.UpdateRuntimeAgent(ctx, ag, ag.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err == nil {
		t.Fatal("re-queued agent's revision integrated")
	}
	if rtParent(t, task.ParentRoot) != before {
		t.Fatal("refused accept changed the parent")
	}
}

// A crash after the revision was recorded applied but before the head moved
// is repaired from the revision's evidence.
func TestReconcileRuntimeRepairsHead(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	got, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.AgentIntegrationHead(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.HeadTree, h.AppliedCount = got.Revision.TreeBefore, 0
	if err = s.SetAgentIntegrationHead(ctx, h, h.Version); err != nil {
		t.Fatal(err)
	}
	if err = svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
	if st.HeadTree != got.HeadTree || st.AppliedCount != 1 || st.Verified {
		t.Fatalf("head not repaired: %+v", st)
	}
	if err = svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if st2, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); st2.AppliedCount != 1 {
		t.Fatalf("repair not idempotent: %+v", st2)
	}
}

// The brief's test names, grouped so `-run 'Runtime'` selects them.
func TestRuntimeManualResolution(t *testing.T) {
	t.Run("TestResolveManuallyRecordsCurrentTreeAsHead", testResolveManuallyRecordsCurrentTreeAsHead)
	t.Run("interrupted before the parent changed returns to conflict", func(t *testing.T) {
		svc, s, task, _, b, _ := rtConflict(t)
		ctx := t.Context()
		st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
		rtSetApplying(t, s, task.ID, b.agent.ID, st.HeadTree, manualResolution)
		if err := svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
		r := rtRevision(t, s, task.ID, b.agent.ID)
		if r.State != store.RevisionConflict || !reflect.DeepEqual(r.Conflict, []string{"tracked"}) || r.Reason == manualResolution {
			t.Fatalf("interrupted resolution reconciled to %+v", r)
		}
		// A retry records its own intent without any manual marker.
		var intentReason string
		setRuntimeIntegrateHook(func(stage string) {
			if stage == "intent" {
				intentReason = rtRevision(t, s, task.ID, b.agent.ID).Reason
			}
		})
		t.Cleanup(func() { setRuntimeIntegrateHook(nil) })
		got, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID)
		if err != nil || got.Revision.State != store.RevisionConflict {
			t.Fatalf("retry = %+v %v", got, err)
		}
		if intentReason != "" {
			t.Fatalf("intent kept reason %q", intentReason)
		}
	})
	t.Run("interrupted after staging resumes", func(t *testing.T) {
		svc, s, task, _, b, c := rtConflict(t)
		ctx := t.Context()
		st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
		workspaceWrite(t, task.ParentRoot, "tracked", "from a and b\n")
		workspaceGit(t, task.ParentRoot, "add", "tracked")
		rtSetApplying(t, s, task.ID, b.agent.ID, st.HeadTree, manualResolution)
		if err := svc.ReconcileRuntimeIntegration(ctx, task.ID); err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("interrupted resolution not reported: %v", err)
		}
		if _, err := svc.IntegrateRuntime(ctx, task.ID, c.agent.ID); err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("accept during interrupted resolution: %v", err)
		}
		got, err := svc.ResolveRuntimeManually(ctx, task.ID, b.agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		tree := workspaceGit(t, task.ParentRoot, "write-tree")
		if got.Revision.State != store.RevisionApplied || got.HeadTree != tree || !got.NeedsVerification {
			t.Fatalf("resumed = %+v", got)
		}
		if st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); st.HeadTree != tree || st.AppliedCount != 2 {
			t.Fatalf("status %+v", st)
		}
	})
}

func TestRuntimeIntegrationRefusesWhenTaskCancelled(t *testing.T) {
	t.Run("TestIntegrationRefusesWhenTaskCancelled", testIntegrationRefusesWhenTaskCancelled)
}

func TestRuntimeIntegrationUsesAuthorityRecheck(t *testing.T) {
	t.Run("TestIntegrationUsesAuthorityRecheck", testIntegrationUsesAuthorityRecheck)
}

// rtConflict integrates and verifies a, then leaves b in conflict on tracked;
// c (adding a new file) is submitted and untouched.
func rtConflict(t *testing.T) (*WorkspaceService, *store.Store, protocol.AgentTask, rtAgent, rtAgent, rtAgent) {
	t.Helper()
	svc, s, task := rtFixture(t)
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	b := rtWriter(t, svc, s, task, "b", rtWrite(t, "tracked", "from b\n"))
	c := rtWriter(t, svc, s, task, "c", rtWrite(t, "added", "c\n"))
	if _, err := svc.IntegrateRuntime(t.Context(), task.ID, a.agent.ID); err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	if got, err := svc.IntegrateRuntime(t.Context(), task.ID, b.agent.ID); err != nil || got.Revision.State != store.RevisionConflict {
		t.Fatalf("expected conflict: %+v %v", got, err)
	}
	return svc, s, task, a, b, c
}

// rtSetApplying simulates a crash right after an intent was recorded.
func rtSetApplying(t *testing.T, s *store.Store, taskID, agentID, before, reason string) {
	t.Helper()
	r := rtRevision(t, s, taskID, agentID)
	r.State, r.TreeBefore, r.Reason = store.RevisionApplying, before, reason
	if err := s.UpdateAgentRevision(t.Context(), r, r.Version); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeConflictRetry(t *testing.T) {
	t.Run("conflicts again", func(t *testing.T) {
		svc, s, task, _, b, _ := rtConflict(t)
		before := rtParent(t, task.ParentRoot)
		got, err := svc.IntegrateRuntime(t.Context(), task.ID, b.agent.ID)
		if err != nil || got.Revision.State != store.RevisionConflict || !reflect.DeepEqual(got.Revision.Conflict, []string{"tracked"}) || len(got.Options) != 2 {
			t.Fatalf("retry = %+v %v", got, err)
		}
		if rtParent(t, task.ParentRoot) != before {
			t.Fatal("retry changed the parent")
		}
		_ = s
	})
	t.Run("failure before mutation restores the conflict", func(t *testing.T) {
		svc, s, task, _, b, _ := rtConflict(t)
		ctx := t.Context()
		setRuntimeIntegrateHook(func(stage string) {
			if stage == "intent" {
				if err := s.UpdateThread(ctx, task.ThreadID, map[string]any{"workspace_mode": "worktree"}); err != nil {
					t.Error(err)
				}
			}
		})
		t.Cleanup(func() { setRuntimeIntegrateHook(nil) })
		if _, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID); err == nil {
			t.Fatal("guard abort not reported")
		}
		r := rtRevision(t, s, task.ID, b.agent.ID)
		if r.State != store.RevisionConflict || !reflect.DeepEqual(r.Conflict, []string{"tracked"}) {
			t.Fatalf("conflict not restored: %+v", r)
		}
	})
}

// A guard that refuses on its second pass (right before git writes) aborts
// with the parent untouched and the revision as it was.
func TestRuntimeGuardAbortOnSecondPass(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	before := rtParent(t, task.ParentRoot)
	setRuntimeIntegrateHook(func(stage string) {
		if stage == "guard2" {
			if err := s.UpdateThread(ctx, task.ThreadID, map[string]any{"workspace_mode": "worktree"}); err != nil {
				t.Error(err)
			}
		}
	})
	t.Cleanup(func() { setRuntimeIntegrateHook(nil) })
	if _, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID); err == nil || !strings.Contains(err.Error(), "parent checkout changed") {
		t.Fatalf("second-pass guard abort: %v", err)
	}
	if rtParent(t, task.ParentRoot) != before || rtRevision(t, s, task.ID, a.agent.ID).State != store.RevisionSubmitted {
		t.Fatal("second-pass abort changed state")
	}
}

// A failure after both guard passes (here: cancellation right before the
// write) is decided from the actual parent tree: unchanged => retry allowed.
// (A conflicted revision never reaches the second pass: it conflicts in the
// private-index prediction first.)
func TestRuntimeFailureAfterSecondGuardReconciles(t *testing.T) {
	svc, s, task := rtFixture(t)
	w := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before := rtParent(t, task.ParentRoot)
	setRuntimeIntegrateHook(func(stage string) {
		if stage == "passed2" {
			cancel()
		}
	})
	t.Cleanup(func() { setRuntimeIntegrateHook(nil) })
	if _, err := svc.IntegrateRuntime(ctx, task.ID, w.agent.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel after the second guard: %v", err)
	}
	if rtParent(t, task.ParentRoot) != before {
		t.Fatal("parent changed")
	}
	if r := rtRevision(t, s, task.ID, w.agent.ID); r.State != store.RevisionSubmitted {
		t.Fatalf("revision %+v", r)
	}
	setRuntimeIntegrateHook(nil)
	if _, err := svc.IntegrateRuntime(t.Context(), task.ID, w.agent.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// Crash after `git apply` of a revision that merged into a file an earlier
// revision changed: recovery recognises the predicted tree and never re-applies.
func TestReconcileRuntimeMergedHunks(t *testing.T) {
	lines := "1\n2\n3\n4\n5\n6\n7\n8\n9\n"
	svc, s, task := rtFixtureFiles(t, map[string]string{"lines": lines})
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "lines", strings.Replace(lines, "1\n", "one\n", 1)))
	b := rtWriter(t, svc, s, task, "b", rtWrite(t, "lines", strings.Replace(lines, "9\n", "nine\n", 1)))
	first, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	rtSetApplying(t, s, task.ID, b.agent.ID, first.HeadTree, "")
	if _, err = svc.Manager.ApplyOnIntegrated(ctx, b.ws.ID, task.ParentRoot, task.BaseRevision, first.HeadTree, b.agent.Revision); err != nil {
		t.Fatal(err)
	}
	tree := workspaceGit(t, task.ParentRoot, "write-tree")
	if err = svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	r := rtRevision(t, s, task.ID, b.agent.ID)
	st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID)
	if r.State != store.RevisionApplied || r.TreeAfter != tree || st.HeadTree != tree || st.AppliedCount != 2 || st.Verified {
		t.Fatalf("merged recovery: %+v / %+v", r, st)
	}
	if _, err = svc.IntegrateRuntime(ctx, task.ID, b.agent.ID); err != nil || workspaceGit(t, task.ParentRoot, "write-tree") != tree {
		t.Fatalf("replay re-applied: %v", err)
	}
	if got := workspaceGit(t, task.ParentRoot, "show", ":lines"); !strings.HasPrefix(got, "one\n") || !strings.HasSuffix(got, "nine") {
		t.Fatalf("merged content %q", got)
	}
}

// The head repair uses the revision applied last, even when trees repeat or
// the apply order differs from the submission order.
func TestReconcileRuntimeRepairsHeadLatest(t *testing.T) {
	rewind := func(t *testing.T, s *store.Store, taskID, tree string, count int) {
		t.Helper()
		h, err := s.AgentIntegrationHead(t.Context(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		h.HeadTree, h.AppliedCount = tree, count
		if err = s.SetAgentIntegrationHead(t.Context(), h, h.Version); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("repeated head tree", func(t *testing.T) {
		svc, s, task, _, b, c := rtConflict(t)
		ctx := t.Context()
		// The lead resolves b by restoring the base content: T1 -> T0.
		workspaceWrite(t, task.ParentRoot, "tracked", "base\n")
		workspaceGit(t, task.ParentRoot, "add", "tracked")
		resolved, err := svc.ResolveRuntimeManually(ctx, task.ID, b.agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if base := workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD^{tree}"); resolved.HeadTree != base {
			t.Fatalf("resolution tree %s, want the base tree %s", resolved.HeadTree, base)
		}
		rtVerify(t, s, task.ID)
		got, err := svc.IntegrateRuntime(ctx, task.ID, c.agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		rewind(t, s, task.ID, resolved.HeadTree, 2)
		if err = svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
		if st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); st.HeadTree != got.HeadTree || st.AppliedCount != 3 {
			t.Fatalf("repaired to %+v, want %s", st, got.HeadTree)
		}
	})
	t.Run("apply order differs from submission order", func(t *testing.T) {
		svc, s, task := rtFixture(t)
		ctx := t.Context()
		a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
		b := rtWriter(t, svc, s, task, "b", rtWrite(t, "added", "b\n"))
		gotB, err := svc.IntegrateRuntime(ctx, task.ID, b.agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		rtVerify(t, s, task.ID)
		gotA, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		rewind(t, s, task.ID, gotB.HeadTree, 1)
		if err = svc.ReconcileRuntimeIntegration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
		if st, _ := svc.RuntimeIntegrationStatus(ctx, task.ID); st.HeadTree != gotA.HeadTree || st.AppliedCount != 2 {
			t.Fatalf("repaired to %+v, want %s", st, gotA.HeadTree)
		}
	})
}

// M4 (final review): completion checks the verified head and changes the
// task state under the task lock, so no check result is recorded in between.
func TestFinishRuntimeHoldsTaskLock(t *testing.T) {
	svc, s, task := rtFixture(t)
	ctx := t.Context()
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	res, err := svc.IntegrateRuntime(ctx, task.ID, a.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	head := res.HeadTree
	fail := func(w store.RuntimeWorkflow) (store.RuntimeWorkflow, error) {
		w.Checks = []store.AgentWorkflowCheck{{Label: "tests", Command: "go test ./...", Status: "failed"}}
		return w, nil
	}
	// An unverified head is refused and nothing completes.
	called := false
	if err = svc.FinishRuntime(ctx, task.ID, head, 1, func() error { called = true; return nil }); err == nil || called || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("unverified head: err=%v called=%v", err, called)
	}
	rtVerify(t, s, task.ID)
	// A stale applied count (another accept happened) is refused.
	if err = svc.FinishRuntime(ctx, task.ID, head, 2, func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("stale count: err=%v called=%v", err, called)
	}
	done := make(chan error, 1)
	err = svc.FinishRuntime(ctx, task.ID, head, 1, func() error {
		go func() { done <- svc.UpdateRuntimeWorkflow(context.Background(), task.ID, head, fail) }()
		select {
		case err := <-done:
			t.Errorf("a check result was recorded inside completion: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		return s.UpdateAgentTaskState(ctx, task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCompleted)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("a failed result was recorded after completion")
	}
	h, err := s.AgentIntegrationHead(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := store.DecodeRuntimeWorkflow(h.Workflow); len(w.Checks) != 1 || w.Checks[0].Status != "passed" {
		t.Fatalf("workflow after completion = %+v", w)
	}
}
