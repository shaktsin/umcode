package delegation

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// rtTwoApplied is a task whose writers a (tracked) and b (added) were both
// accepted, a's head verified in between; the final head is not verified.
func rtTwoApplied(t *testing.T) (*WorkspaceService, *store.Store, protocol.AgentTask, rtAgent, rtAgent) {
	t.Helper()
	svc, s, task := rtFixture(t)
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	b := rtWriter(t, svc, s, task, "b", rtWrite(t, "added", "from b\n"))
	if _, err := svc.IntegrateRuntime(t.Context(), task.ID, a.agent.ID); err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	if res, err := svc.IntegrateRuntime(t.Context(), task.ID, b.agent.ID); err != nil || res.Revision.State != store.RevisionApplied {
		t.Fatalf("accept b = %+v %v", res, err)
	}
	return svc, s, task, a, b
}

// rtStageFix edits tracked in the project checkout and stages it, returning
// the staged tree.
func rtStageFix(t *testing.T, root, data string) string {
	t.Helper()
	workspaceWrite(t, root, "tracked", data)
	workspaceGit(t, root, "add", "tracked")
	return workspaceGit(t, root, "write-tree")
}

func rtHeadRow(t *testing.T, s *store.Store, taskID string) store.AgentIntegrationHead {
	t.Helper()
	h, err := s.AgentIntegrationHead(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAmendRuntimeHeadRecordsStagedTree(t *testing.T) {
	svc, s, task, a, b := rtTwoApplied(t)
	ctx := t.Context()
	before := rtHeadRow(t, s, task.ID)
	fixed := rtStageFix(t, task.ParentRoot, "from a, fixed\n")
	// Make tracked entries stat-dirty: amend must still not rewrite the index.
	later := time.Now().Add(2 * time.Hour)
	for _, rel := range []string{"tracked", "added"} {
		if err := os.Chtimes(filepath.Join(task.ParentRoot, rel), later, later); err != nil {
			t.Fatal(err)
		}
	}
	checkout := rtCheckoutBytes(t, task.ParentRoot)
	revA, revB := rtRevision(t, s, task.ID, a.agent.ID), rtRevision(t, s, task.ID, b.agent.ID)
	got, err := svc.AmendRuntimeHead(ctx, task.ID, "  combined checks fail: fix tracked  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadTree != fixed || !got.NeedsVerification {
		t.Fatalf("amend = %+v, want head %s", got, fixed)
	}
	if after := rtCheckoutBytes(t, task.ParentRoot); after != checkout {
		t.Fatalf("amend rewrote the project checkout:\n%s\n---\n%s", checkout, after)
	}
	if _, err := os.Lstat(filepath.Join(task.ParentRoot, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index.lock: %v", err)
	}
	h := rtHeadRow(t, s, task.ID)
	if h.HeadTree != fixed || h.AppliedCount != before.AppliedCount || h.Amendments != 1 || h.AmendReason != "combined checks fail: fix tracked" || h.Version != before.Version+1 {
		t.Fatalf("head = %+v (before %+v)", h, before)
	}
	if w, _ := store.DecodeRuntimeWorkflow(h.Workflow); len(w.Checks) != 0 || w.HeadTree != "" {
		t.Fatalf("workflow not reset: %+v", w)
	}
	st, err := svc.RuntimeIntegrationStatus(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.HeadTree != fixed || st.AppliedCount != 2 || st.Amendments != 1 || st.Verified {
		t.Fatalf("status = %+v", st)
	}
	if r := rtRevision(t, s, task.ID, a.agent.ID); !reflect.DeepEqual(r, revA) {
		t.Fatalf("a changed: %+v -> %+v", revA, r)
	}
	if r := rtRevision(t, s, task.ID, b.agent.ID); !reflect.DeepEqual(r, revB) {
		t.Fatalf("b changed: %+v -> %+v", revB, r)
	}
	// The amended head verifies and completes like any other head.
	if err := svc.ValidateRuntimeHead(ctx, task.ID, fixed); err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	if err := svc.FinishRuntime(ctx, task.ID, fixed, 2, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestAmendNeedsReasonAndActiveTask(t *testing.T) {
	svc, s, task, _, _ := rtTwoApplied(t)
	rtStageFix(t, task.ParentRoot, "fixed\n")
	if _, err := svc.AmendRuntimeHead(t.Context(), task.ID, "  "); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("amend without reason: %v", err)
	}
	if err := s.UpdateAgentTaskState(t.Context(), task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AmendRuntimeHead(t.Context(), task.ID, "fix"); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("amend on cancelled task: %v", err)
	}
}

func TestAmendRefusesUnstagedUntrackedMovedHead(t *testing.T) {
	cases := []struct {
		name, want string
		dirty      func(t *testing.T, root string)
	}{
		{"unstaged", "removed", func(t *testing.T, root string) { workspaceWrite(t, root, "removed", "edited, not staged\n") }},
		{"untracked", "stray.txt", func(t *testing.T, root string) { workspaceWrite(t, root, "stray.txt", "x\n") }},
		{"moved HEAD", "HEAD moved", func(t *testing.T, root string) {
			workspaceGit(t, root, "commit", "-qm", "user commit")
			rtStageFix(t, root, "fixed again\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, s, task, _, _ := rtTwoApplied(t)
			rtStageFix(t, task.ParentRoot, "fixed\n")
			c.dirty(t, task.ParentRoot)
			before := rtHeadRow(t, s, task.ID)
			checkout := rtCheckoutBytes(t, task.ParentRoot)
			_, err := svc.AmendRuntimeHead(t.Context(), task.ID, "fix")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("amend with %s: %v", c.name, err)
			}
			if h := rtHeadRow(t, s, task.ID); h.Version != before.Version || h.HeadTree != before.HeadTree || h.Amendments != 0 {
				t.Fatalf("head moved: %+v", h)
			}
			if after := rtCheckoutBytes(t, task.ParentRoot); after != checkout {
				t.Fatal("refused amend changed the checkout")
			}
		})
	}
}

func TestAmendRefusesConflictStages(t *testing.T) {
	svc, s, task, _, _ := rtTwoApplied(t)
	root := task.ParentRoot
	blob := func(data string) string {
		workspaceWrite(t, root, "scratch.tmp", data)
		id := workspaceGit(t, root, "hash-object", "-w", "scratch.tmp")
		if err := os.Remove(filepath.Join(root, "scratch.tmp")); err != nil {
			t.Fatal(err)
		}
		return id
	}
	o, x, y := blob("o\n"), blob("x\n"), blob("y\n")
	workspaceGit(t, root, "rm", "-q", "--cached", "tracked")
	rtGitStdin(t, root, "100644 "+o+" 1\ttracked\n100644 "+x+" 2\ttracked\n100644 "+y+" 3\ttracked\n", "update-index", "--index-info")
	before := rtHeadRow(t, s, task.ID)
	_, err := svc.AmendRuntimeHead(t.Context(), task.ID, "fix")
	if err == nil || !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "tracked") {
		t.Fatalf("amend with conflict stages: %v", err)
	}
	if h := rtHeadRow(t, s, task.ID); h.Version != before.Version {
		t.Fatalf("head moved: %+v", h)
	}
}

func TestAmendNothingToAmend(t *testing.T) {
	svc, s, task, _, _ := rtTwoApplied(t)
	before := rtHeadRow(t, s, task.ID)
	_, err := svc.AmendRuntimeHead(t.Context(), task.ID, "fix")
	if err == nil || !strings.Contains(err.Error(), "nothing to amend") {
		t.Fatalf("amend without a staged change: %v", err)
	}
	if h := rtHeadRow(t, s, task.ID); h.Version != before.Version || h.Amendments != 0 {
		t.Fatalf("head changed: %+v", h)
	}
}

func TestAmendIdempotentRetry(t *testing.T) {
	svc, s, task, _, _ := rtTwoApplied(t)
	ctx := t.Context()
	fixed := rtStageFix(t, task.ParentRoot, "fixed\n")
	if _, err := svc.AmendRuntimeHead(ctx, task.ID, "fix"); err != nil {
		t.Fatal(err)
	}
	rtVerify(t, s, task.ID)
	once := rtHeadRow(t, s, task.ID)
	got, err := svc.AmendRuntimeHead(ctx, task.ID, "fix")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got.HeadTree != fixed {
		t.Fatalf("retry head = %s", got.HeadTree)
	}
	h := rtHeadRow(t, s, task.ID)
	if h.Amendments != 1 || h.Version != once.Version || string(h.Workflow) != string(once.Workflow) {
		t.Fatalf("retry changed the head: %+v -> %+v", once, h)
	}
	// A second, different fix is a second amendment.
	second := rtStageFix(t, task.ParentRoot, "fixed twice\n")
	if _, err := svc.AmendRuntimeHead(ctx, task.ID, "again"); err != nil {
		t.Fatal(err)
	}
	if h := rtHeadRow(t, s, task.ID); h.Amendments != 2 || h.HeadTree != second || h.AmendReason != "again" {
		t.Fatalf("second amend: %+v", h)
	}
}

func TestAmendNeedsAppliedRevision(t *testing.T) {
	t.Run("nothing applied", func(t *testing.T) {
		svc, s, task := rtFixture(t)
		rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
		rtStageFix(t, task.ParentRoot, "user fix\n")
		if _, err := svc.AmendRuntimeHead(t.Context(), task.ID, "fix"); err == nil || !strings.Contains(err.Error(), "applied") {
			t.Fatalf("amend before any accept: %v", err)
		}
	})
	t.Run("a revision applying", func(t *testing.T) {
		svc, s, task := rtFixture(t)
		a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
		c := rtWriter(t, svc, s, task, "c", rtWrite(t, "added", "c\n"))
		if _, err := svc.IntegrateRuntime(t.Context(), task.ID, a.agent.ID); err != nil {
			t.Fatal(err)
		}
		rtStageFix(t, task.ParentRoot, "user fix\n")
		h := rtHeadRow(t, s, task.ID)
		// An interrupted apply whose parent is neither before nor after stays
		// applying (blocked); amend must refuse it.
		rtSetApplying(t, s, task.ID, c.agent.ID, h.HeadTree, "")
		if _, err := svc.AmendRuntimeHead(t.Context(), task.ID, "fix"); err == nil || !strings.Contains(err.Error(), "applying") {
			t.Fatalf("amend while applying: %v", err)
		}
		if got := rtHeadRow(t, s, task.ID); got.Version != h.Version {
			t.Fatalf("head moved: %+v", got)
		}
	})
}

func TestAmendDoesNotChangeRevisionStates(t *testing.T) {
	svc, s, task, _, b, c := rtConflict(t)
	ctx := t.Context()
	d := rtWriter(t, svc, s, task, "d", rtWrite(t, "other", "d\n"))
	if _, err := svc.DropRuntime(ctx, task.ID, d.agent.ID, "not needed"); err != nil {
		t.Fatal(err)
	}
	revs, err := s.AgentRevisions(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	rtStageFix(t, task.ParentRoot, "from a, amended\n")
	if _, err := svc.AmendRuntimeHead(ctx, task.ID, "fix"); err != nil {
		t.Fatal(err)
	}
	after, err := s.AgentRevisions(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(revs) {
		t.Fatalf("revisions %d -> %d", len(revs), len(after))
	}
	for i := range revs {
		if revs[i].State != after[i].State || revs[i].Version != after[i].Version || revs[i].TreeAfter != after[i].TreeAfter {
			t.Fatalf("revision %s changed: %+v -> %+v", revs[i].AgentID, revs[i], after[i])
		}
	}
	if rtRevision(t, s, task.ID, b.agent.ID).State != store.RevisionConflict || rtRevision(t, s, task.ID, c.agent.ID).State != store.RevisionSubmitted {
		t.Fatal("states changed")
	}
}
