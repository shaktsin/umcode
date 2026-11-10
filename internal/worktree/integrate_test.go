package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// integrationFixture is a clean parent repository with one base commit and a
// manager that owns agent checkouts created from it.
type integrationFixture struct {
	parent string
	base   string
	m      *Manager
}

func newIntegrationFixture(t *testing.T, files map[string]string) integrationFixture {
	t.Helper()
	parent := newRepo(t)
	for rel, contents := range files {
		write(t, parent, rel, contents)
	}
	git(t, parent, "add", "-A")
	git(t, parent, "commit", "-m", "base")
	return integrationFixture{parent: parent, base: git(t, parent, "rev-parse", "HEAD"), m: New(filepath.Join(t.TempDir(), "UMCode"))}
}

// agent creates an owned checkout for id at the fixture base, runs edit in it
// and commits everything; it returns the committed result revision.
func (f integrationFixture) agent(t *testing.T, id string, edit func(dir string)) string {
	t.Helper()
	ws, err := f.m.EnsureCommitted(context.Background(), id, f.parent, f.base)
	if err != nil {
		t.Fatal(err)
	}
	edit(ws.Path)
	git(t, ws.Path, "add", "-A")
	git(t, ws.Path, "-c", "user.name=Agent", "-c", "user.email=agent@umcode.invalid", "commit", "--quiet", "--allow-empty", "-m", "agent "+id)
	return git(t, ws.Path, "rev-parse", "HEAD")
}

func (f integrationFixture) baseTree(t *testing.T) string {
	return git(t, f.parent, "rev-parse", f.base+"^{tree}")
}

// parentSnapshot captures everything a failed integration must leave unchanged:
// HEAD, index tree, staged and unstaged diffs, status and every worktree byte
// and mode outside .git.
func parentSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "HEAD %s\n", git(t, root, "rev-parse", "HEAD"))
	fmt.Fprintf(&b, "tree %s\n", git(t, root, "write-tree"))
	fmt.Fprintf(&b, "cached %s\n", git(t, root, "diff", "--cached", "--binary"))
	fmt.Fprintf(&b, "diff %s\n", git(t, root, "diff", "--binary"))
	fmt.Fprintf(&b, "status %s\n", git(t, root, "status", "--porcelain", "--untracked-files=all", "--ignored"))
	var files []string
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
		entry := rel + " " + info.Mode().String()
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			entry += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			entry += " " + hex.EncodeToString(sum[:])
		}
		files = append(files, entry)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	b.WriteString(strings.Join(files, "\n"))
	return b.String()
}

