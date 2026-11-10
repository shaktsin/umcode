package worktree

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
	"testing"
	"time"
)

// checkoutBytes fingerprints a project checkout byte for byte: HEAD, the raw
// .git/index file and every working tree entry (path, mode, content or link
// target). Unlike parentSnapshot it runs no Git command that could rewrite
// the index.
func checkoutBytes(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	head, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "HEAD %s\n", head)
	index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "index %x\n", index)
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
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " -> %s", target)
		case info.Mode().IsRegular():
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

// integratedFixture is a parent with one agent revision applied (staged) by
// ApplyOnIntegrated: binary change, deletion, rename and mode change.
func integratedFixture(t *testing.T) (integrationFixture, string) {
	t.Helper()
	f := newIntegrationFixture(t, map[string]string{"keep.txt": "keep\n", "gone.txt": "gone\n", "old.txt": "rename me\n", "run.sh": "echo hi\n", "bin.dat": "\x00\x01base"})
	result := f.agent(t, "writer", func(dir string) {
		write(t, dir, "bin.dat", "\x00\x02\xffchanged")
		if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(dir, "old.txt"), filepath.Join(dir, "new.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "added/deep.txt", "added\n")
	})
	res, err := f.m.ApplyOnIntegrated(context.Background(), "writer", f.parent, f.base, f.baseTree(t), result)
	if err != nil {
		t.Fatal(err)
	}
	return f, res.TreeAfter
}

func TestParentStateForRuntimeAcceptsBaseOrHead(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationFixture(t, map[string]string{"a.txt": "a\n"})
	if err := ParentStateForRuntime(ctx, f.parent, f.base, ""); err != nil {
		t.Fatalf("clean parent at base: %v", err)
	}
	if err := ParentStateForRuntime(ctx, f.parent, f.base, f.baseTree(t)); err != nil {
		t.Fatalf("clean parent at base with head = base tree: %v", err)
	}
	f, head := integratedFixture(t)
	if err := ParentStateForRuntime(ctx, f.parent, f.base, head); err != nil {
		t.Fatalf("parent at the integrated head: %v", err)
	}
	// The staged state is not the recorded head.
	if err := ParentStateForRuntime(ctx, f.parent, f.base, f.baseTree(t)); err == nil || !strings.Contains(err.Error(), "bin.dat") {
		t.Fatalf("staged state that is not the head: %v", err)
	}
	if err := ParentStateForRuntime(ctx, f.parent, f.base, ""); err == nil || !strings.HasPrefix(err.Error(), cleanCheckoutRefusal) {
		t.Fatalf("staged state with no head: %v", err)
	}
}

func TestPrepareRuntimeRefusesDirtyOrMovedParent(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		dirty func(t *testing.T, f integrationFixture)
		want  []string
	}{
		{"unrelated edit", func(t *testing.T, f integrationFixture) { write(t, f.parent, "keep.txt", "user edit\n") }, []string{cleanCheckoutRefusal, "keep.txt"}},
		{"untracked file", func(t *testing.T, f integrationFixture) { write(t, f.parent, "notes.txt", "mine\n") }, []string{cleanCheckoutRefusal, "notes.txt"}},
		{"unrelated staged edit", func(t *testing.T, f integrationFixture) {
			write(t, f.parent, "keep.txt", "user staged\n")
			git(t, f.parent, "add", "keep.txt")
		}, []string{cleanCheckoutRefusal, "keep.txt"}},
		{"moved HEAD", func(t *testing.T, f integrationFixture) {
			moved := gitStdin(t, f.parent, "moved\n", "commit-tree", f.baseTree(t), "-p", f.base)
			git(t, f.parent, "update-ref", "HEAD", moved)
		}, []string{"delegation parent revision changed", f0base}},
		{"conflict stages", func(t *testing.T, f integrationFixture) {
			blob := func(s string) string { return gitStdin(t, f.parent, s, "hash-object", "-w", "--stdin") }
			zero := strings.Repeat("0", len(f.base))
			info := fmt.Sprintf("0 %s\tkeep.txt\n100644 %s 1\tkeep.txt\n100644 %s 2\tkeep.txt\n100644 %s 3\tkeep.txt\n", zero, blob("keep\n"), blob("ours\n"), blob("theirs\n"))
			gitStdin(t, f.parent, info, "update-index", "--index-info")
		}, []string{cleanCheckoutRefusal, "keep.txt", "conflict"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, head := integratedFixture(t)
			c.dirty(t, f)
			before := checkoutBytes(t, f.parent)
			err := ParentStateForRuntime(ctx, f.parent, f.base, head)
			for _, want := range c.want {
				if want == f0base {
					want = f.base
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("ParentStateForRuntime = %v, want %q", err, want)
				}
			}
			if _, err = f.m.EnsureRuntime(ctx, "late", f.parent, f.base, head); err == nil {
				t.Fatal("EnsureRuntime accepted a dirty or moved parent")
			}
			if _, statErr := os.Lstat(f.m.Path("late")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("refused EnsureRuntime left a checkout: %v", statErr)
			}
			if after := checkoutBytes(t, f.parent); after != before {
				t.Fatalf("refusal changed the parent:\n%s\n---\n%s", before, after)
			}
		})
	}
}

