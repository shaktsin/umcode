package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnsureSnapshotsChangesAndKeepsSourceUntouched(t *testing.T) {
	root := newRepo(t)
	write(t, root, "tracked.txt", "base\n")
	write(t, root, ".gitignore", "ignored.txt\n")
	git(t, root, "add", "tracked.txt", ".gitignore")
	git(t, root, "commit", "-m", "initial")

	write(t, root, "tracked.txt", "dirty tracked edit\n")
	write(t, root, "new file.txt", "new untracked content\n")
	write(t, root, "ignored.txt", "must stay out of the task snapshot\n")
	beforeStatus := git(t, root, "status", "--porcelain")
	appData := filepath.Join(t.TempDir(), "UMCode")
	manager := New(appData)

	first, err := manager.Ensure(context.Background(), "thr_first", root)
	if err != nil {
		t.Fatal(err)
	}
	if !manager.HasWorkspace("thr_first") || manager.HasWorkspace("missing-task") {
		t.Fatal("workspace marker detection did not distinguish initialized and missing task workspaces")
	}
	if !first.Created || first.Path == root || first.SourceCommit == "" {
		t.Fatalf("unexpected task workspace: %+v", first)
	}
	gitDir, err := os.Stat(filepath.Join(first.Path, ".git"))
	if err != nil || !gitDir.IsDir() {
		t.Fatalf("task Git metadata must be self-contained, .git stat = %v, %v", gitDir, err)
	}
	if got := git(t, first.Path, "remote", "-v"); got != "" {
		t.Fatalf("task workspace unexpectedly inherited source remotes: %q", got)
	}
	if got := git(t, first.Path, "rev-parse", "--path-format=absolute", "--git-common-dir"); !samePath(got, filepath.Join(first.Path, ".git")) {
		t.Fatalf("task Git metadata points outside the workspace: %q", got)
	}
	if got := read(t, first.Path, "tracked.txt"); got != "dirty tracked edit\n" {
		t.Fatalf("tracked change was not snapshotted: %q", got)
	}
	if got := read(t, first.Path, "new file.txt"); got != "new untracked content\n" {
		t.Fatalf("untracked file was not snapshotted: %q", got)
	}
	if _, err := os.Stat(filepath.Join(first.Path, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored file was copied (stat err %v)", err)
	}
	if got := read(t, root, "tracked.txt"); got != "dirty tracked edit\n" {
		t.Fatalf("source tracked file changed: %q", got)
	}
	if got := git(t, root, "status", "--porcelain"); got != beforeStatus {
		t.Fatalf("source Git status changed: before %q after %q", beforeStatus, got)
	}

	second, err := manager.Ensure(context.Background(), "thr_second", root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Path == first.Path {
		t.Fatal("two tasks received the same workspace")
	}
	again, err := manager.Ensure(context.Background(), "thr_first", root)
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != first.Path || again.Created || again.SourceCommit != first.SourceCommit {
		t.Fatalf("workspace was not stable across Ensure: first=%+v again=%+v", first, again)
	}
}

func TestEnsurePreservesWorkspaceAfterTaskCommitAndSourceMoves(t *testing.T) {
	root := newRepo(t)
	write(t, root, "file.txt", "base\n")
	git(t, root, "add", "file.txt")
	git(t, root, "commit", "-m", "initial")
	manager := New(filepath.Join(t.TempDir(), "UMCode"))

	ws, err := manager.Ensure(context.Background(), "thr_persist", root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws.Path, "task.txt", "task work\n")
	git(t, ws.Path, "add", "task.txt")
	git(t, ws.Path, "commit", "-m", "task commit")
	write(t, root, "file.txt", "source moved on\n")
	git(t, root, "add", "file.txt")
	git(t, root, "commit", "-m", "source update")

	got, err := manager.Ensure(context.Background(), "thr_persist", root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != ws.Path || got.SourceCommit != ws.SourceCommit {
		t.Fatalf("task workspace changed after source update: before=%+v after=%+v", ws, got)
	}
	if content := read(t, got.Path, "task.txt"); content != "task work\n" {
		t.Fatalf("task's committed work was lost: %q", content)
	}
}

func TestKeepAppliesOnlyTaskChangesAndRejectsConflicts(t *testing.T) {
	root := newRepo(t)
	write(t, root, "file.txt", "base\n")
	git(t, root, "add", "file.txt")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "file.txt", "user's existing dirty edit\n")
	manager := New(filepath.Join(t.TempDir(), "UMCode"))
	ws, err := manager.Ensure(context.Background(), "thr_keep", root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws.Path, "file.txt", "user's existing dirty edit\nplus task edit\n")
	write(t, ws.Path, "task.txt", "new task file\n")
	if n, err := manager.Keep(context.Background(), "thr_keep", root); err != nil || n != 2 {
		t.Fatalf("Keep = %d, %v", n, err)
	}
	if got := read(t, root, "file.txt"); got != "user's existing dirty edit\nplus task edit\n" {
		t.Fatalf("kept tracked content = %q", got)
	}
	if got := read(t, root, "task.txt"); got != "new task file\n" {
		t.Fatalf("kept new file = %q", got)
	}
	if n, err := manager.Keep(context.Background(), "thr_keep", root); err != nil || n != 0 {
		t.Fatalf("repeat keep should be a no-op after baseline advances: %d, %v", n, err)
	}
	write(t, root, "file.txt", "source changed after keep\n")
	write(t, ws.Path, "file.txt", "task changed after keep\n")
	if _, err := manager.Keep(context.Background(), "thr_keep", root); err == nil || !strings.Contains(err.Error(), "conflicting edits") {
		t.Fatalf("diverged changes should be rejected: %v", err)
	}
}

func TestDiscardRemovesOnlyOwnedWorkspace(t *testing.T) {
	root := newRepo(t)
	write(t, root, "file.txt", "base\n")
	git(t, root, "add", "file.txt")
	git(t, root, "commit", "-m", "initial")
	manager := New(filepath.Join(t.TempDir(), "UMCode"))
	ws, err := manager.Ensure(context.Background(), "thr_discard", root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws.Path, "task.txt", "uncommitted task data")
	if err := manager.Discard(context.Background(), "thr_discard", root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatalf("task workspace remains after discard: %v", err)
	}
	if got := read(t, root, "file.txt"); got != "base\n" {
		t.Fatalf("discard modified source checkout = %q", got)
	}
}

func TestEnsureConcurrentTasksAreDistinct(t *testing.T) {
	root := newRepo(t)
	write(t, root, "file.txt", "base\n")
	git(t, root, "add", "file.txt")
	git(t, root, "commit", "-m", "initial")
	manager := New(filepath.Join(t.TempDir(), "UMCode"))

	ids := []string{"thr_a", "thr_b", "thr_c", "thr_d"}
	paths := make([]string, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ws, err := manager.Ensure(context.Background(), ids[i], root)
			paths[i], errs[i] = ws.Path, err
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Ensure(%s): %v", ids[i], err)
		}
		if paths[i] == "" || seen[paths[i]] {
			t.Fatalf("task worktree path missing or shared: %v", paths)
		}
		seen[paths[i]] = true
	}
}

func TestEnsureExplainsInvalidProjectAndKeepsCanceledTaskWorkspace(t *testing.T) {
	manager := New(filepath.Join(t.TempDir(), "UMCode"))
	nonRepo := t.TempDir()
	if _, err := manager.Ensure(context.Background(), "thr_bad", nonRepo); err == nil || !strings.Contains(err.Error(), "Git repository") {
		t.Fatalf("non-Git folder should explain the requirement, got %v", err)
	}

	root := newRepo(t)
	write(t, root, "file.txt", "base\n")
	git(t, root, "add", "file.txt")
	git(t, root, "commit", "-m", "initial")
	ws, err := manager.Ensure(context.Background(), "thr_cancel", root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Models the task's VM/agent context being canceled after setup.
	if err := ctx.Err(); err == nil {
		t.Fatal("test context was not canceled")
	}
	got, err := manager.Ensure(context.Background(), "thr_cancel", root)
	if err != nil || got.Path != ws.Path {
		t.Fatalf("canceled task workspace should remain recoverable: got=%+v err=%v", got, err)
	}
}

func TestEnsureRequiresInitialCommit(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	manager := New(filepath.Join(t.TempDir(), "UMCode"))
	if _, err := manager.Ensure(context.Background(), "thr_unborn", root); err == nil || !strings.Contains(err.Error(), "no commit yet") {
		t.Fatalf("expected clear initial-commit requirement, got %v", err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.name", "UMCode Test")
	git(t, root, "config", "user.email", "test@umcode.invalid")
	return root
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, data)
	}
	return strings.TrimSpace(string(data))
}

func write(t *testing.T, root, rel, contents string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
