package workbench

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteModelMovesCompleteBundleToTrash(t *testing.T) {
	root := t.TempDir()
	m, err := Create(root, "Remove me", "custom")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, "Keep me", "classification"); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"data/raw.csv": "x,y\n1,2\n", "runs/checkpoint.bin": "saved weights"} {
		path := filepath.Join(root, "models", m.ID, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	trash, err := Delete(root, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(trash, "models/.trash/") {
		t.Fatalf("invalid recovery path: %s", trash)
	}
	for name, want := range map[string]string{"data/raw.csv": "x,y\n1,2\n", "runs/checkpoint.bin": "saved weights"} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(trash), name))
		if err != nil || string(got) != want {
			t.Fatalf("recovery %s = %q, %v", name, got, err)
		}
	}
	if _, err := Open(root, m.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted model still opens: %v", err)
	}
	models, err := List(root)
	if err != nil || len(models) != 1 || models[0].ID != "keep-me" {
		t.Fatalf("remaining models: %+v, %v", models, err)
	}
	if _, err := Delete(root, m.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repeat deletion = %v", err)
	}
	if _, err := Create(root, "Remove me", "custom"); err != nil {
		t.Fatal(err)
	}
	second, err := Delete(root, m.ID)
	if err != nil || second == trash {
		t.Fatalf("second deletion overwrites recovery: %s, %v", second, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(trash), "graph.json")); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteModelRejectsUnsafePaths(t *testing.T) {
	for _, scenario := range []string{"id", "models", "bundle", "trash"} {
		t.Run(scenario, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if _, err := Create(root, "Safe", "custom"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			id := "safe"
			switch scenario {
			case "id":
				id = "../safe"
			case "models":
				if err := os.Rename(filepath.Join(root, "models"), filepath.Join(root, "original")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "models")); err != nil {
					t.Fatal(err)
				}
			case "bundle":
				if err := os.Rename(filepath.Join(root, "models", id), filepath.Join(root, "original")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "models", id)); err != nil {
					t.Fatal(err)
				}
			case "trash":
				if err := os.Symlink(outside, filepath.Join(root, "models", ".trash")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Delete(root, id); err == nil {
				t.Fatal("unsafe deletion accepted")
			}
			got, err := os.ReadFile(filepath.Join(outside, "keep"))
			if err != nil || string(got) != "unchanged" {
				t.Fatalf("external files affected: %s, %v", got, err)
			}
			if scenario == "id" || scenario == "trash" {
				if _, err := Open(root, "safe"); err != nil {
					t.Fatalf("failed delete damaged model: %v", err)
				}
			}
		})
	}
}
