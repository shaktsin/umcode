package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedTaskSnapshotRecoveryReapsOnlyOrphan(t *testing.T) {
	env := suggestFixture(t)
	id := "unadmitted-crash"
	destination := env.e.Worktrees.Path(id)
	if err := env.e.markPreparedTaskSnapshot(id, destination); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "partial"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	env.e.reconcilePreparedTaskSnapshots(t.Context())
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("orphan snapshot remains: %v", err)
	}
	if _, err := os.Lstat(destination + ".preparing"); !os.IsNotExist(err) {
		t.Fatalf("orphan marker remains: %v", err)
	}
}
