package projects

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/compute"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store, *config.Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	cfg := config.Default(home)
	st, err := store.Open(context.Background(), filepath.Join(home, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, cfg), st, cfg
}

func TestCreateAndGuards(t *testing.T) {
	svc, _, cfg := newService(t)
	ctx := context.Background()
	root := t.TempDir()

	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != filepath.Base(root) || p.Root != pathutil.Resolved(root) {
		t.Fatalf("project = %+v", p)
	}
	if _, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root}); err == nil ||
		!strings.Contains(err.Error(), "already a project") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: filepath.Join(root, "nope")}); err == nil {
		t.Fatal("missing folder accepted")
	}
	if _, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: cfg.Home}); err == nil ||
		!strings.Contains(err.Error(), "data folder") {
		t.Fatalf("engine home accepted: %v", err)
	}
	home, _ := os.UserHomeDir()
	if _, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: home}); err == nil {
		t.Fatal("whole home folder accepted")
	}

	// A folder that disappears is reported, not hidden.
	gone := t.TempDir()
	g, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: gone})
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(gone)
	if g, err = svc.Get(ctx, g.ID); err != nil || !g.Missing {
		t.Fatalf("missing folder: %+v %v", g, err)
	}
}

func TestReadArtifactAllowsBoundedScreenshotsOnly(t *testing.T) {
	svc, _, _ := newService(t)
	root := t.TempDir()
	p := protocol.Project{Root: root}
	if err := os.WriteFile(filepath.Join(root, "shot.png"), []byte("image bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ReadArtifact(context.Background(), p, protocol.ProjectReadArtifactParams{Path: "shot.png"})
	if err != nil || got.MimeType != "image/png" || got.DataB64 == "" {
		t.Fatalf("artifact = %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "trace.zip"), []byte("trace"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadArtifact(context.Background(), p, protocol.ProjectReadArtifactParams{Path: "trace.zip"}); err == nil {
		t.Fatal("executable/non-image artifact was returned inline")
	}
}

func TestComputeResourceSettingsAreValidated(t *testing.T) {
	svc, _, _ := newService(t)
	badCPU := 9
	if _, err := svc.Create(context.Background(), protocol.ProjectCreateParams{
		Root: t.TempDir(), Tools: protocol.ProjectTools{ComputeVCPUs: &badCPU},
	}); err == nil || !strings.Contains(err.Error(), "vCPU") {
		t.Fatalf("invalid compute limit accepted: %v", err)
	}
	project, err := svc.Create(context.Background(), protocol.ProjectCreateParams{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	badDisk := 512
	if _, err := svc.Update(context.Background(), protocol.ProjectUpdateParams{
		ProjectID: project.ID, Tools: &protocol.ProjectTools{ComputeDiskMiB: &badDisk},
	}); err == nil || !strings.Contains(err.Error(), "workspace limit") {
		t.Fatalf("invalid disk limit accepted: %v", err)
	}
}

func TestProjectFolderCanChangeOnlyBeforeChatsExist(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := context.Background()
	oldRoot, newRoot := t.TempDir(), t.TempDir()
	project, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: oldRoot, Name: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	name := "After"
	updated, err := svc.Update(ctx, protocol.ProjectUpdateParams{ProjectID: project.ID, Name: &name, Root: &newRoot})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || !pathutil.SameFolder(updated.Root, newRoot) {
		t.Fatalf("updated project = %+v", updated)
	}
	if _, err := st.CreateThread(ctx, protocol.Thread{Title: "Existing chat", ProjectID: project.ID}); err != nil {
		t.Fatal(err)
	}
	thirdRoot := t.TempDir()
	if _, err := svc.Update(ctx, protocol.ProjectUpdateParams{ProjectID: project.ID, Root: &thirdRoot}); err == nil || !strings.Contains(err.Error(), "cannot be changed while it has chats") {
		t.Fatalf("folder changed after project chats existed: %v", err)
	}
	unchanged, err := svc.Get(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pathutil.SameFolder(unchanged.Root, newRoot) {
		t.Fatalf("rejected folder change mutated project root: %s", unchanged.Root)
	}
}

func TestProjectComputeEnablementChecksHostReadiness(t *testing.T) {
	svc, _, _ := newService(t)
	project, err := svc.Create(context.Background(), protocol.ProjectCreateParams{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	_, createErr := svc.Create(context.Background(), protocol.ProjectCreateParams{
		Root: t.TempDir(), Tools: protocol.ProjectTools{Compute: &enabled},
	})
	hostErr := compute.CheckHostAvailability()
	if hostErr != nil {
		if createErr == nil || !strings.Contains(createErr.Error(), "isolated compute is unavailable") {
			t.Fatalf("compute was enabled without host support: create err=%v, host err=%v", createErr, hostErr)
		}
		_, updateErr := svc.Update(context.Background(), protocol.ProjectUpdateParams{
			ProjectID: project.ID, Tools: &protocol.ProjectTools{Compute: &enabled},
		})
		if updateErr == nil || !strings.Contains(updateErr.Error(), "isolated compute is unavailable") {
			t.Fatalf("compute was enabled during project update without host support: %v", updateErr)
		}
		updated, err := svc.Get(context.Background(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Tools.Compute != nil && *updated.Tools.Compute {
			t.Fatal("project persisted compute enabled after host preflight failed")
		}
		return
	}
	if createErr != nil {
		t.Fatalf("compute should enable on project creation when host support is available: %v", createErr)
	}
	if _, err := svc.Update(context.Background(), protocol.ProjectUpdateParams{
		ProjectID: project.ID, Tools: &protocol.ProjectTools{Compute: &enabled},
	}); err != nil {
		t.Fatalf("compute should enable on project update when host support is available: %v", err)
	}
}

func TestResolveStaysInsideTheProject(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o600)
	os.Symlink(outside, filepath.Join(root, "link"))
	os.MkdirAll(filepath.Join(root, "src"), 0o755)

	for _, ok := range []string{"", ".", "src", "src/main.go", filepath.Join(root, "src/main.go")} {
		if _, err := Resolve(root, ok); err != nil {
			t.Errorf("Resolve(%q) = %v, want allowed", ok, err)
		}
	}
	for _, bad := range []string{"..", "../secret.txt", outside, filepath.Join(outside, "secret.txt"),
		"link/secret.txt", "src/../../escape", "~/secret.txt"} {
		if _, err := Resolve(root, bad); err == nil {
			t.Errorf("Resolve(%q) was allowed", bad)
		}
	}
}

func TestInstructionsComposition(t *testing.T) {
	svc, _, cfg := newService(t)
	ctx := context.Background()
	root := t.TempDir()
	os.WriteFile(filepath.Join(cfg.Home, "AGENT.md"), []byte("Global: be terse."), 0o644)
	os.MkdirAll(filepath.Join(root, "web"), 0o755)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Project: run make test."), 0o644)
	os.WriteFile(filepath.Join(root, "web", "CLAUDE.md"), []byte("Web: use Svelte 5 runes."), 0o644)

	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	composed, sources := svc.InstructionsFor(ctx, p, "")
	if !strings.Contains(composed, "Global: be terse.") || !strings.Contains(composed, "Project: run make test.") {
		t.Fatalf("composed = %q", composed)
	}
	if strings.Contains(composed, "Svelte 5") {
		t.Fatal("nested file applied without a hint")
	}
	if len(sources) != 2 || sources[0].Scope != "global" || sources[1].Scope != "project" {
		t.Fatalf("sources = %+v", sources)
	}
	// Working in a nested directory composes root-to-leaf instructions.
	composed, sources = svc.InstructionsFor(ctx, p, "web/App.svelte")
	if !strings.Contains(composed, "Svelte 5 runes") || len(sources) != 3 || sources[2].Scope != "nested" {
		t.Fatalf("nested: %q %+v", composed, sources)
	}
	// UMCODE, AGENTS, and CLAUDE instructions are all composed; external files remain inputs.
	os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte("Project: UMCode guidance."), 0o644)
	composed, _ = svc.InstructionsFor(ctx, p, "")
	if !strings.Contains(composed, "UMCode guidance") || !strings.Contains(composed, "run make test") {
		t.Fatalf("precedence: %q", composed)
	}
	// The editor writes only UMCODE.md and leaves AGENTS.md and CLAUDE.md untouched.
	text := "Project: written by the app."
	res, err := svc.Instructions(ctx, p, &text)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(pathutil.Resolved(root), "UMCODE.md") || !res.Exists || !strings.Contains(res.Composed, "written by the app") {
		t.Fatalf("instructions = %+v", res)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(data) != "Project: run make test." {
		t.Fatalf("AGENTS.md was modified: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "web", "CLAUDE.md")); string(data) != "Web: use Svelte 5 runes." {
		t.Fatalf("CLAUDE.md was modified: %q", data)
	}
}

func TestDraftInstructionsScansWithoutWriting(t *testing.T) {
	svc, _, _ := newService(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\tgo test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := protocol.Project{Name: "Sample", Root: root}
	draft, err := svc.DraftInstructions(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft.Content, "go test ./...") || !strings.Contains(draft.Content, "UMCode project instructions") {
		t.Fatalf("draft did not reflect scan: %q", draft.Content)
	}
	if _, err := os.Stat(filepath.Join(root, "UMCODE.md")); !os.IsNotExist(err) {
		t.Fatalf("scan wrote UMCODE.md without approval: %v", err)
	}
}

func TestRecordDiffAndRevert(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	root := t.TempDir()
	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(root, "notes.txt")
	os.WriteFile(notes, []byte("one\ntwo\n"), 0o644)

	var emitted []protocol.FileChangeData
	rec := svc.NewRecorder(p, "thr_1", "trn_1", func(c protocol.FileChangeData) { emitted = append(emitted, c) })

	before := Snapshot(notes)
	os.WriteFile(notes, []byte("one\ntwo\nthree\n"), 0o644)
	rec.Record(ctx, notes, before, false)

	created := filepath.Join(root, "new.txt")
	beforeCreate := Snapshot(created) // nil: the file does not exist yet
	os.WriteFile(created, []byte("fresh\n"), 0o644)
	rec.Record(ctx, created, beforeCreate, false)

	if len(emitted) != 2 {
		t.Fatalf("emitted = %+v", emitted)
	}
	if emitted[0].Action != protocol.FileModified || emitted[0].Additions != 1 || !strings.Contains(emitted[0].Diff, "+three") {
		t.Fatalf("modify = %+v", emitted[0])
	}
	if emitted[1].Action != protocol.FileCreated || emitted[1].Additions != 1 {
		t.Fatalf("create = %+v", emitted[1])
	}

	diff, err := svc.Diff(ctx, p, protocol.ProjectDiffParams{TurnID: "trn_1"})
	if err != nil || len(diff.Files) != 2 {
		t.Fatalf("diff = %+v (%v)", diff, err)
	}

	res, err := svc.RevertTurn(ctx, "trn_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reverted) != 2 {
		t.Fatalf("revert = %+v", res)
	}
	if data, _ := os.ReadFile(notes); string(data) != "one\ntwo\n" {
		t.Fatalf("notes after revert = %q", data)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatal("created file survived the revert")
	}
}

func TestUnifiedDiff(t *testing.T) {
	diff, adds, dels, _ := Unified("a.txt", "one\ntwo\nthree\n", "one\n2\nthree\nfour\n")
	if adds != 2 || dels != 1 {
		t.Fatalf("counts = +%d -%d", adds, dels)
	}
	for _, want := range []string{"--- a/a.txt", "+++ b/a.txt", "@@", "-two", "+2", "+four", " three"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff)
		}
	}
	if d, a, _, _ := Unified("a.txt", "same\n", "same\n"); d != "" || a != 0 {
		t.Fatalf("no-op diff = %q", d)
	}
	// A new file is all additions.
	if _, a, d, _ := Unified("n.txt", "", "hello\nworld\n"); a != 2 || d != 0 {
		t.Fatalf("new file = +%d -%d", a, d)
	}
}

func TestFilesAndRead(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src", "deep"), 0o755)
	os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("noise"), 0o644)
	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Files(ctx, p, protocol.ProjectFilesParams{Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range res.Entries {
		paths = append(paths, e.Path)
	}
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "src/main.go") || strings.Contains(joined, "node_modules") {
		t.Fatalf("entries = %v", paths)
	}
	file, err := svc.ReadFile(ctx, p, protocol.ProjectReadFileParams{Path: "src/main.go"})
	if err != nil || file.Content != "package main\n" {
		t.Fatalf("read = %+v (%v)", file, err)
	}
	if _, err := svc.ReadFile(ctx, p, protocol.ProjectReadFileParams{Path: "../outside"}); err == nil {
		t.Fatal("read outside the project was allowed")
	}
}

// On macOS /var is a symlink to /private/var, so a project added by one
// spelling must accept paths given in the other — and must not be addable twice.
func TestProjectRootReachedThroughASymlink(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	base := t.TempDir()
	real := filepath.Join(base, "private", "work")
	if err := os.MkdirAll(filepath.Join(real, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(real, "src", "main.go"), []byte("package main\n"), 0o644)
	link := filepath.Join(base, "work")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: link, Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != pathutil.Resolved(real) {
		t.Fatalf("root = %q, want the resolved %q", p.Root, pathutil.Resolved(real))
	}
	// The same folder by its other name is the same project.
	if _, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: real}); err == nil ||
		!strings.Contains(err.Error(), "already a project") {
		t.Fatalf("duplicate through symlink: %v", err)
	}
	// Both spellings resolve to files inside the project.
	for _, path := range []string{"src/main.go", filepath.Join(link, "src", "main.go"), filepath.Join(real, "src", "main.go")} {
		if _, err := Resolve(p.Root, path); err != nil {
			t.Errorf("Resolve(%q) = %v, want allowed", path, err)
		}
	}
	file, err := svc.ReadFile(ctx, p, protocol.ProjectReadFileParams{Path: filepath.Join(link, "src", "main.go")})
	if err != nil || file.Path != "src/main.go" {
		t.Fatalf("read through the symlinked path = %+v (%v)", file, err)
	}
}
