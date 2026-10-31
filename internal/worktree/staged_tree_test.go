package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// StagedTree reads the staged tree of an amended checkout without rewriting
// the index file or taking index.lock, even with stat-dirty entries.
func TestStagedTreeNeverRewritesIndex(t *testing.T) {
	ctx := context.Background()
	f, head := integratedFixture(t)
	write(t, f.parent, "keep.txt", "keep, fixed\n")
	git(t, f.parent, "add", "keep.txt")
	later := time.Now().Add(2 * time.Hour)
	for _, rel := range []string{"keep.txt", "new.txt", "bin.dat"} {
		if err := os.Chtimes(filepath.Join(f.parent, rel), later, later); err != nil {
			t.Fatal(err)
		}
	}
	before := checkoutBytes(t, f.parent)
	tree, err := f.m.StagedTree(ctx, f.parent, f.base)
	if err != nil {
		t.Fatal(err)
	}
	if after := checkoutBytes(t, f.parent); after != before {
		t.Fatalf("StagedTree rewrote the checkout:\n%s\n---\n%s", before, after)
	}
	if _, err := os.Lstat(filepath.Join(f.parent, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index.lock: %v", err)
	}
	if want := git(t, f.parent, "write-tree"); tree != want || tree == head {
		t.Fatalf("StagedTree = %s, want %s (head %s)", tree, want, head)
	}
	// The unchanged integrated head is reported as is (the caller decides).
	git(t, f.parent, "read-tree", "-m", "-u", head)
	if tree, err = f.m.StagedTree(ctx, f.parent, f.base); err != nil || tree != head {
		t.Fatalf("StagedTree at head = %s %v", tree, err)
	}
}

func TestStagedTreeRefusals(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, want string
		dirty      func(t *testing.T, f integrationFixture)
	}{
		{"unstaged", "keep.txt", func(t *testing.T, f integrationFixture) { write(t, f.parent, "keep.txt", "KEEP\n") }},
		{"untracked", "stray.txt", func(t *testing.T, f integrationFixture) { write(t, f.parent, "stray.txt", "x\n") }},
		{"moved HEAD", "HEAD moved", func(t *testing.T, f integrationFixture) { git(t, f.parent, "commit", "-qm", "user") }},
		{"conflict stages", "keep.txt", func(t *testing.T, f integrationFixture) {
			blob := git(t, f.parent, "rev-parse", "HEAD:keep.txt")
			git(t, f.parent, "rm", "-q", "--cached", "keep.txt")
			cmd := exec.Command("git", "-C", f.parent, "update-index", "--index-info")
			cmd.Stdin = strings.NewReader("100644 " + blob + " 1\tkeep.txt\n100644 " + blob + " 2\tkeep.txt\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
		}},
		{"staged gitlink", "submodule", func(t *testing.T, f integrationFixture) {
			git(t, f.parent, "update-index", "--add", "--cacheinfo", "160000,"+f.base+",sub")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := integratedFixture(t)
			c.dirty(t, f)
			before := checkoutBytes(t, f.parent)
			_, err := f.m.StagedTree(ctx, f.parent, f.base)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("StagedTree with %s: %v", c.name, err)
			}
			if after := checkoutBytes(t, f.parent); after != before {
				t.Fatal("refusal changed the checkout")
			}
		})
	}
}
