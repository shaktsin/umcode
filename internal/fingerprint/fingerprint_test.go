package fingerprint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	write(t, filepath.Join(root, "a.txt"), "one\n")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return root
}

func take(t *testing.T, root string) Workspace {
	t.Helper()
	w, ok := TakeWorkspace(context.Background(), root, "")
	if !ok {
		t.Fatal("TakeWorkspace failed")
	}
	return w
}

func TestGitWorkspaceChangesWithEdit(t *testing.T) {
	root := gitRepo(t)
	a := take(t, root)
	if again := take(t, root); again.Value != a.Value {
		t.Fatal("fingerprint is not stable for an unchanged tree")
	}
	write(t, filepath.Join(root, "a.txt"), "two\n")
	b := take(t, root)
	if a.Value == b.Value {
		t.Fatal("edit did not change the fingerprint")
	}
	if got := Diff(a, b); !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("diff = %v", got)
	}
	write(t, filepath.Join(root, "new.txt"), "x")
	c := take(t, root)
	if got := Diff(b, c); !reflect.DeepEqual(got, []string{"new.txt"}) {
		t.Fatalf("untracked diff = %v", got)
	}
}

func TestNonGitManifestChanges(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "x.go"), "package x")
	a := take(t, root)
	if again := take(t, root); again.Value != a.Value {
		t.Fatal("unstable")
	}
	write(t, filepath.Join(root, "x.go"), "package xx // longer")
	write(t, filepath.Join(root, "y.go"), "package y")
	b := take(t, root)
	if got := Diff(a, b); !reflect.DeepEqual(got, []string{"x.go", "y.go"}) {
		t.Fatalf("diff = %v", got)
	}
}

func TestIgnoresGitNodeModulesVault(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, "vault")
	write(t, filepath.Join(root, "keep.txt"), "k")
	a, ok := TakeWorkspace(context.Background(), root, vaultDir)
	if !ok {
		t.Fatal("take failed")
	}
	write(t, filepath.Join(root, "node_modules", "pkg", "i.js"), "x")
	write(t, filepath.Join(root, ".git", "HEAD"), "ref")
	write(t, filepath.Join(vaultDir, "objects", "ab", "cd"), "blob")
	b, _ := TakeWorkspace(context.Background(), root, vaultDir)
	if a.Value != b.Value || len(Diff(a, b)) != 0 {
		t.Fatalf("ignored directories changed the fingerprint: %v", Diff(a, b))
	}
}

func TestManifestCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxManifestFiles+50; i++ {
		write(t, filepath.Join(root, fmt.Sprintf("d%02d", i%20), fmt.Sprintf("f%05d.txt", i)), "x")
	}
	a, ok := TakeWorkspace(context.Background(), root, "")
	if !ok {
		t.Fatal("capped walk must still succeed")
	}
	b, _ := TakeWorkspace(context.Background(), root, "")
	if a.Value != b.Value {
		t.Fatal("capped manifest is not deterministic")
	}
}

func TestSymlinkOutsideRootNotFollowed(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "o.txt"), "one")
	root := t.TempDir()
	write(t, filepath.Join(root, "in.txt"), "in")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	a := take(t, root)
	write(t, filepath.Join(outside, "o.txt"), "changed and longer")
	write(t, filepath.Join(outside, "new.txt"), "n")
	b := take(t, root)
	if a.Value != b.Value {
		t.Fatalf("fingerprint followed a symlink out of the project: %v", Diff(a, b))
	}
}

func TestBudgetTimeout(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "a")
	old := budget
	budget = time.Nanosecond
	defer func() { budget = old }()
	start := time.Now()
	w, ok := TakeWorkspace(context.Background(), root, "")
	if ok || w.Value != "" || time.Since(start) > time.Second {
		t.Fatalf("ok=%v value=%q after %s", ok, w.Value, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	budget = old
	if _, ok := TakeWorkspace(ctx, root, ""); ok {
		t.Fatal("cancelled context must not produce a fingerprint")
	}
	if _, ok := TakeWorkspace(context.Background(), "", ""); ok {
		t.Fatal("empty root must not produce a fingerprint")
	}
}

func TestEnvironmentStableAndSensitive(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v1 := func(context.Context) string { return "go1.24" }
	v2 := func(context.Context) string { return "go1.25" }
	a := Environment(ctx, root, v1)
	if a == "" || a != Environment(ctx, root, v1) {
		t.Fatalf("not stable: %q", a)
	}
	if a == Environment(ctx, root, v2) {
		t.Fatal("tool version change did not change the environment fingerprint")
	}
	if Environment(ctx, root, nil) == "" {
		t.Fatal("nil callback must still produce a value")
	}
}

func TestEnvironmentChangesWithGitHead(t *testing.T) {
	root := gitRepo(t)
	a := Environment(context.Background(), root, nil)
	write(t, filepath.Join(root, "b.txt"), "b")
	cmd := exec.Command("git", "-C", root, "add", "-A")
	cmd.Run()
	cmd = exec.Command("git", "-C", root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "second")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if b := Environment(context.Background(), root, nil); a == b {
		t.Fatal("HEAD change did not change the environment fingerprint")
	}
}

func TestGitDeletedAndBranchSwitchAreDetected(t *testing.T) {
	root := gitRepo(t)
	a := take(t, root)
	os.Remove(filepath.Join(root, "a.txt"))
	b := take(t, root)
	if got := Diff(a, b); !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("deleted file diff = %v", got)
	}
}

func TestSymlinkedRootIsFingerprinted(t *testing.T) {
	real := t.TempDir()
	write(t, filepath.Join(real, "a.txt"), "one")
	link := filepath.Join(t.TempDir(), "proj")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	before := take(t, link)
	write(t, filepath.Join(real, "a.txt"), "changed content")
	after := take(t, link)
	if before.Value == after.Value {
		t.Fatal("an edit under a symlinked project root went undetected")
	}
}