func assertUnchanged(t *testing.T, root, before string) {
	t.Helper()
	if after := parentSnapshot(t, root); after != before {
		t.Fatalf("parent changed by a refused integration:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestApplyOnIntegratedTwoDisjointRevisions(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "package a\n\nvar A = 1\n", "b.go": "package b\n\nvar B = 1\n", ".gitignore": "*.log\n"})
	resultA := f.agent(t, "agent-a", func(dir string) { write(t, dir, "a.go", "package a\n\nvar A = 2\n") })
	resultB := f.agent(t, "agent-b", func(dir string) { write(t, dir, "b.go", "package b\n\nvar B = 2\n") })
	write(t, f.parent, "build.log", "ignored output is tolerated\n")
	ctx := context.Background()

	treeA, err := f.m.ApplyOnIntegrated(ctx, "agent-a", f.parent, f.base, f.baseTree(t), resultA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(treeA.Paths, ",") != "a.go" {
		t.Fatalf("paths = %v", treeA.Paths)
	}
	treeB, err := f.m.ApplyOnIntegrated(ctx, "agent-b", f.parent, f.base, treeA.TreeAfter, resultB)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(treeB.Paths, ",") != "b.go" {
		t.Fatalf("paths = %v", treeB.Paths)
	}

	// Reference: a manual merge of both edits in an independent clone.
	ref := filepath.Join(t.TempDir(), "ref")
	git(t, filepath.Dir(ref), "clone", "--quiet", f.parent, ref)
	write(t, ref, "a.go", "package a\n\nvar A = 2\n")
	write(t, ref, "b.go", "package b\n\nvar B = 2\n")
	git(t, ref, "add", "-A")
	want := git(t, ref, "write-tree")

	got, err := f.m.ParentTree(ctx, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || treeB.TreeAfter != want {
		t.Fatalf("integrated tree %s (reported %s), manual merge %s", got, treeB.TreeAfter, want)
	}
	if head := git(t, f.parent, "rev-parse", "HEAD"); head != f.base {
		t.Fatalf("parent HEAD moved to %s", head)
	}
	if status := git(t, f.parent, "status", "--porcelain", "--untracked-files=all"); status != "M  a.go\nM  b.go" {
		t.Fatalf("parent status should only show staged changes, got %q", status)
	}
	if err := f.m.CheckParentState(ctx, f.parent, f.base, want); err != nil {
		t.Fatalf("integrated parent should match its expected tree: %v", err)
	}
}

func TestApplyOnIntegratedOverlapIsConflict(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "package a\n\nvar A = 1\n", "b.go": "package b\n"})
	resultA := f.agent(t, "agent-a", func(dir string) { write(t, dir, "a.go", "package a\n\nvar A = 2\n") })
	resultB := f.agent(t, "agent-b", func(dir string) {
		write(t, dir, "a.go", "package a\n\nvar A = 3\n")
		write(t, dir, "b.go", "package b\n\n// B\n")
	})
	ctx := context.Background()
	applied, err := f.m.ApplyOnIntegrated(ctx, "agent-a", f.parent, f.base, f.baseTree(t), resultA)
	if err != nil {
		t.Fatal(err)
	}
	before := parentSnapshot(t, f.parent)
	_, err = f.m.ApplyOnIntegrated(ctx, "agent-b", f.parent, f.base, applied.TreeAfter, resultB)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	if strings.Join(conflict.Paths, ",") != "a.go" || conflict.Detail == "" {
		t.Fatalf("conflict should name only a.go with detail, got %+v", conflict)
	}
	assertUnchanged(t, f.parent, before)
	if got := read(t, f.parent, "b.go"); got != "package b\n" {
		t.Fatalf("non-conflicting part of the revision was partially applied: %q", got)
	}
	// Both versions remain intact: the agent checkout still holds its result.
	ws, err := f.m.Lookup(ctx, "agent-b", f.parent)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, ws.Path, "rev-parse", "HEAD"); got != resultB {
		t.Fatalf("agent checkout moved to %s", got)
	}
}

func TestApplyOnIntegratedWrongExpectedTree(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", "b.go": "b\n"})
	resultA := f.agent(t, "agent-a", func(dir string) { write(t, dir, "a.go", "a2\n") })
	resultB := f.agent(t, "agent-b", func(dir string) { write(t, dir, "b.go", "b2\n") })
	ctx := context.Background()
	if _, err := f.m.ApplyOnIntegrated(ctx, "agent-a", f.parent, f.base, f.baseTree(t), resultA); err != nil {
		t.Fatal(err)
	}
	before := parentSnapshot(t, f.parent)
	_, err := f.m.ApplyOnIntegrated(ctx, "agent-b", f.parent, f.base, f.baseTree(t), resultB)
	if !errors.Is(err, ErrParentChanged) || !strings.Contains(err.Error(), "a.go") {
		t.Fatalf("stale expected tree should be refused naming a.go, got %v", err)
	}
	assertUnchanged(t, f.parent, before)
}

func TestApplyOnIntegratedParentEditedRefused(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		edit       func(t *testing.T, parent string)
	}{
		{"tracked edit", "a.go", func(t *testing.T, parent string) { write(t, parent, "a.go", "user edit\n") }},
		{"untracked file", "notes.txt", func(t *testing.T, parent string) { write(t, parent, "notes.txt", "scratch\n") }},
		{"staged edit", "b.go", func(t *testing.T, parent string) {
			write(t, parent, "b.go", "staged\n")
			git(t, parent, "add", "b.go")
		}},
		{"head moved", "", func(t *testing.T, parent string) {
			git(t, parent, "commit", "--quiet", "--allow-empty", "-m", "moved")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", "b.go": "b\n", "c.go": "c\n"})
			result := f.agent(t, "agent", func(dir string) { write(t, dir, "c.go", "c2\n") })
			tc.edit(t, f.parent)
			before := parentSnapshot(t, f.parent)
			_, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
			if !errors.Is(err, ErrParentChanged) || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("expected parent-changed refusal naming %q, got %v", tc.path, err)
			}
			if err := f.m.CheckParentState(context.Background(), f.parent, f.base, f.baseTree(t)); !errors.Is(err, ErrParentChanged) {
				t.Fatalf("CheckParentState should refuse too, got %v", err)
			}
			assertUnchanged(t, f.parent, before)
		})
	}
}

