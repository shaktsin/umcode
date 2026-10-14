package evaluation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluationWorkspaceOwnership(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "fixture")
	os.Mkdir(src, 0700)
	os.WriteFile(filepath.Join(src, "notes.txt"), []byte("original"), 0600)
	m := fixtureManifest()
	m.BaseDir = base
	hash, err := TreeHash(src)
	if err != nil {
		t.Fatal(err)
	}
	m.Tasks[0].FixtureHash = hash
	u, _ := Schedule(m)
	out := t.TempDir()
	p, err := Stage(out, m, u[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := CleanupOwned(p, "wrong"); err == nil {
		t.Fatal("wrong owner deleted")
	}
	b, _ := os.ReadFile(filepath.Join(p, "workspace", "notes.txt"))
	if string(b) != "original" {
		t.Fatal("staging")
	}
	os.WriteFile(filepath.Join(p, "workspace", "notes.txt"), []byte("changed"), 0600)
	b, _ = os.ReadFile(filepath.Join(src, "notes.txt"))
	if string(b) != "original" {
		t.Fatal("modified fixture")
	}
	if err := CleanupOwned(p, m.ID); err != nil {
		t.Fatal(err)
	}
	os.Symlink("notes.txt", filepath.Join(src, "alias"))
	if _, err := Stage(out, m, u[0]); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(filepath.Join(src, "alias"))
	f, _ := os.Create(filepath.Join(src, "large"))
	f.Truncate(64<<20 + 1)
	f.Close()
	if _, err := TreeHash(src); err == nil {
		t.Fatal("oversize accepted")
	}
}
