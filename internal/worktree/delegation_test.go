package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDelegationWorkspaceLinkedMetadataContainment(t *testing.T) {
	root := newRepo(t)
	write(t, root, "tracked", "base")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "--detach", linked, "HEAD")
	if err := ValidateCommitted(context.Background(), linked, git(t, linked, "rev-parse", "HEAD")); err == nil {
		t.Fatal("linked Git metadata skipped source symlink containment")
	}
}
