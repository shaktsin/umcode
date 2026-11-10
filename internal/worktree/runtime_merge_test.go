package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskMergePreservesIndexAndUnrelatedEdits(t *testing.T) {
	root := newRepo(t)
	write(t, root, "a.txt", "original\n")
	write(t, root, "other.txt", "other\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")
	write(t, root, "a.txt", "user start\n")
	write(t, root, "UMCODE.md", "instructions\n")
	dest := filepath.Join(t.TempDir(), "task")
	snap, err := CaptureTaskSnapshot(context.Background(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dest, "a.txt", "agent result\n")
	write(t, dest, "new.txt", "new result\n")
	git(t, dest, "add", "-A")
	git(t, dest, "commit", "-m", "accepted")
	headTree := git(t, dest, "rev-parse", "HEAD^{tree}")
	indexPath := git(t, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "other.txt", "new unrelated edit\n")
	receipt, err := MergeTaskResult(context.Background(), root, snap, headTree)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Paths) != 2 {
		t.Fatalf("merged paths: %v", receipt.Paths)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "agent result\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(got) != "new result\n" {
		t.Fatalf("new.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "other.txt")); string(got) != "new unrelated edit\n" {
		t.Fatalf("unrelated edit = %q", got)
	}
	if got, _ := os.ReadFile(indexPath); string(got) != string(indexBefore) {
		t.Fatal("merge changed the index")
	}
}

func TestTaskMergeIntoOrdinaryProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "UMCODE.md", "instructions\n")
	write(t, root, "other.txt", "before\n")
	dest := filepath.Join(t.TempDir(), "task")
	snap, err := CaptureTaskSnapshot(t.Context(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dest, "result.go", "package smoke\n")
	git(t, dest, "add", "-A")
	git(t, dest, "commit", "-m", "accepted")
	write(t, root, "other.txt", "user concurrent edit\n")
	headTree := git(t, dest, "rev-parse", "HEAD^{tree}")
	if _, err := MergeTaskResult(t.Context(), root, snap, headTree); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "result.go"); got != "package smoke\n" {
		t.Fatalf("merged content %q", got)
	}
	if got := read(t, root, "other.txt"); got != "user concurrent edit\n" {
		t.Fatalf("unrelated edit %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("merge created source Git metadata: %v", err)
	}
}

func TestTaskMergeOrdinaryProjectRefusesConcurrentTargetEdit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package before\n")
	dest := filepath.Join(t.TempDir(), "task")
	snap, err := CaptureTaskSnapshot(t.Context(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dest, "main.go", "package agent\n")
	git(t, dest, "add", "-A")
	git(t, dest, "commit", "-m", "accepted")
	write(t, root, "main.go", "package user\n")
	headTree := git(t, dest, "rev-parse", "HEAD^{tree}")
	if _, err := MergeTaskResult(t.Context(), root, snap, headTree); !errors.Is(err, ErrParentChanged) {
		t.Fatalf("concurrent target edit was not refused: %v", err)
	}
	if got := read(t, root, "main.go"); got != "package user\n" {
		t.Fatalf("customer edit changed: %q", got)
	}
}

func TestTaskMergeRejectsReplacedSourceRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "main.go", "package before\n")
	dest := filepath.Join(t.TempDir(), "task")
	snap, err := CaptureTaskSnapshot(t.Context(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dest, "main.go", "package agent\n")
	git(t, dest, "add", "-A")
	git(t, dest, "commit", "-m", "accepted")
	outside := filepath.Join(parent, "outside")
	if err := os.Rename(root, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	headTree := git(t, dest, "rev-parse", "HEAD^{tree}")
	if _, err := MergeTaskResult(t.Context(), root, snap, headTree); err == nil {
		t.Fatal("merge accepted a replaced source root")
	}
	if got := read(t, outside, "main.go"); got != "package before\n" {
		t.Fatalf("outside content changed: %q", got)
	}
}

func TestMergeRollbackPreservesLaterUserEdit(t *testing.T) {
	project := t.TempDir()
	write(t, project, "main.go", "package agent\n")
	root, err := os.OpenRoot(project)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	after, err := readMergePathRoot(root, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	write(t, project, "main.go", "package user\n")
	backup := mergeBackup{path: filepath.Join(project, "main.go"), kind: "file", mode: 0644, data: []byte("package before\n")}
	if err := restoreMergeBackupIfUnchanged(root, project, backup, after); !errors.Is(err, ErrParentChanged) {
		t.Fatalf("rollback replaced concurrent edit: %v", err)
	}
	if got := read(t, project, "main.go"); got != "package user\n" {
		t.Fatalf("concurrent edit lost: %q", got)
	}
}

func TestMergeWriteRejectsTargetChangedDuringStaging(t *testing.T) {
	project := t.TempDir()
	write(t, project, "main.go", "package user\n")
	root, err := os.OpenRoot(project)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	c := TaskMergeChange{Path: "main.go", Before: SnapshotPath{Kind: "absent"}, After: SnapshotPath{Kind: "file", Mode: 0100644}, data: []byte("package agent\n")}
	if err := writeMergeTarget(root, c.Path, c); !errors.Is(err, ErrParentChanged) {
		t.Fatalf("changed target was overwritten: %v", err)
	}
	if got := read(t, project, "main.go"); got != "package user\n" {
		t.Fatalf("user content lost: %q", got)
	}
}

func TestRootedMergeWriteRejectsEscapingAncestor(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, "nested")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(project)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	c := TaskMergeChange{Path: "nested/result.txt", After: SnapshotPath{Kind: "file", Mode: 0100644}, data: []byte("agent result")}
	if err := writeMergeTarget(root, c.Path, c); err == nil {
		t.Fatal("merge followed escaping ancestor")
	}
	if _, err := os.Lstat(filepath.Join(outside, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("merge wrote outside project: %v", err)
	}
}

func TestTaskMergeRefusesTargetEditWithoutMutation(t *testing.T) {
	root := newRepo(t)
	write(t, root, "a.txt", "base\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")
	dest := filepath.Join(t.TempDir(), "task")
	snap, err := CaptureTaskSnapshot(context.Background(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dest, "a.txt", "agent\n")
	git(t, dest, "add", "-A")
	git(t, dest, "commit", "-m", "accepted")
	headTree := git(t, dest, "rev-parse", "HEAD^{tree}")
	write(t, root, "a.txt", "user concurrent\n")
	before := parentSnapshot(t, root)
	_, err = MergeTaskResult(context.Background(), root, snap, headTree)
	if !errors.Is(err, ErrParentChanged) {
		t.Fatalf("want target conflict, got %v", err)
	}
	assertUnchanged(t, root, before)
}

func TestTaskMergeRefusesUntrackedCollisionWithoutMutation(t *testing.T) {
	root := newRepo(t)
	write(t, root, "a.txt", "base\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")
	dest := filepath.Join(t.TempDir(), "task")
	snap, err := CaptureTaskSnapshot(context.Background(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dest, "new.txt", "agent\n")
	git(t, dest, "add", "-A")
	git(t, dest, "commit", "-m", "accepted")
	headTree := git(t, dest, "rev-parse", "HEAD^{tree}")
	write(t, root, "new.txt", "user\n")
	before := parentSnapshot(t, root)
	_, err = MergeTaskResult(context.Background(), root, snap, headTree)
	if !errors.Is(err, ErrParentChanged) {
		t.Fatalf("want collision, got %v", err)
	}
	assertUnchanged(t, root, before)
}
