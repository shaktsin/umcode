package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskSnapshotPreservesVisibleDirtyState(t *testing.T) {
	root := newRepo(t)
	write(t, root, "staged.txt", "base\n")
	write(t, root, "unstaged.txt", "base\n")
	write(t, root, "deleted.txt", "base\n")
	write(t, root, "staged-deleted.txt", "base\n")
	write(t, root, ".gitignore", "ignored.txt\n")
	git(t, root, "add", "--all")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "staged.txt", "staged\n")
	git(t, root, "add", "staged.txt")
	write(t, root, "unstaged.txt", "unstaged\n")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "rm", "staged-deleted.txt")
	write(t, root, "UMCODE.md", "untracked required input\n")
	write(t, root, "ignored.txt", "excluded\n")
	beforeStatus := git(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	indexPath := git(t, root, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(root, indexPath)
	}
	beforeIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "snapshot")
	snap, err := CaptureTaskSnapshot(context.Background(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	if snap.SourceHead == "" || snap.BaseCommit == "" || snap.Manifest.IndexSHA256 == "" {
		t.Fatalf("incomplete snapshot: %+v", snap)
	}
	if snap.Manifest.IndexPaths["staged.txt"] == "" || snap.Manifest.IndexPaths["staged-deleted.txt"] != "" {
		t.Fatalf("index identities missing: %+v", snap.Manifest.IndexPaths)
	}
	if got := read(t, dest, "staged.txt"); got != "staged\n" {
		t.Fatalf("staged content %q", got)
	}
	if got := read(t, dest, "unstaged.txt"); got != "unstaged\n" {
		t.Fatalf("unstaged content %q", got)
	}
	if got := read(t, dest, "UMCODE.md"); got != "untracked required input\n" {
		t.Fatalf("untracked content %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dest, "deleted.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "staged-deleted.txt")); !os.IsNotExist(err) {
		t.Fatalf("staged deleted file copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored file copied: %v", err)
	}
	if got := git(t, root, "status", "--porcelain=v1", "--untracked-files=all"); got != beforeStatus {
		t.Fatalf("source status changed: %q => %q", beforeStatus, got)
	}
	afterIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterIndex) != string(beforeIndex) {
		t.Fatal("source index bytes changed")
	}
	if got := git(t, dest, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("snapshot not committed: %q", got)
	}
	if got := git(t, dest, "rev-parse", "HEAD"); got != snap.BaseCommit {
		t.Fatalf("snapshot HEAD %q != %q", got, snap.BaseCommit)
	}
	m := New(filepath.Dir(dest))
	// The manager uses a deterministic destination for an owned task parent.
	owned := m.Path("task-snapshot")
	ownedSnap, err := CaptureTaskSnapshot(context.Background(), root, owned)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := m.Lookup(context.Background(), "task-snapshot", root)
	if err != nil || ws.Path != owned || ws.BaselineCommit != ownedSnap.BaseCommit {
		t.Fatalf("owned lookup: %+v, %v", ws, err)
	}
}

func TestTaskSnapshotFromOrdinaryProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "UMCODE.md", "project instructions\n")
	write(t, root, "main.go", "package smoke\n")
	before, err := CaptureSourceManifest(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if before.Head != "" || len(before.Paths) != 2 {
		t.Fatalf("ordinary manifest: %+v", before)
	}
	dest := filepath.Join(t.TempDir(), "snapshot")
	snap, err := CaptureTaskSnapshot(t.Context(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	if snap.SourceHead != "" || snap.BaseCommit == "" || read(t, dest, "UMCODE.md") != "project instructions\n" {
		t.Fatalf("ordinary snapshot: %+v", snap)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("snapshot changed source Git state: %v", err)
	}
	if got := git(t, dest, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("snapshot dirty: %q", got)
	}
}

func TestTaskSnapshotOrdinaryProjectRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureTaskSnapshot(t.Context(), root, filepath.Join(t.TempDir(), "task")); err == nil {
		t.Fatal("escaping symlink entered the private task workspace")
	}
}

func TestTaskSnapshotRejectsUnsafeInputs(t *testing.T) {
	root := newRepo(t)
	write(t, root, "file.txt", "base\n")
	git(t, root, "add", "file.txt")
	git(t, root, "commit", "-m", "initial")
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureTaskSnapshot(context.Background(), root, filepath.Join(t.TempDir(), "snapshot")); err == nil {
		t.Fatal("accepted escaping symlink")
	}
}

func TestTaskSnapshotHonorsUserGlobalIgnore(t *testing.T) {
	root := newRepo(t)
	write(t, root, "tracked.txt", "base\n")
	git(t, root, "add", "tracked.txt")
	git(t, root, "commit", "-m", "initial")
	config := t.TempDir()
	ignore := filepath.Join(config, "ignore")
	if err := os.WriteFile(ignore, []byte("secret.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(config, "git", "config")
	if err := os.MkdirAll(filepath.Dir(gitConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitConfig, []byte("[core]\n\texcludesFile = "+ignore+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", config)
	write(t, root, "secret.txt", "excluded\n")
	write(t, root, "visible.txt", "included\n")
	dest := filepath.Join(t.TempDir(), "snapshot")
	snap, err := CaptureTaskSnapshot(context.Background(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.UntrackedPaths) != 1 || snap.UntrackedPaths[0] != "visible.txt" {
		t.Fatalf("unexpected untracked paths: %v", snap.UntrackedPaths)
	}
	if _, err := os.Lstat(filepath.Join(dest, "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("globally ignored file copied: %v", err)
	}
}