func TestApplyOnIntegratedBinaryDeleteRenameMode(t *testing.T) {
	binary := string([]byte{0, 1, 2, 3, 0xff, 0xfe, 0, 'x', '\n'})
	f := newIntegrationFixture(t, map[string]string{
		"bin.dat": binary, "gone.txt": "delete me\n", "old.txt": "rename me\nline two\n", "run.sh": "#!/bin/sh\necho hi\n", "keep.txt": "keep\n",
	})
	result := f.agent(t, "agent", func(dir string) {
		write(t, dir, "bin.dat", string([]byte{9, 0, 8, 0, 7, 0xff, '\n'}))
		if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "mv", "old.txt", "new.txt")
		if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "new.bin", string([]byte{0, 0, 0, 1}))
	})
	ws, err := f.m.Lookup(context.Background(), "agent", f.parent)
	if err != nil {
		t.Fatal(err)
	}
	want := git(t, ws.Path, "rev-parse", result+"^{tree}")
	got, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
	if err != nil {
		t.Fatal(err)
	}
	if got.TreeAfter != want {
		t.Fatalf("integrated tree %s, agent result tree %s", got.TreeAfter, want)
	}
	if strings.Join(got.Paths, ",") != "bin.dat,gone.txt,new.bin,new.txt,old.txt,run.sh" {
		t.Fatalf("paths = %v", got.Paths)
	}
	if read(t, f.parent, "bin.dat") != string([]byte{9, 0, 8, 0, 7, 0xff, '\n'}) || read(t, f.parent, "new.txt") != "rename me\nline two\n" {
		t.Fatal("binary or renamed content differs in the parent working tree")
	}
	for _, gone := range []string{"gone.txt", "old.txt"} {
		if _, err := os.Lstat(filepath.Join(f.parent, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s should be removed, stat err %v", gone, err)
		}
	}
	if info, err := os.Stat(filepath.Join(f.parent, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh should be executable: %v %v", info, err)
	}
	if mode := git(t, f.parent, "ls-files", "-s", "run.sh"); !strings.HasPrefix(mode, "100755 ") {
		t.Fatalf("index mode = %q", mode)
	}
	if err := f.m.CheckParentState(context.Background(), f.parent, f.base, want); err != nil {
		t.Fatal(err)
	}
}

func TestApplyOnIntegratedRefusesDotGitSymlinkSubmodule(t *testing.T) {
	outside := t.TempDir()
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, dir string)
	}{
		{"absolute symlink", func(t *testing.T, dir string) {
			if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
				t.Fatal(err)
			}
		}},
		{"relative symlink", func(t *testing.T, dir string) {
			if err := os.Symlink("../../../../../../../../..", filepath.Join(dir, "sub", "escape")); err != nil {
				t.Fatal(err)
			}
		}},
		{"gitlink", func(t *testing.T, dir string) {
			nested := filepath.Join(dir, "vendor", "lib")
			write(t, nested, "lib.go", "lib\n")
			git(t, nested, "init", "--quiet")
			git(t, nested, "add", "-A")
			git(t, nested, "-c", "user.name=A", "-c", "user.email=a@b", "commit", "--quiet", "-m", "lib")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", "sub/x.go": "x\n"})
			result := f.agent(t, "agent", func(dir string) { tc.edit(t, dir) })
			if result == f.base || git(t, f.parent, "rev-parse", f.base+"^{tree}") == gitInWorkspace(t, f, "agent", "rev-parse", result+"^{tree}") {
				t.Fatal("fixture revision is empty; the forbidden entry was not committed")
			}
			before := parentSnapshot(t, f.parent)
			if _, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result); err == nil {
				t.Fatal("forbidden revision was applied")
			}
			assertUnchanged(t, f.parent, before)
		})
	}
}

