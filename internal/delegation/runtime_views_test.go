package delegation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// rtCheckoutBytes fingerprints the project checkout byte for byte without
// running Git: HEAD, the raw index file and every working tree entry.
func rtCheckoutBytes(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	for _, name := range []string{"HEAD", "index"} {
		data, err := os.ReadFile(filepath.Join(root, ".git", name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %x\n", name, data)
	}
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %s", rel, info.Mode())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " %x", data)
		}
		b.WriteString("\n")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// rtStartAgent creates a running runtime agent with files access that has no
// workspace yet.
func rtStartAgent(t *testing.T, s *store.Store, task protocol.AgentTask, name string, files protocol.FileAccess) protocol.RuntimeAgent {
	t.Helper()
	ctx := t.Context()
	a, err := s.CreateRuntimeAgent(ctx, protocol.RuntimeAgent{TaskID: task.ID, Name: name, Purpose: "work",
		Assignment: protocol.AgentAssignment{Prompt: "work"}, Capabilities: protocol.AgentCapabilities{Files: files}}, "call-"+name)
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
	return a
}

// rtAccepted is a task whose writer "a" (edit) was accepted and verified.
func rtAccepted(t *testing.T, files map[string]string, edit func(root string)) (*WorkspaceService, *store.Store, protocol.AgentTask, string) {
	t.Helper()
	svc, s, task := rtFixtureFiles(t, files)
	a := rtWriter(t, svc, s, task, "a", edit)
	res, err := svc.IntegrateRuntime(t.Context(), task.ID, a.agent.ID)
	if err != nil || len(res.Options) > 0 {
		t.Fatalf("accept a = %+v %v", res, err)
	}
	rtVerify(t, s, task.ID)
	return svc, s, task, res.HeadTree
}

func TestPrepareRuntimeAfterFirstAcceptWriter(t *testing.T) {
	svc, s, task, head := rtAccepted(t, nil, rtWrite(t, "tracked", "from a\n"))
	before := rtCheckoutBytes(t, task.ParentRoot)
	b := rtStartAgent(t, s, task, "b", protocol.FilesReadWrite)
	ref, err := svc.PrepareRuntime(t.Context(), task, b, true)
	if err != nil {
		t.Fatalf("writer after the first accept: %v", err)
	}
	if after := rtCheckoutBytes(t, task.ParentRoot); after != before {
		t.Fatalf("preparing a writer changed the project checkout:\n%s\n---\n%s", before, after)
	}
	if got := workspaceGit(t, ref.Root, "rev-parse", "HEAD"); got != task.BaseRevision {
		t.Fatalf("writer HEAD = %s, want base %s", got, task.BaseRevision)
	}
	if data, err := os.ReadFile(filepath.Join(ref.Root, "tracked")); err != nil || string(data) != "base\n" {
		t.Fatalf("writer sees %q %v, want the base revision", data, err)
	}
	if ref.BaseRevision != task.BaseRevision {
		t.Fatalf("writer base %s", ref.BaseRevision)
	}
	w, err := s.AgentWorkspace(t.Context(), ref.ID)
	if err != nil || w.Kind != "implementer" {
		t.Fatalf("writer workspace = %+v %v", w, err)
	}
	if h, err := s.AgentIntegrationHead(t.Context(), task.ID); err != nil || h.HeadTree != head {
		t.Fatalf("head changed: %+v %v", h, err)
	}
}

func TestPrepareRuntimeViewHasIntegratedTree(t *testing.T) {
	files := map[string]string{"bin.dat": "\x00\x01base", "old.txt": "rename me\n", "run.sh": "echo hi\n"}
	svc, s, task, head := rtAccepted(t, files, func(root string) {
		workspaceWrite(t, root, "bin.dat", "\x00\x02\xffchanged")
		workspaceWrite(t, root, "tracked", "from a\n")
		if err := os.Remove(filepath.Join(root, "removed")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
	})
	before := rtCheckoutBytes(t, task.ParentRoot)
	r := rtStartAgent(t, s, task, "reviewer", protocol.FilesRead)
	ref, err := svc.PrepareRuntimeView(t.Context(), task.ID, r.ID)
	if err != nil {
		t.Fatalf("view after the first accept: %v", err)
	}
	if after := rtCheckoutBytes(t, task.ParentRoot); after != before {
		t.Fatalf("preparing a view changed the project checkout:\n%s\n---\n%s", before, after)
	}
	if got := workspaceGit(t, ref.Root, "rev-parse", "HEAD^{tree}"); got != head {
		t.Fatalf("view tree = %s, want the integrated head %s", got, head)
	}
	if got := workspaceGit(t, ref.Root, "write-tree"); got != head {
		t.Fatalf("view index = %s, want %s", got, head)
	}
	if st := workspaceGit(t, ref.Root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("view not clean: %q", st)
	}
	if data, err := os.ReadFile(filepath.Join(ref.Root, "bin.dat")); err != nil || !bytes.Equal(data, []byte("\x00\x02\xffchanged")) {
		t.Fatalf("binary = %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(ref.Root, "tracked")); err != nil || string(data) != "from a\n" {
		t.Fatalf("tracked = %q %v", data, err)
	}
	for _, gone := range []string{"removed", "old.txt"} {
		if _, err := os.Lstat(filepath.Join(ref.Root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still in the view: %v", gone, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(ref.Root, "new.txt")); err != nil || string(data) != "rename me\n" {
		t.Fatalf("renamed = %q %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(ref.Root, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode change missing: %v %v", info, err)
	}
	w, err := s.AgentWorkspace(t.Context(), ref.ID)
	if err != nil || w.Kind != "reviewer" || w.SourceID != runtimeHeadSource || w.ResultTree != head || w.BaseRevision != task.BaseRevision {
		t.Fatalf("view workspace = %+v %v", w, err)
	}
	if got := workspaceGit(t, ref.Root, "rev-parse", "HEAD"); got != w.ResultRevision {
		t.Fatalf("view HEAD %s, recorded %s", got, w.ResultRevision)
	}
	// The stopped view snapshots as unchanged and is discarded like a base view.
	if r, err = s.RuntimeAgent(t.Context(), task.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.SnapshotRuntime(t.Context(), task, r, ref)
	if err != nil || snap.ResultRevision != w.ResultRevision || len(snap.ChangedPaths) != 0 {
		t.Fatalf("snapshot view = %+v %v", snap, err)
	}
	r.State = protocol.AgentStateCompleted
	if err = s.UpdateRuntimeAgent(t.Context(), r, r.Version); err != nil {
		t.Fatal(err)
	}
	if err = svc.DiscardRuntimeView(t.Context(), task.ID, r.ID); err != nil {
		t.Fatalf("discard view: %v", err)
	}
	// PrepareRuntime with writable=false is the same view.
	r2 := rtStartAgent(t, s, task, "reviewer2", protocol.FilesRead)
	ref2, err := svc.PrepareRuntime(t.Context(), task, r2, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := workspaceGit(t, ref2.Root, "rev-parse", "HEAD^{tree}"); got != head {
		t.Fatalf("PrepareRuntime view tree = %s, want %s", got, head)
	}
}

// Before any accept a read-only agent still gets the base revision.
func TestPrepareRuntimeViewBeforeAcceptIsBase(t *testing.T) {
	svc, s, task := rtFixture(t)
	r := rtStartAgent(t, s, task, "reader", protocol.FilesRead)
	ref, err := svc.PrepareRuntimeView(t.Context(), task.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AgentWorkspace(t.Context(), ref.ID)
	if err != nil || w.SourceID != runtimeBaseSource || w.ResultRevision != task.BaseRevision {
		t.Fatalf("view workspace = %+v %v", w, err)
	}
	if got := workspaceGit(t, ref.Root, "rev-parse", "HEAD"); got != task.BaseRevision {
		t.Fatalf("view HEAD %s", got)
	}
	// A writer is never given a view.
	wr := rtStartAgent(t, s, task, "writer", protocol.FilesReadWrite)
	if _, err = svc.PrepareRuntimeView(t.Context(), task.ID, wr.ID); err == nil {
		t.Fatal("a write-capable agent got a read-only view")
	}
}

func TestPrepareRuntimeRefusesDirtyOrMovedParentService(t *testing.T) {
	cases := []struct {
		name  string
		dirty func(t *testing.T, root string, task protocol.AgentTask)
		want  string
	}{
		{"unrelated edit", func(t *testing.T, root string, _ protocol.AgentTask) {
			workspaceWrite(t, root, "removed", "user edit\n")
		}, "removed"},
		{"untracked", func(t *testing.T, root string, _ protocol.AgentTask) { workspaceWrite(t, root, "notes", "mine\n") }, "notes"},
		{"moved HEAD", func(t *testing.T, root string, task protocol.AgentTask) {
			tree := workspaceGit(t, root, "rev-parse", task.BaseRevision+"^{tree}")
			moved := workspaceGit(t, root, "commit-tree", tree, "-p", task.BaseRevision, "-m", "moved")
			workspaceGit(t, root, "update-ref", "HEAD", moved)
		}, "revision changed"},
		{"conflict stages", func(t *testing.T, root string, task protocol.AgentTask) {
			blob := workspaceGit(t, root, "rev-parse", task.BaseRevision+":removed")
			zero := strings.Repeat("0", len(blob))
			cmd := fmt.Sprintf("0 %s\tremoved\n100644 %s 1\tremoved\n100644 %s 2\tremoved\n100644 %s 3\tremoved\n", zero, blob, blob, blob)
			rtGitStdin(t, root, cmd, "update-index", "--index-info")
		}, "removed"},
	}
	for _, c := range cases {
		for _, files := range []protocol.FileAccess{protocol.FilesReadWrite, protocol.FilesRead} {
			t.Run(c.name+"/"+string(files), func(t *testing.T) {
				svc, s, task, _ := rtAccepted(t, nil, rtWrite(t, "tracked", "from a\n"))
				c.dirty(t, task.ParentRoot, task)
				before := rtCheckoutBytes(t, task.ParentRoot)
				a := rtStartAgent(t, s, task, "late", files)
				_, err := svc.PrepareRuntime(t.Context(), task, a, files == protocol.FilesReadWrite)
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("PrepareRuntime = %v, want refusal naming %q", err, c.want)
				}
				if after := rtCheckoutBytes(t, task.ParentRoot); after != before {
					t.Fatalf("refusal changed the project checkout")
				}
			})
		}
	}
}

func TestPrepareRuntimeRefusedWhileApplying(t *testing.T) {
	svc, s, task := rtFixture(t)
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	rev := rtRevision(t, s, task.ID, a.agent.ID)
	rev.State, rev.TreeBefore = store.RevisionApplying, workspaceGit(t, task.ParentRoot, "rev-parse", task.BaseRevision+"^{tree}")
	if err := s.UpdateAgentRevision(t.Context(), rev, rev.Version); err != nil {
		t.Fatal(err)
	}
	for _, files := range []protocol.FileAccess{protocol.FilesReadWrite, protocol.FilesRead} {
		b := rtStartAgent(t, s, task, "late-"+string(files), files)
		if _, err := svc.PrepareRuntime(t.Context(), task, b, files == protocol.FilesReadWrite); err == nil || !strings.Contains(err.Error(), "an integration is being applied") || !strings.Contains(err.Error(), "accept agent "+a.agent.ID+" again") || strings.Contains(err.Error(), "try again") {
			t.Fatalf("prepare while applying (%s): %v", files, err)
		}
	}
}

// A view prepared while an accept is applying waits for it (task lock) and
// then shows the new head; it never observes a half-applied parent.
func TestPrepareRuntimeViewSerializesWithIntegrate(t *testing.T) {
	svc, s, task := rtFixture(t)
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	r := rtStartAgent(t, s, task, "reviewer", protocol.FilesRead)
	type result struct {
		ref WorkspaceRef
		err error
	}
	done := make(chan result, 1)
	started := false
	restore := SetRuntimeIntegrateHookForTest(func(stage string) {
		if stage != "passed2" || started {
			return
		}
		started = true
		go func() {
			ref, err := svc.PrepareRuntimeView(t.Context(), task.ID, r.ID)
			done <- result{ref, err}
		}()
		select {
		case got := <-done:
			t.Errorf("view prepared during the apply: %+v", got)
			done <- got
		case <-time.After(300 * time.Millisecond):
		}
	})
	res, err := svc.IntegrateRuntime(t.Context(), task.ID, a.agent.ID)
	restore()
	if err != nil || len(res.Options) > 0 {
		t.Fatalf("accept = %+v %v", res, err)
	}
	if !started {
		t.Fatal("hook never ran")
	}
	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("view preparation never finished")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if tree := workspaceGit(t, got.ref.Root, "rev-parse", "HEAD^{tree}"); tree != res.HeadTree {
		t.Fatalf("view tree %s, want the new head %s", tree, res.HeadTree)
	}
}

func rtGitStdin(t *testing.T, root, stdin string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
}

// I1 at the service level: a stat-only change in the project checkout is
// accepted and the index file is not rewritten.
func TestPrepareRuntimeAfterFirstAcceptStatDirtyParent(t *testing.T) {
	svc, s, task, head := rtAccepted(t, nil, rtWrite(t, "tracked", "from a\n"))
	later := time.Now().Add(2 * time.Hour)
	for _, rel := range []string{"tracked", "removed"} {
		if err := os.Chtimes(filepath.Join(task.ParentRoot, rel), later, later); err != nil {
			t.Fatal(err)
		}
	}
	before := rtCheckoutBytes(t, task.ParentRoot)
	b := rtStartAgent(t, s, task, "b", protocol.FilesReadWrite)
	if _, err := svc.PrepareRuntime(t.Context(), task, b, true); err != nil {
		t.Fatal(err)
	}
	r := rtStartAgent(t, s, task, "r", protocol.FilesRead)
	ref, err := svc.PrepareRuntime(t.Context(), task, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := workspaceGit(t, ref.Root, "rev-parse", "HEAD^{tree}"); got != head {
		t.Fatalf("view tree %s, want %s", got, head)
	}
	if after := rtCheckoutBytes(t, task.ParentRoot); after != before {
		t.Fatal("preparing workspaces rewrote the project checkout (index bytes)")
	}
	if _, err := os.Lstat(filepath.Join(task.ParentRoot, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index.lock left behind: %v", err)
	}
}

// M3: a writer re-prepared after its snapshot recorded a result revision
// gets its checkout back (no "view" refusal).
func TestPrepareRuntimeWriterAgainAfterSnapshot(t *testing.T) {
	svc, s, task := rtFixture(t)
	w := rtStartAgent(t, s, task, "w", protocol.FilesReadWrite)
	ref, err := svc.PrepareRuntime(t.Context(), task, w, true)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ref.Root, "tracked", "edit\n")
	if w, err = s.RuntimeAgent(t.Context(), task.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SnapshotRuntime(t.Context(), task, w, ref); err != nil {
		t.Fatal(err)
	}
	again, err := svc.PrepareRuntime(t.Context(), task, w, true)
	if err != nil || again.ID != ref.ID {
		t.Fatalf("writer re-prepare = %+v %v", again, err)
	}
}

// M3: a reader whose first preparation was refused (dirty parent) leaves no
// stale identity: after the parent is clean and a revision is accepted it
// gets the view of the new head.
func TestPrepareRuntimeViewAfterRefusedFirstPrepare(t *testing.T) {
	svc, s, task := rtFixture(t)
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	r := rtStartAgent(t, s, task, "reader", protocol.FilesRead)
	workspaceWrite(t, task.ParentRoot, "notes", "mine\n")
	if _, err := svc.PrepareRuntime(t.Context(), task, r, false); err == nil || !strings.Contains(err.Error(), "notes") {
		t.Fatalf("dirty parent prepare: %v", err)
	}
	if err := os.Remove(filepath.Join(task.ParentRoot, "notes")); err != nil {
		t.Fatal(err)
	}
	res, err := svc.IntegrateRuntime(t.Context(), task.ID, a.agent.ID)
	if err != nil || len(res.Options) > 0 {
		t.Fatalf("accept = %+v %v", res, err)
	}
	ref, err := svc.PrepareRuntime(t.Context(), task, r, false)
	if err != nil {
		t.Fatalf("prepare after the refusal and an accept: %v", err)
	}
	if got := workspaceGit(t, ref.Root, "rev-parse", "HEAD^{tree}"); got != res.HeadTree {
		t.Fatalf("view tree %s, want %s", got, res.HeadTree)
	}
}

// M9: an accept started while a workspace is being prepared waits for the
// preparation (task lock) and the preparation sees the pre-accept state.
func TestIntegrateWaitsForWorkspaceBeingPrepared(t *testing.T) {
	svc, s, task := rtFixture(t)
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	r := rtStartAgent(t, s, task, "reader", protocol.FilesRead)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	svc.ValidateParent = func(context.Context, protocol.AgentTask) error {
		blocked := false
		once.Do(func() { blocked = true })
		if blocked {
			close(entered)
			<-release
		}
		return nil
	}
	type prepared struct {
		ref WorkspaceRef
		err error
	}
	prep := make(chan prepared, 1)
	go func() {
		ref, err := svc.PrepareRuntime(t.Context(), task, r, false)
		prep <- prepared{ref, err}
	}()
	<-entered
	before := rtCheckoutBytes(t, task.ParentRoot)
	integ := make(chan error, 1)
	go func() {
		_, err := svc.IntegrateRuntime(t.Context(), task.ID, a.agent.ID)
		integ <- err
	}()
	select {
	case err := <-integ:
		t.Fatalf("accept finished while a workspace was being prepared: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if after := rtCheckoutBytes(t, task.ParentRoot); after != before {
		t.Fatal("accept changed the project checkout while a workspace was being prepared")
	}
	close(release)
	got := <-prep
	if got.err != nil {
		t.Fatal(got.err)
	}
	if err := <-integ; err != nil {
		t.Fatal(err)
	}
	if head := workspaceGit(t, got.ref.Root, "rev-parse", "HEAD"); head != task.BaseRevision {
		t.Fatalf("view prepared before the accept is at %s, want the base", head)
	}
	if rev := rtRevision(t, s, task.ID, a.agent.ID); rev.State != store.RevisionApplied {
		t.Fatalf("revision %s after the preparation finished", rev.State)
	}
}

// N3: an interrupted manual resolution is settled with resolved, not accept.
func TestPrepareRuntimeRefusedWhileManualResolutionApplying(t *testing.T) {
	svc, s, task := rtFixture(t)
	a := rtWriter(t, svc, s, task, "a", rtWrite(t, "tracked", "from a\n"))
	rev := rtRevision(t, s, task.ID, a.agent.ID)
	rev.State, rev.Reason = store.RevisionApplying, manualResolution
	rev.TreeBefore = workspaceGit(t, task.ParentRoot, "rev-parse", task.BaseRevision+"^{tree}")
	if err := s.UpdateAgentRevision(t.Context(), rev, rev.Version); err != nil {
		t.Fatal(err)
	}
	b := rtStartAgent(t, s, task, "late", protocol.FilesRead)
	_, err := svc.PrepareRuntime(t.Context(), task, b, false)
	if err == nil || !strings.Contains(err.Error(), `agent.integrate {action:"resolved", agent:"`+a.agent.ID+`"}`) || strings.Contains(err.Error(), "accept agent") {
		t.Fatalf("prepare while a manual resolution is applying: %v", err)
	}
}

// N4: a head view whose checkout disappeared is re-planned for the current
// head after another accept.
func TestPrepareRuntimeViewReplannedForNewHead(t *testing.T) {
	svc, s, task, _ := rtAccepted(t, nil, rtWrite(t, "tracked", "from a\n"))
	r := rtStartAgent(t, s, task, "reader", protocol.FilesRead)
	ref, err := svc.PrepareRuntime(t.Context(), task, r, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AgentWorkspace(t.Context(), ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := svc.Manager.Path(ref.ID)
	if !filepath.IsAbs(root) || root != ref.Root {
		t.Fatalf("unexpected checkout root %q", root)
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(root + ".ready"); err != nil {
		t.Fatal(err)
	}
	b := rtWriter(t, svc, s, task, "b", rtWrite(t, "added", "from b\n"))
	res, err := svc.IntegrateRuntime(t.Context(), task.ID, b.agent.ID)
	if err != nil || len(res.Options) > 0 {
		t.Fatalf("accept b = %+v %v", res, err)
	}
	again, err := svc.PrepareRuntime(t.Context(), task, r, false)
	if err != nil {
		t.Fatalf("re-plan: %v", err)
	}
	if got := workspaceGit(t, again.Root, "rev-parse", "HEAD^{tree}"); got != res.HeadTree {
		t.Fatalf("re-planned view tree %s, want %s", got, res.HeadTree)
	}
	w, err := s.AgentWorkspace(t.Context(), ref.ID)
	if err != nil || w.ResultTree != res.HeadTree || w.ResultRevision == first.ResultRevision || w.SourceID != runtimeHeadSource {
		t.Fatalf("re-planned row = %+v %v", w, err)
	}
}
