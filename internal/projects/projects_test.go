package projects

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte("Project: UMCode guidance."), 0o644)
	os.WriteFile(filepath.Join(root, "web", "UMCODE.md"), []byte("Web: nested UMCode."), 0o644)

	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	foreign := []string{"Global: be terse.", "run make test", "Svelte 5 runes"}
	composed, sources := svc.InstructionsFor(ctx, p, "")
	if !strings.Contains(composed, "UMCode guidance") || strings.Contains(composed, "nested UMCode") {
		t.Fatalf("composed = %q", composed)
	}
	if len(sources) != 1 || sources[0].Scope != "project" {
		t.Fatalf("sources = %+v", sources)
	}
	// Working in a nested directory composes root-to-leaf UMCODE.md files.
	composed, sources = svc.InstructionsFor(ctx, p, "web/App.svelte")
	if !strings.Contains(composed, "nested UMCode") || len(sources) != 2 || sources[1].Scope != "nested" {
		t.Fatalf("nested: %q %+v", composed, sources)
	}
	for _, f := range foreign {
		if strings.Contains(composed, f) {
			t.Fatalf("foreign instruction %q was composed: %q", f, composed)
		}
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

func TestInstructionsIgnoreForeignOnlyProject(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Project: run make test."), 0o644)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Prefer small commits."), 0o644)
	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	composed, sources := svc.InstructionsFor(ctx, p, "")
	if composed != "" || len(sources) != 0 {
		t.Fatalf("composed = %q sources = %+v", composed, sources)
	}
	res, err := svc.Instructions(ctx, p, nil)
	if err != nil || res.Exists {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestInstructionsSkipDirectoryNamedUMCode(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "UMCODE.md"), 0o755)
	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if composed, sources := svc.InstructionsFor(ctx, p, ""); composed != "" || len(sources) != 0 {
		t.Fatalf("composed = %q sources = %+v", composed, sources)
	}
	res, err := svc.Instructions(ctx, p, nil)
	if err != nil || res.Exists {
		t.Fatalf("a directory was reported as an instruction file: %+v err = %v", res, err)
	}
}

func TestInstructionInventory(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"UMCODE.md", "web/UMCODE.md", "web/deep/UMCODE.md", "web/deep/code.go", "api/code.go"} {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("guidance"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Foreign instruction names must not be statted, read, or followed. An
	// unreadable/escaping symlink with a foreign name cannot poison inventory.
	if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	got, err := InstructionInventory(root, []string{"web/deep/code.go", "api"})
	if err != nil || !reflect.DeepEqual(got, []string{"UMCODE.md", "web/UMCODE.md", "web/deep/UMCODE.md"}) {
		t.Fatalf("inventory = %v, %v", got, err)
	}
	if got, err := InstructionInventory(t.TempDir(), nil); err != nil || len(got) != 0 {
		t.Fatalf("absent root instruction = %v, %v", got, err)
	}
}

func TestInstructionInventoryRejectsUnsafePaths(t *testing.T) {
	for _, kind := range []string{"directory instruction", "escaping instruction", "foreign instruction target", "broken instruction", "escaping directory scope", "escaping file scope", "missing scope", "absolute scope", "parent scope", "foreign scope", "relative root", "missing root"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "code.go"), []byte("outside"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "web"), 0o755); err != nil {
				t.Fatal(err)
			}
			var scopes []string
			switch kind {
			case "directory instruction":
				if err := os.Mkdir(filepath.Join(root, "web/UMCODE.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "escaping instruction":
				if err := os.Symlink(filepath.Join(outside, "code.go"), filepath.Join(root, "web/UMCODE.md")); err != nil {
					t.Fatal(err)
				}
			case "broken instruction":
				if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "UMCODE.md")); err != nil {
					t.Fatal(err)
				}
			case "foreign instruction target":
				if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("foreign"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("AGENTS.md", filepath.Join(root, "UMCODE.md")); err != nil {
					t.Fatal(err)
				}
			case "escaping directory scope":
				if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
					t.Fatal(err)
				}
				scopes = []string{"escape"}
			case "escaping file scope":
				if err := os.Symlink(filepath.Join(outside, "code.go"), filepath.Join(root, "escape.go")); err != nil {
					t.Fatal(err)
				}
				scopes = []string{"escape.go"}
			case "missing scope":
				scopes = []string{"web/absent.go"}
			case "absolute scope":
				scopes = []string{filepath.Join(root, "web")}
			case "parent scope":
				scopes = []string{"web/../web"}
			case "foreign scope":
				scopes = []string{"CLAUDE.md"}
			case "relative root":
				root = "relative-project"
			case "missing root":
				root = filepath.Join(root, "absent")
			}
			if got, err := InstructionInventory(root, scopes); err == nil || len(got) != 0 {
				t.Fatalf("unsafe inventory = %v, %v", got, err)
			}
		})
	}
}

func TestInstructionInventoryContainedSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guidance.txt"), []byte("contained guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("guidance.txt", filepath.Join(root, "UMCODE.md")); err != nil {
		t.Fatal(err)
	}
	got, err := InstructionInventory(root, []string{"guidance.txt"})
	if err != nil || !reflect.DeepEqual(got, []string{"UMCODE.md"}) {
		t.Fatalf("contained link = %v, %v", got, err)
	}
}

func TestInstructionInventoryCompositionContainment(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprint(nested), func(t *testing.T) {
			svc, _, _ := newService(t)
			root, outside := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "web"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "UMCODE.md"), []byte("outside secret"), 0o644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "UMCODE.md")
			if nested {
				link = filepath.Join(root, "web/UMCODE.md")
			}
			if err := os.Symlink(filepath.Join(outside, "UMCODE.md"), link); err != nil {
				t.Fatal(err)
			}
			got, sources := svc.InstructionsFor(context.Background(), protocol.Project{Root: root, ID: "containment"}, "web")
			if got != "" || len(sources) != 0 {
				t.Fatalf("escaped instruction composed: %q, %+v", got, sources)
			}
		})
	}
}

func TestInstructionsTruncateLargeUMCode(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte(strings.Repeat("a", 40<<10)), 0o644)
	p, err := svc.Create(ctx, protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	_, sources := svc.InstructionsFor(ctx, p, "")
	if len(sources) != 1 || !strings.Contains(sources[0].Error, "truncated to 32 KB") {
		t.Fatalf("sources = %+v", sources)
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