// Revisions that ordinary Git refuses to check out (a .git path) are built with
// plumbing so the diff scanner itself is exercised on real Git output.
func TestIntegrationScanRefusesForbiddenEntries(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", "sub/x.go": "x\n"})
	ws, err := f.m.EnsureCommitted(context.Background(), "agent", f.parent, f.base)
	if err != nil {
		t.Fatal(err)
	}
	commitWith := func(entry string) string {
		t.Helper()
		baseTree := git(t, ws.Path, "rev-parse", f.base+"^{tree}")
		listing := git(t, ws.Path, "ls-tree", baseTree)
		tree := gitStdin(t, ws.Path, listing+"\n"+entry+"\n", "mktree")
		return git(t, ws.Path, "-c", "user.name=A", "-c", "user.email=a@b", "commit-tree", "-p", f.base, "-m", "x", tree)
	}
	blob := func(contents string) string { return gitStdin(t, ws.Path, contents, "hash-object", "-w", "--stdin") }
	dotGit := gitStdin(t, ws.Path, "100644 blob "+blob("[core]\n")+"\tconfig\n", "mktree")
	upperGit := gitStdin(t, ws.Path, "100644 blob "+blob("x")+"\thooks\n", "mktree")
	cases := map[string]string{
		".git directory":  "040000 tree " + dotGit + "\t.git",
		".GIT directory":  "040000 tree " + upperGit + "\t.GIT",
		"gitlink":         "160000 commit " + f.base + "\tmodule",
		"absolute link":   "120000 blob " + blob("/etc") + "\tabs",
		"dotdot link":     "120000 blob " + blob("sub/../..") + "\tup",
		"long link":       "120000 blob " + blob(strings.Repeat("a/", 3000)) + "\tlong",
		"inside is legal": "120000 blob " + blob("sub/x.go") + "\tok",
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			result := commitWith(entry)
			entries, err := scanRevision(context.Background(), ws.Path, f.base, result)
			if err == nil {
				err = checkSymlinks(context.Background(), f.parent, ws.Path, f.baseTree(t), entries)
			}
			if name == "inside is legal" {
				if err != nil {
					t.Fatalf("inside symlink refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s accepted", name)
			}
		})
	}
}

func TestResolveLinkTargetFollowsChains(t *testing.T) {
	links := map[string]string{"x/d": "..", "loop1": "loop2", "loop2": "loop1", "in": "x"}
	lookup := func(p string) (bool, string, error) {
		target, ok := links[p]
		return ok, target, nil
	}
	for target, escapes := range map[string]bool{
		"x/d/..":    true,  // lexically x, actually the repository's parent
		"x/d/x":     false, // x/d is the root, so this is x
		"in/d/a":    false,
		"in/d/../b": true,
		"loop1/a":   true, // unresolvable loops are refused
		"../a":      true,
		"/abs":      true,
		"x/./y/../": false,
	} {
		err := resolveLink("l", target, lookup)
		if (err != nil) != escapes {
			t.Fatalf("target %q: err = %v, want escape=%v", target, err, escapes)
		}
	}
}

