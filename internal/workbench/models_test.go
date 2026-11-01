package workbench

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateListOpenAndPersistGraph(t *testing.T) {
	root := t.TempDir()
	models, err := List(root)
	if err != nil || len(models) != 0 {
		t.Fatalf("empty list = %v, %v", models, err)
	}
	m, err := Create(root, "Customer Support", "text-generation")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "customer-support" {
		t.Fatalf("unexpected slug: %q", m.ID)
	}
	for _, file := range []string{"model.json", "graph.json", "model.py", "adapter.py"} {
		if _, err := os.Stat(filepath.Join(root, "models", m.ID, file)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "models", m.ID, "runs")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, "Customer Support", "custom"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	data := json.RawMessage(`{"version":1,"nodes":[{"id":"n1"}],"edges":[]}`)
	if err := SaveGraph(root, m.ID, data); err != nil {
		t.Fatal(err)
	}
	got, err := Open(root, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model.Task != "text-generation" || !strings.Contains(string(got.Graph), `"n1"`) {
		t.Fatalf("bad bundle: %+v", got)
	}
	models, err = List(root)
	if err != nil || len(models) != 1 || models[0].ID != m.ID {
		t.Fatalf("unexpected list: %v, %v", models, err)
	}
}

func TestValidationAndTraversal(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, task string }{{"!!!", "custom"}, {"OK", "other"}, {" ", "custom"}} {
		if _, err := Create(root, tc.name, tc.task); err == nil {
			t.Fatalf("expected reject: %+v", tc)
		}
	}
	_, err := Create(root, "Valid", "classification")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../../etc", "..", "valid/../other"} {
		if _, err := Open(root, id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	for _, bad := range []string{`null`, `[]`, `{"version":2,"nodes":[],"edges":[]}`, `{"version":1,"nodes":{},"edges":[]}`, `{"nodes":[],"edges":[]}`} {
		if err := SaveGraph(root, "valid", json.RawMessage(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if err := SaveGraph(root, "valid", json.RawMessage(strings.Repeat("a", maxGraphBytes+1))); err == nil {
		t.Fatal("accepted oversized graph")
	}
}

func TestSymlinksRejected(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, "models")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Create(root, "Bad", "custom"); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := List(root); err == nil {
		t.Fatal("listed symlink")
	}
}

func TestModelDirectorySymlinkRejected(t *testing.T) {
	root := t.TempDir()
	_, err := Create(root, "Valid", "custom")
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, "models", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Open(root, "linked"); err == nil {
		t.Fatal("opened linked bundle")
	}
}

func TestSavePreservesJSONNumbers(t *testing.T) {
	root := t.TempDir()
	if _, err := Create(root, "Exact", "custom"); err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"version":1,"nodes":[],"edges":[],"seed":9007199254740993,"scale":1.0000000000000000001}`)
	if err := SaveGraph(root, "exact", data); err != nil {
		t.Fatal(err)
	}
	got, err := Open(root, "exact")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Graph), "9007199254740993") || !strings.Contains(string(got.Graph), "1.0000000000000000001") {
		t.Fatalf("JSON numbers changed: %s", got.Graph)
	}
}

func TestSaveRejectsFormattingOverflowWithoutDamage(t *testing.T) {
	root := t.TempDir()
	if _, err := Create(root, "Size", "custom"); err != nil {
		t.Fatal(err)
	}
	before, err := Open(root, "size")
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"version":1,"nodes":[` + strings.Repeat(`{"id":1},`, 90000) + `{}],"edges":[]}`)
	if len(data) >= maxGraphBytes {
		t.Fatal("fixture must fit before formatting")
	}
	if err := SaveGraph(root, "size", data); err == nil {
		t.Fatal("accepted graph that exceeds limit once formatted")
	}
	after, err := Open(root, "size")
	if err != nil || string(after.Graph) != string(before.Graph) {
		t.Fatalf("failed save damaged the graph: %v", err)
	}
}

func TestBundleFilesRejectSymlinks(t *testing.T) {
	for _, name := range []string{"model.json", "graph.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if _, err := Create(root, "Linked", "custom"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "models", "linked", name)
			outside := filepath.Join(t.TempDir(), name)
			if err := os.Rename(path, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(root, "linked"); err == nil {
				t.Fatal("followed bundle file symlink")
			}
			if err := SaveGraph(root, "linked", json.RawMessage(`{"version":1,"nodes":[],"edges":[]}`)); err == nil {
				t.Fatal("saved through invalid bundle")
			}
		})
	}
}

func TestCustomAdapterDoesNotDeclareTextTypes(t *testing.T) {
	root := t.TempDir()
	if _, err := Create(root, "Custom", "custom"); err != nil {
		t.Fatal(err)
	}
	adapter, err := os.ReadFile(filepath.Join(root, "models", "custom", "adapter.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adapter), "INPUT_TYPE = None") || !strings.Contains(string(adapter), "OUTPUT_TYPE = None") {
		t.Fatalf("custom adapter silently sets types: %s", adapter)
	}
}