// f0base stands for the fixture's base revision in a case's wanted text.
const f0base = "<base>"

func TestEnsureRuntimeAfterIntegrationClonesBase(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	before := checkoutBytes(t, f.parent)
	ws, err := f.m.EnsureRuntime(ctx, "late-writer", f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	if after := checkoutBytes(t, f.parent); after != before {
		t.Fatalf("preparing a workspace changed the parent:\n%s\n---\n%s", before, after)
	}
	if got := git(t, ws.Path, "rev-parse", "HEAD"); got != f.base {
		t.Fatalf("workspace HEAD = %s, want base %s", got, f.base)
	}
	if got := git(t, ws.Path, "write-tree"); got != f.baseTree(t) {
		t.Fatalf("workspace tree = %s, want base tree", got)
	}
	if st := git(t, ws.Path, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("workspace not clean: %q", st)
	}
	if ws.SourceCommit != f.base {
		t.Fatalf("source commit %s", ws.SourceCommit)
	}
	if again, err := f.m.EnsureRuntime(ctx, "late-writer", f.parent, f.base, head); err != nil || again.Path != ws.Path {
		t.Fatalf("second EnsureRuntime = %+v %v", again, err)
	}
	if _, err = f.m.Lookup(ctx, "late-writer", f.parent); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	// A clean parent at base still works as before.
	g := newIntegrationFixture(t, map[string]string{"a.txt": "a\n"})
	if _, err = g.m.EnsureRuntime(ctx, "first", g.parent, g.base, g.baseTree(t)); err != nil {
		t.Fatalf("clean parent: %v", err)
	}
}

func TestRuntimeViewHasIntegratedTreeExactly(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	before := checkoutBytes(t, f.parent)
	commit, err := RuntimeViewCommit(ctx, f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := RuntimeViewCommit(ctx, f.parent, f.base, head); err != nil || again != commit {
		t.Fatalf("view commit is not deterministic: %s %s %v", commit, again, err)
	}
	if err := exec.Command("git", "-C", f.parent, "cat-file", "-e", commit).Run(); err == nil {
		t.Fatal("RuntimeViewCommit wrote the commit into the parent")
	}
	ws, err := f.m.EnsureRuntime(ctx, "view", f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.ApplyRuntimeView(ctx, "view", f.parent, f.base, head, commit); err != nil {
		t.Fatal(err)
	}
	if after := checkoutBytes(t, f.parent); after != before {
		t.Fatalf("preparing a view changed the parent:\n%s\n---\n%s", before, after)
	}
	if got := git(t, ws.Path, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("view HEAD = %s, want %s", got, commit)
	}
	if got := git(t, ws.Path, "rev-parse", "HEAD^{tree}"); got != head {
		t.Fatalf("view tree = %s, want head %s", got, head)
	}
	if got := git(t, ws.Path, "write-tree"); got != head {
		t.Fatalf("view index tree = %s, want head %s", got, head)
	}
	if st := git(t, ws.Path, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("view not clean: %q", st)
	}
	if err = ValidateCommitted(ctx, ws.Path, commit); err != nil {
		t.Fatalf("view is not a clean committed checkout: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws.Path, "bin.dat"))
	if err != nil || !bytes.Equal(data, []byte("\x00\x02\xffchanged")) {
		t.Fatalf("binary = %q %v", data, err)
	}
	for _, gone := range []string{"gone.txt", "old.txt"} {
		if _, err := os.Lstat(filepath.Join(ws.Path, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still present: %v", gone, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(ws.Path, "new.txt")); err != nil || string(data) != "rename me\n" {
		t.Fatalf("renamed = %q %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(ws.Path, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode change lost: %v %v", info, err)
	}
	if err = ValidateOwnedMetadata(ctx, ws.Path); err != nil {
		t.Fatal(err)
	}
	// Idempotent once applied.
	if err = f.m.ApplyRuntimeView(ctx, "view", f.parent, f.base, head, commit); err != nil {
		t.Fatalf("repeat apply: %v", err)
	}
	if err = f.m.DiscardCommitted(ctx, "view", f.parent, commit); err != nil {
		t.Fatalf("discard view: %v", err)
	}
}

func TestRuntimeViewFailsClosed(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	commit, err := RuntimeViewCommit(ctx, f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	// A wrong expected commit (for another head) is refused and the view is
	// not committed.
	ws, err := f.m.EnsureRuntime(ctx, "wrong", f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	other, err := RuntimeViewCommit(ctx, f.parent, f.base, f.baseTree(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.ApplyRuntimeView(ctx, "wrong", f.parent, f.base, head, other); err == nil {
		t.Fatal("view with a mismatching expected commit accepted")
	}
	if got := git(t, ws.Path, "rev-parse", "HEAD"); got == other || got == commit {
		t.Fatalf("refused view moved HEAD to %s", got)
	}
	// Hostile metadata in the view checkout is refused before any write.
	ws2, err := f.m.EnsureRuntime(ctx, "hostile", f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	git(t, ws2.Path, "config", "core.pager", "touch /tmp/umcode-pwned")
	if err = f.m.ApplyRuntimeView(ctx, "hostile", f.parent, f.base, head, commit); err == nil || !strings.Contains(err.Error(), "core.pager") {
		t.Fatalf("hostile view metadata: %v", err)
	}
	if got := git(t, ws2.Path, "rev-parse", "HEAD"); got != f.base {
		t.Fatalf("hostile view moved HEAD to %s", got)
	}
	// A view checkout that is no longer the clean base is not reused.
	ws3, err := f.m.EnsureRuntime(ctx, "dirty", f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws3.Path, "keep.txt", "tampered\n")
	if err = f.m.ApplyRuntimeView(ctx, "dirty", f.parent, f.base, head, commit); err == nil {
		t.Fatal("dirty view checkout accepted")
	}
}

// I1: a stat-only change (an editor saving an unchanged file) must not make
// the read-only parent check refresh and rewrite the project's index.
func TestParentStateForRuntimeNeverRewritesIndex(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	later := time.Now().Add(2 * time.Hour)
	for _, rel := range []string{"keep.txt", "new.txt", "bin.dat"} {
		if err := os.Chtimes(filepath.Join(f.parent, rel), later, later); err != nil {
			t.Fatal(err)
		}
	}
	before := checkoutBytes(t, f.parent)
	if err := ParentStateForRuntime(ctx, f.parent, f.base, head); err != nil {
		t.Fatalf("stat-only change refused: %v", err)
	}
	if _, err := f.m.EnsureRuntime(ctx, "writer2", f.parent, f.base, head); err != nil {
		t.Fatal(err)
	}
	commit, err := RuntimeViewCommit(ctx, f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.EnsureRuntime(ctx, "view2", f.parent, f.base, head); err != nil {
		t.Fatal(err)
	}
	if err = f.m.ApplyRuntimeView(ctx, "view2", f.parent, f.base, head, commit); err != nil {
		t.Fatal(err)
	}
	if after := checkoutBytes(t, f.parent); after != before {
		t.Fatalf("preparing workspaces rewrote the parent (index or files):\n%s\n---\n%s", before, after)
	}
	if _, err := os.Lstat(filepath.Join(f.parent, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index.lock left behind: %v", err)
	}
	// A real content change with the same size is still refused.
	write(t, f.parent, "keep.txt", "KEEP\n")
	if err := ParentStateForRuntime(ctx, f.parent, f.base, head); err == nil || !strings.Contains(err.Error(), "keep.txt") {
		t.Fatalf("content edit not refused: %v", err)
	}
}

// M1: the parent's fsmonitor hook (user config) is never run by the check.
func TestParentStateForRuntimeRunsNoParentHooks(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, f.parent, "config", "core.fsmonitor", hook)
	if err := ParentStateForRuntime(ctx, f.parent, f.base, head); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.EnsureRuntime(ctx, "w", f.parent, f.base, head); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parent fsmonitor hook ran: %v", err)
	}
}

// M2: the clone ignores inherited GIT_* variables and runs no global hooks.
func TestEnsureRuntimeIgnoresGitEnvAndGlobalHooks(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	decoy := filepath.Join(t.TempDir(), "decoy.git")
	home := t.TempDir()
	hooks := filepath.Join(home, "hooks")
	marker := filepath.Join(home, "hook-ran")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"post-checkout", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_DIR", decoy)
	t.Setenv("GIT_WORK_TREE", f.parent)
	ws, err := f.m.EnsureRuntime(ctx, "env-writer", f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(decoy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clone honoured GIT_DIR: %v", err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("global hook ran during the clone: %v", err)
	}
	if err = ValidateOwnedMetadata(ctx, ws.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(ws.Path, ".git", "HEAD")); err != nil {
		t.Fatalf("workspace metadata missing: %v", err)
	}
}

// M9: views of type changes (file -> symlink, file -> directory) and of
// content under eol .gitattributes equal the head exactly.
func TestRuntimeViewTypeChangesAndEOL(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationFixture(t, map[string]string{"keep.txt": "keep\n", "link": "was a file\n", "dir": "was a file\n",
		".gitattributes": "*.crlf text eol=crlf\n", "a.crlf": "one\ntwo\n"})
	result := f.agent(t, "writer", func(dir string) {
		if err := os.Remove(filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("keep.txt", filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "dir")); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "dir/inner.txt", "inner\n")
		write(t, dir, "a.crlf", "one\r\ntwo\r\nthree\r\n")
	})
	res, err := f.m.ApplyOnIntegrated(ctx, "writer", f.parent, f.base, f.baseTree(t), result)
	if err != nil {
		t.Fatal(err)
	}
	head := res.TreeAfter
	before := checkoutBytes(t, f.parent)
	commit, err := RuntimeViewCommit(ctx, f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.m.EnsureRuntime(ctx, "view", f.parent, f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.ApplyRuntimeView(ctx, "view", f.parent, f.base, head, commit); err != nil {
		t.Fatal(err)
	}
	if after := checkoutBytes(t, f.parent); after != before {
		t.Fatal("view preparation changed the parent")
	}
	if got := git(t, ws.Path, "rev-parse", "HEAD^{tree}"); got != head {
		t.Fatalf("view tree %s, want %s", got, head)
	}
	if err = ValidateCommitted(ctx, ws.Path, commit); err != nil {
		t.Fatalf("view not clean: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(ws.Path, "link")); err != nil || target != "keep.txt" {
		t.Fatalf("link = %q %v", target, err)
	}
	if data, err := os.ReadFile(filepath.Join(ws.Path, "dir", "inner.txt")); err != nil || string(data) != "inner\n" {
		t.Fatalf("dir/inner.txt = %q %v", data, err)
	}
	if blob := git(t, ws.Path, "cat-file", "-p", "HEAD:a.crlf"); blob != "one\ntwo\nthree" {
		t.Fatalf("a.crlf blob = %q", blob)
	}
}

// N1: a project owned by another user that is trusted through the user's
// global safe.directory (containers, CI, WSL) keeps working for the legacy
// committed checks and for the runtime parent check, which pass
// safe.directory for exactly the repository they operate on.
func TestHardenedChecksHonourOwnershipException(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown the repository to another user")
	}
	ctx := context.Background()
	f, head := integratedFixture(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[safe]\n\tdirectory = *\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := filepath.WalkDir(f.parent, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, 65534, 65534)
	}); err != nil {
		t.Skipf("chown unavailable: %v", err)
	}
	if err := ValidateIndexFlags(ctx, f.parent); err != nil {
		t.Fatalf("ValidateIndexFlags: %v", err)
	}
	if err := ParentStateForRuntime(ctx, f.parent, f.base, head); err != nil {
		t.Fatalf("ParentStateForRuntime: %v", err)
	}
	g := newIntegrationFixture(t, map[string]string{"a.txt": "a\n"})
	if err := filepath.WalkDir(g.parent, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, 65534, 65534)
	}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommitted(ctx, g.parent, g.base); err != nil {
		t.Fatalf("ValidateCommitted: %v", err)
	}
	if _, err := g.m.EnsureCommitted(ctx, "legacy", g.parent, g.base); err != nil {
		t.Fatalf("EnsureCommitted: %v", err)
	}
}

// N2: the user's own global excludes file is honoured by the runtime parent
// checks (a globally ignored .DS_Store does not block), and nothing else of
// the global configuration is loaded.
func TestParentChecksHonourUserGlobalExcludes(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"configured", "xdg-default", "missing-file"} {
		t.Run(mode, func(t *testing.T) {
			f, head := integratedFixture(t)
			home := t.TempDir()
			xdg := filepath.Join(home, "xdg")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			switch mode {
			case "configured":
				write(t, home, "my-ignore", ".DS_Store\n")
				write(t, home, ".gitconfig", "[core]\n\texcludesFile = ~/my-ignore\n")
			case "xdg-default":
				write(t, xdg, "git/ignore", ".DS_Store\n")
			case "missing-file":
				write(t, home, ".gitconfig", "[core]\n\texcludesFile = ~/no-such-file\n")
			}
			write(t, f.parent, ".DS_Store", "finder\n")
			err := ParentStateForRuntime(ctx, f.parent, f.base, head)
			checkErr := f.m.CheckParentState(ctx, f.parent, f.base, head)
			if mode == "missing-file" {
				if err == nil || !strings.Contains(err.Error(), ".DS_Store") || checkErr == nil {
					t.Fatalf("without an excludes file .DS_Store must be refused: %v / %v", err, checkErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("globally ignored .DS_Store refused: %v", err)
			}
			if checkErr != nil {
				t.Fatalf("CheckParentState refused a globally ignored .DS_Store: %v", checkErr)
			}
			write(t, f.parent, "notes.txt", "mine\n")
			if err := ParentStateForRuntime(ctx, f.parent, f.base, head); err == nil || !strings.Contains(err.Error(), "notes.txt") || strings.Contains(err.Error(), ".DS_Store") {
				t.Fatalf("untracked non-ignored file: %v", err)
			}
		})
	}
}

// N2: global configuration other than the excludes file is not loaded (a
// global fsmonitor hook still never runs).
func TestParentChecksLoadNoOtherGlobalConfig(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	home := t.TempDir()
	marker := filepath.Join(home, "ran")
	write(t, home, "hook.sh", "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	if err := os.Chmod(filepath.Join(home, "hook.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, home, "ign", ".DS_Store\n")
	write(t, home, ".gitconfig", "[core]\n\texcludesFile = "+filepath.Join(home, "ign")+"\n\tfsmonitor = "+filepath.Join(home, "hook.sh")+"\n")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if err := ParentStateForRuntime(ctx, f.parent, f.base, head); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("global fsmonitor hook ran: %v", err)
	}
}
