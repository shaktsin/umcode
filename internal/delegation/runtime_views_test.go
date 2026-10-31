package delegation

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		if _, err := svc.PrepareRuntime(t.Context(), task, b, files == protocol.FilesReadWrite); err == nil || !strings.Contains(err.Error(), "an integration is being applied; try again") {
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
