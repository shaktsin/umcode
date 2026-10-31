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
