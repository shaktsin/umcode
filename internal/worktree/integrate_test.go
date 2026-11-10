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
			_, err := scanRevision(context.Background(), ws.Path, f.base, result)
			if err == nil {
				err = checkSymlinks(context.Background(), f.parent, ws.Path, result+"^{tree}", nil)
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

// I1: symlinks resolving into repository metadata are as dangerous as ones
// escaping the checkout: writing through them rewrites config or hooks.
func TestApplyOnIntegratedRefusesLinksIntoGitMetadata(t *testing.T) {
	for _, target := range []string{".git/config", ".git", "sub/../.git", "sub/../.git/hooks", ".GIT", ".Git/config", ".git./config", "git~1/config", ".g\u200cit/config"} {
		t.Run(target, func(t *testing.T) {
			f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", "sub/x.go": "x\n"})
			result := f.agent(t, "agent", func(dir string) {
				if err := os.Symlink(target, filepath.Join(dir, "cfg")); err != nil {
					t.Fatal(err)
				}
			})
			before := parentSnapshot(t, f.parent)
			_, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
			if err == nil || !strings.Contains(err.Error(), "repository metadata") {
				t.Fatalf("symlink to %q must be refused as resolving into metadata, got %v", target, err)
			}
			assertUnchanged(t, f.parent, before)
		})
	}
}

// I1: plain paths with metadata-equivalent names (case, NTFS/HFS variants) are
// refused even though ordinary Git cannot check them out. The agent checkout's
// HEAD is moved to a plumbing-built commit, which hardened validation accepts.
func TestApplyOnIntegratedRefusesDotGitVariantPaths(t *testing.T) {
	for _, dir := range []string{".GIT", ".Git", ".git", "git~1", ".git. ", ".gi\u200ct"} {
		t.Run(dir, func(t *testing.T) {
			f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
			f.agent(t, "agent", func(string) {})
			ws, err := f.m.Lookup(context.Background(), "agent", f.parent)
			if err != nil {
				t.Fatal(err)
			}
			blob := gitStdin(t, ws.Path, "[core]\n", "hash-object", "-w", "--stdin")
			inner := gitStdin(t, ws.Path, "100644 blob "+blob+"\tconfig\n", "mktree")
			listing := git(t, ws.Path, "ls-tree", f.base+"^{tree}")
			tree := gitStdin(t, ws.Path, listing+"\n040000 tree "+inner+"\t"+dir+"\n", "mktree")
			result := git(t, ws.Path, "-c", "user.name=A", "-c", "user.email=a@b", "commit-tree", "-p", f.base, "-m", "x", tree)
			git(t, ws.Path, "update-ref", "HEAD", result)
			before := parentSnapshot(t, f.parent)
			_, err = f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
			if err == nil || !strings.Contains(err.Error(), "forbidden path") {
				t.Fatalf("path under %q should be refused as forbidden, got %v", dir, err)
			}
			assertUnchanged(t, f.parent, before)
		})
	}
}

func TestIsDotGitName(t *testing.T) {
	for name, want := range map[string]bool{
		".git": true, ".GIT": true, ".Git": true, ".git.": true, ".git ": true, ".git. .": true, "git~1": true, "GIT~1": true,
		".g\u200cit": true, "\ufeff.git": true, ".git::$INDEX_ALLOCATION": true, "sub\\.git": true, "sub\\x": false,
		".gitignore": false, ".github": false, "git": false, "x.git": false, ".gitmodules": false,
	} {
		if got := isDotGit(name); got != want {
			t.Fatalf("isDotGit(%q) = %v, want %v", name, got, want)
		}
	}
}

// I2: agent configuration that appears after the metadata check must never
// be executed by any later Git call, and the late change must be refused.
func TestApplyOnIntegratedConfigSwappedAfterValidation(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", ".gitattributes": "*.go filter=evil diff=evil\n"})
	result := f.agent(t, "agent", func(dir string) {
		write(t, dir, "a.go", "a2\n")
		if err := os.Symlink("a.go", filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
	})
	ws, err := f.m.Lookup(context.Background(), "agent", f.parent)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "pwned")
	cmd := "touch " + marker
	integrateHook = func(stage string) {
		if stage != "validated" {
			return
		}
		cfg, err := os.OpenFile(filepath.Join(ws.Path, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(cfg, "[core]\n\tfsmonitor = %s\n\tpager = %s\n\thooksPath = %s\n[diff]\n\texternal = %s\n[diff \"evil\"]\n\tcommand = %s\n\ttextconv = %s\n[filter \"evil\"]\n\tclean = %s\n\tsmudge = %s\n",
			cmd, cmd, t.TempDir(), cmd, cmd, cmd, cmd, cmd)
		if err := cfg.Close(); err != nil {
			t.Fatal(err)
		}
		// Make the dirty-check path tempting too: an unstaged edit would make
		// `git status` run fsmonitor and the clean filter.
		write(t, ws.Path, "a.go", "dirty\n")
	}
	defer func() { integrateHook = nil }()
	before := parentSnapshot(t, f.parent)
	_, err = f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
	if err == nil || !strings.Contains(err.Error(), "unsupported private Git configuration") {
		t.Fatalf("configuration changed after validation must be refused by the final metadata check, got %v", err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("agent configuration written after validation executed a command")
	}
	assertUnchanged(t, f.parent, before)
}

// M2: only genuine content conflicts are ConflictErrors; a held index lock is
// an ordinary (retryable) error and leaves the parent unchanged.
func TestApplyOnIntegratedIndexLockIsNotConflict(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
	result := f.agent(t, "agent", func(dir string) { write(t, dir, "a.go", "a2\n") })
	before := parentSnapshot(t, f.parent)
	lock := filepath.Join(f.parent, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
	var conflict *ConflictError
	if err == nil || errors.As(err, &conflict) {
		t.Fatalf("held index lock should be a plain error, got %v", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	assertUnchanged(t, f.parent, before)
	if _, err := f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result); err != nil {
		t.Fatalf("retry after the lock is released should apply: %v", err)
	}
}

// M2: binary preimage mismatches and add/add collisions are genuine content
// conflicts and must still be reported as ConflictError with their paths.
func TestApplyOnIntegratedBinaryAndAddConflicts(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n", "img.bin": "\x00\x01\x02base"})
	resultA := f.agent(t, "agent-a", func(dir string) {
		write(t, dir, "img.bin", "\x00\x01\x02from-a")
		write(t, dir, "new.go", "a\n")
	})
	resultB := f.agent(t, "agent-b", func(dir string) {
		write(t, dir, "img.bin", "\x00\x01\x02from-b")
		write(t, dir, "new.go", "b\n")
	})
	ctx := context.Background()
	applied, err := f.m.ApplyOnIntegrated(ctx, "agent-a", f.parent, f.base, f.baseTree(t), resultA)
	if err != nil {
		t.Fatal(err)
	}
	before := parentSnapshot(t, f.parent)
	_, err = f.m.ApplyOnIntegrated(ctx, "agent-b", f.parent, f.base, applied.TreeAfter, resultB)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || strings.Join(conflict.Paths, ",") != "img.bin,new.go" {
		t.Fatalf("want conflict on img.bin and new.go, got %v", err)
	}
	assertUnchanged(t, f.parent, before)
}

// The agent can rewrite its objects under the same ids between two reads
// (Git does not re-hash loose objects on read). Whatever the patch bytes
// finally contain is validated on the parent side before anything is applied.
func TestApplyOnIntegratedObjectSwapAfterScanIsRefused(t *testing.T) {
	outside := t.TempDir()
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
	result := f.agent(t, "agent", func(dir string) { write(t, dir, "a.go", "a2\n") })
	ws, err := f.m.Lookup(context.Background(), "agent", f.parent)
	if err != nil {
		t.Fatal(err)
	}
	// A second commit with an escaping symlink, stored loose in the agent.
	blob := gitStdin(t, ws.Path, outside, "hash-object", "-w", "--stdin")
	listing := git(t, ws.Path, "ls-tree", result+"^{tree}")
	tree := gitStdin(t, ws.Path, listing+"\n120000 blob "+blob+"\tescape\n", "mktree")
	evil := git(t, ws.Path, "-c", "user.name=A", "-c", "user.email=a@b", "commit-tree", "-p", f.base, "-m", "x", tree)
	object := func(id string) string { return filepath.Join(ws.Path, ".git", "objects", id[:2], id[2:]) }
	evilBytes, err := os.ReadFile(object(evil))
	if err != nil {
		t.Fatal(err)
	}
	integrateHook = func(stage string) {
		if stage != "scanned" {
			return
		}
		if err := os.Chmod(object(result), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(object(result), evilBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { integrateHook = nil }()
	before := parentSnapshot(t, f.parent)
	_, err = f.m.ApplyOnIntegrated(context.Background(), "agent", f.parent, f.base, f.baseTree(t), result)
	if err == nil || !strings.Contains(err.Error(), "unscanned path") {
		t.Fatalf("revision swapped after the scan must be refused by the parent-side check, got %v", err)
	}
	assertUnchanged(t, f.parent, before)
}

func TestClassifyApplyCheckFailure(t *testing.T) {
	paths := []string{"a.go", "b.go", "c.go"}
	cause := errors.New("exit status 1")
	for _, tc := range []struct {
		stderr   string
		conflict []string
	}{
		{"error: patch failed: a.go:1\nerror: a.go: patch does not apply\n", []string{"a.go"}},
		{"error: b.go: does not match index\n", nil},
		{"error: c.go: already exists in index\nerror: a.go: does not exist in index\n", []string{"a.go", "c.go"}},
		{"error: c.go: already exists in working directory\n", nil},
		{"error: affected file 'd/x' is beyond a symbolic link\n", nil},
		{"fatal: Unable to create '/p/.git/index.lock': File exists.\n", nil},
		{"error: patch failed: a.go:1\nerror: a.go: patch does not apply\nerror: b.go: Permission denied\n", nil},
		{"error: the patch applies to 'b.go' (1ff8), which does not match the current contents.\nerror: b.go: patch does not apply\n", []string{"b.go"}},
		{"error: a.go: wrong type\n", []string{"a.go"}},
		{"error: patch failed: a.go:1\nerror: a.go: patch does not apply\nerror: d.go: patch does not apply\n", nil}, // unknown path
		{"", nil},
	} {
		err := classifyApplyCheck(tc.stderr, paths, cause)
		if strings.Contains(tc.stderr, "does not match index") && !errors.Is(err, ErrParentChanged) {
			t.Fatalf("%q: want ErrParentChanged, got %v", tc.stderr, err)
		}
		var conflict *ConflictError
		if tc.conflict == nil {
			if errors.As(err, &conflict) || err == nil {
				t.Fatalf("%q: want plain error, got %v", tc.stderr, err)
			}
			continue
		}
		if !errors.As(err, &conflict) || strings.Join(conflict.Paths, ",") != strings.Join(tc.conflict, ",") {
			t.Fatalf("%q: want conflict on %v, got %v", tc.stderr, tc.conflict, err)
		}
	}
}

// I3: the single shared metadata validator is exported for delegation.
func TestValidateOwnedMetadataExported(t *testing.T) {
	f := newIntegrationFixture(t, map[string]string{"a.go": "a\n"})
	f.agent(t, "agent", func(string) {})
	ws, err := f.m.Lookup(context.Background(), "agent", f.parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnedMetadata(context.Background(), ws.Path); err != nil {
		t.Fatal(err)
	}
	gitfile := filepath.Join(t.TempDir(), "w")
	if err := os.MkdirAll(gitfile, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, gitfile, ".git", "gitdir: "+filepath.Join(ws.Path, ".git")+"\n")
	if err := ValidateOwnedMetadata(context.Background(), gitfile); err == nil {
		t.Fatal("a .git file redirecting elsewhere was accepted")
	}
	git(t, ws.Path, "config", "core.sshCommand", "touch x")
	if err := ValidateOwnedMetadata(context.Background(), ws.Path); err == nil || !strings.Contains(err.Error(), "core.sshcommand") {
		t.Fatalf("unsupported key accepted: %v", err)
	}
}