func TestApplyOnIntegratedRefusesHostileAgentConfig(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
	result := f.agent(t, "agent", func(dir string) { write(t, dir, "a.go", "a2\n") })
	ws, err := f.m.Lookup(context.Background(), "agent", f.parent)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "pwned")
	cfg, err := os.OpenFile(filepath.Join(ws.Path, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(cfg, "[core]\n\tfsmonitor = touch %s\n[diff]\n\texternal = touch %s\n", marker, marker)
	if err := cfg.Close(); err != nil {
		t.Fatal(err)
	}
	before := parentSnapshot(t, f.parent)
	if _, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result); err == nil {
		t.Fatal("agent checkout with hostile configuration was integrated")
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("agent-controlled configuration executed a command")
	}
	assertUnchanged(t, f.parent, before)
}

func TestApplyOnIntegratedEmptyPatchIsNoop(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
	result := f.agent(t, "agent", func(string) {})
	before := parentSnapshot(t, f.parent)
	got, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
	if err != nil {
		t.Fatal(err)
	}
	if got.TreeAfter != f.baseTree(t) || len(got.Paths) != 0 {
		t.Fatalf("empty revision result = %+v", got)
	}
	assertUnchanged(t, f.parent, before)
}

func TestApplyOnIntegratedGuardRunsTwice(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", "b.go": "b\n"})
	resultA := f.agent(t, "agent-a", func(dir string) { write(t, dir, "a.go", "a2\n") })
	resultB := f.agent(t, "agent-b", func(dir string) { write(t, dir, "b.go", "b2\n") })
	calls := 0
	applied, err := f.m.ApplyOnIntegrated(context.Background(), "agent-a", f.parent, f.base, f.baseTree(t), resultA, func() error {
		calls++
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("guard calls = %d, err = %v", calls, err)
	}

	before := parentSnapshot(t, f.parent)
	stop := errors.New("lead lock lost")
	calls = 0
	_, err = f.m.ApplyOnIntegrated(context.Background(), "agent-b", f.parent, f.base, applied.TreeAfter, resultB, func() error {
		calls++
		if calls == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || calls != 2 {
		t.Fatalf("second guard failure should abort, calls=%d err=%v", calls, err)
	}
	assertUnchanged(t, f.parent, before)

	calls = 0
	if _, err = f.m.ApplyOnIntegrated(context.Background(), "agent-b", f.parent, f.base, applied.TreeAfter, resultB, func() error {
		calls++
		return stop
	}); !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("first guard failure should abort before any check, calls=%d err=%v", calls, err)
	}
	assertUnchanged(t, f.parent, before)
}

func TestApplyOnIntegratedCancelledBeforeMutation(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
	result := f.agent(t, "agent", func(dir string) { write(t, dir, "a.go", "a2\n") })
	before := parentSnapshot(t, f.parent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.m.ApplyOnIntegrated(ctx, "agent", f.parent, f.base, f.baseTree(t), result); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	assertUnchanged(t, f.parent, before)

	// Cancellation observed by the second guard (after --check) also aborts.
	ctx, cancel = context.WithCancel(context.Background())
	calls := 0
	if _, err := f.m.ApplyOnIntegrated(ctx, "agent", f.parent, f.base, f.baseTree(t), result, func() error {
		if calls++; calls == 2 {
			cancel()
		}
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled after late cancellation, got %v", err)
	}
	assertUnchanged(t, f.parent, before)
}

func TestParentTreeRefusesIndexFlags(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
	tree, err := f.m.ParentTree(context.Background(), f.parent)
	if err != nil || tree != f.baseTree(t) {
		t.Fatalf("tree = %s err = %v", tree, err)
	}
	git(t, f.parent, "update-index", "--assume-unchanged", "a.go")
	if _, err := f.m.ParentTree(context.Background(), f.parent); err == nil {
		t.Fatal("assume-unchanged index flag was accepted")
	}
}

func gitInWorkspace(t *testing.T, f integrationFixture, id string, args ...string) string {
	t.Helper()
	ws, err := f.m.Lookup(context.Background(), id, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	return git(t, ws.Path, args...)
}

func gitStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, data)
	}
	return strings.TrimSpace(string(data))
}
