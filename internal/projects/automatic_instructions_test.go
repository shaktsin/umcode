package projects

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestProjectAutomaticallyCreatesInstructions(t *testing.T) {
	svc, st, _ := newService(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/auto\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Existing agent guidance"), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Create(t.Context(), protocol.ProjectCreateParams{Root: root, Name: "Automatic"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "UMCODE.md"))
	if err != nil {
		t.Fatalf("new project has no instructions: %v", err)
	}
	if !strings.Contains(string(data), "Automatic") || !strings.Contains(string(data), "go test") {
		t.Fatalf("draft not grounded in project: %s", data)
	}
	// Existing instructions and other tools' guidance are never replaced.
	if err := os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte("My instructions"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Open(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, "UMCODE.md"))
	if string(data) != "My instructions" {
		t.Fatalf("overwrote existing instructions: %s", data)
	}
	data, _ = os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(data) != "Existing agent guidance" {
		t.Fatal("changed AGENTS.md")
	}
	// Older registered projects are initialized when explicitly opened.
	oldRoot := t.TempDir()
	old, err := st.CreateProject(t.Context(), protocol.Project{Name: "Older", Root: oldRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Open(t.Context(), old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(oldRoot, "UMCODE.md")); err != nil {
		t.Fatalf("older project not initialized: %v", err)
	}
}

func TestAutomaticInstructionsDoNotFollowExistingAliases(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, _ := newService(t)
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside.md")
			if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "UMCODE.md")
			var err error
			if kind == "symlink" {
				err = os.Symlink(outside, path)
			} else {
				err = os.Mkdir(path, 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Create(t.Context(), protocol.ProjectCreateParams{Root: root}); err == nil {
				t.Fatal("unsafe existing instructions accepted")
			}
			data, _ := os.ReadFile(outside)
			if string(data) != "untouched" {
				t.Fatal("modified alias destination")
			}
		})
	}
}

func TestAutomaticInstructionsDoNotReadExternalMetadata(t *testing.T) {
	svc, _, _ := newService(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Makefile")
	if err := os.WriteFile(outside, []byte("test:\n\techo outside\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "Makefile")); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Create(t.Context(), protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.Root, "UMCODE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "`make test`") {
		t.Fatal("external symlink metadata was read")
	}
}

func TestAutomaticInstructionsPreserveContainedAlias(t *testing.T) {
	svc, _, _ := newService(t)
	root := t.TempDir()
	target := filepath.Join(root, "instructions.md")
	if err := os.WriteFile(target, []byte("Existing guidance"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("instructions.md", filepath.Join(root, "UMCODE.md")); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Create(t.Context(), protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Open(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "Existing guidance" {
		t.Fatal("alias overwritten")
	}
}
func TestAutomaticDraftBoundsDirectoryOnlyTrees(t *testing.T) {
	svc, _, _ := newService(t)
	root := t.TempDir()
	for i := 0; i < 300; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("%03d-%s", i, strings.Repeat("d", 200))), 0755); err != nil {
			t.Fatal(err)
		}
	}
	draft, err := svc.DraftInstructions(protocol.Project{Root: root, Name: strings.Repeat("name", 10000)})
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Content) > maxInstructionBytes {
		t.Fatalf("draft too large: %d", len(draft.Content))
	}
	if !strings.Contains(draft.Content, "## Working guidelines") {
		t.Fatal("working guidance lost")
	}
}
