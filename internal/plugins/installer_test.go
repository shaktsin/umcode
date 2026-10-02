package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func TestInspectDoesNotMutate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := t.TempDir()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	installer := NewInstaller(home, st, time.Now)

	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Token == "" || inspection.Digest == "" || inspection.SourceID == "" {
		t.Fatalf("inspection missing binding fields: %#v", inspection)
	}
	if !inspection.RequiresApproval {
		t.Fatal("executable MCP/hook components must require approval")
	}
	installed, err := st.ListPluginInstallations(ctx)
	if err != nil || len(installed) != 0 {
		t.Fatalf("inspection mutated store: %#v, %v", installed, err)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "cache")); !os.IsNotExist(err) {
		t.Fatalf("inspection created cache: %v", err)
	}
}

func TestManagedInstallCopiesAndSurvivesSourceRemoval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := t.TempDir()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	installer := NewInstaller(home, st, time.Now)
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installer.Install(ctx, inspection.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Root == source || installed.Mode != string(InstallManaged) {
		t.Fatalf("managed installation = %#v", installed)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	pkg, err := LoadPackage(installed.Root)
	if err != nil || pkg.ID != "sample-plugin" {
		t.Fatalf("cached package after source removal = %#v, %v", pkg, err)
	}
}

func TestInstallRejectsChangedSourceAfterInspection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	installer := NewInstaller(t.TempDir(), st, time.Now)
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	writeInstallerFile(t, filepath.Join(source, "README.md"), "changed after inspection")
	_, err = installer.Install(ctx, inspection.Token, "")
	assertPluginErrorCode(t, err, "install/source_changed")
	if _, err := st.GetPluginInstallation(ctx, inspection.SourceID+"/sample-plugin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("changed source installed: %v", err)
	}
}

func TestInstallRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	outside := t.TempDir()
	writeInstallerFile(t, filepath.Join(outside, "SKILL.md"), "---\nname: escaped\ndescription: escaped\n---\n")
	if err := os.Symlink(outside, filepath.Join(source, "skills", "escaped")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	installer := NewInstaller(t.TempDir(), st, time.Now)
	_, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	assertPluginErrorCode(t, err, "validate/path_escape")
}

func TestFailedUpdatePreservesPreviousVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	installer := NewInstaller(t.TempDir(), st, time.Now)
	firstInspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	first, err := installer.Install(ctx, firstInspection.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	writeInstallerManifest(t, source, "2.0.0")
	secondInspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	writeInstallerFile(t, filepath.Join(source, "README.md"), "mutated")
	if _, err := installer.Install(ctx, secondInspection.Token, ""); err == nil {
		t.Fatal("changed update unexpectedly installed")
	}
	got, err := st.GetPluginInstallation(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.0.0" || got.Root != first.Root || got.Digest != first.Digest {
		t.Fatalf("previous installation changed: %#v, want %#v", got, first)
	}
	if _, err := LoadPackage(first.Root); err != nil {
		t.Fatalf("previous cache unusable: %v", err)
	}
}

func TestInspectionTokenExpiresAndIsSingleUseAfterSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	installer := NewInstaller(t.TempDir(), st, func() time.Time { return now })
	expired, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Minute)
	_, err = installer.Install(ctx, expired.Token, "")
	assertPluginErrorCode(t, err, "install/inspection_expired")

	fresh, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(ctx, fresh.Token, ""); err != nil {
		t.Fatal(err)
	}
	_, err = installer.Install(ctx, fresh.Token, "")
	assertPluginErrorCode(t, err, "install/invalid_token")
}

func TestInspectionTokenCannotBeUsedConcurrently(t *testing.T) {
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	installer := NewInstaller(t.TempDir(), st, time.Now)
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := installer.Install(ctx, inspection.Token, "")
			results <- err
		}()
	}
	close(start)
	var successes int
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent installs = %d, want 1", successes)
	}
}

func TestInspectPrunesExpiredInspectionTokens(t *testing.T) {
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	source := writeInstallerPlugin(t, "1.0.0")
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	installer := NewInstaller(t.TempDir(), st, func() time.Time { return now })
	if _, err := installer.Inspect(ctx, InspectRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Minute)
	if _, err := installer.Inspect(ctx, InspectRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	installer.mu.Lock()
	remaining := len(installer.inspections)
	installer.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("unexpired inspections = %d, want 1", remaining)
	}
}

func TestLinkedReloadKeepsPreviousPackageOnValidationFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	project, err := st.CreateProject(ctx, protocol.Project{Name: "linked", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	source := writeInstallerPlugin(t, "1.0.0")
	installer := NewInstaller(t.TempDir(), st, time.Now)
	manager, err := NewManager(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	installer.SetLifecycle(manager)
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallLinked})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installer.Install(ctx, inspection.Token, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeInstallerFile(t, filepath.Join(source, "plugin.json"), `{"$schema":"unsupported","name":"sample-plugin"}`)
	if _, err := installer.Reload(ctx, installed.ID); err == nil {
		t.Fatal("invalid linked package unexpectedly reloaded")
	}
	got, err := st.GetPluginInstallation(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != installed.Digest || got.Version != installed.Version || got.Root != installed.Root {
		t.Fatalf("failed reload changed active record: %#v, want %#v", got, installed)
	}
	manager.Close()
	restarted, err := NewManager(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	snapshot, err := restarted.Acquire(ctx, project.ID)
	if err != nil {
		t.Fatalf("last-known-good linked snapshot did not survive restart: %v", err)
	}
	snapshot.Release()
}

func TestLinkedReloadAllowsSameVersionContentEdits(t *testing.T) {
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	project, err := st.CreateProject(ctx, protocol.Project{Name: "linked-same-version", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	source := writeInstallerPlugin(t, "1.0.0")
	manager, err := NewManager(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	installer := NewInstaller(t.TempDir(), st, time.Now)
	installer.SetLifecycle(manager)
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallLinked})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installer.Install(ctx, inspection.Token, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeInstallerFile(t, filepath.Join(source, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: Changed without a version bump.\n---\n")
	reloaded, err := installer.Reload(ctx, installed.ID)
	if err != nil {
		t.Fatalf("same-version linked reload failed: %v", err)
	}
	if reloaded.Version != installed.Version || reloaded.Root == installed.Root {
		t.Fatalf("reload did not create a new immutable same-version snapshot: before=%#v after=%#v", installed, reloaded)
	}
}

func TestUninstallRefusesEnabledProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	project, err := st.CreateProject(ctx, protocol.Project{Name: "plugin project", Root: filepath.Join(t.TempDir(), "project")})
	if err != nil {
		t.Fatal(err)
	}
	source := writeInstallerPlugin(t, "1.0.0")
	installer := NewInstaller(t.TempDir(), st, time.Now)
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source, Mode: InstallManaged})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installer.Install(ctx, inspection.Token, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.Uninstall(ctx, installed.ID, false); err == nil {
		t.Fatal("enabled plugin unexpectedly uninstalled")
	}
	if _, err := st.GetPluginInstallation(ctx, installed.ID); err != nil {
		t.Fatalf("refused uninstall removed installation: %v", err)
	}
	if err := installer.Uninstall(ctx, installed.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPluginInstallation(ctx, installed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("installation remains after explicit disable: %v", err)
	}
	if _, err := os.Stat(installed.Root); !os.IsNotExist(err) {
		t.Fatalf("managed code remains after uninstall: %v", err)
	}
}

func TestManagedUpdateActivatesEveryEnabledProjectAndRollsBackGlobally(t *testing.T) {
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	project1, err := st.CreateProject(ctx, protocol.Project{ID: "project-a", Name: "one", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	project2, err := st.CreateProject(ctx, protocol.Project{ID: "project-b", Name: "two", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	installer := NewInstaller(home, st, time.Now)
	source := writeInstallerPlugin(t, "1.0.0")
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installer.Install(ctx, inspection.Token, project1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectPlugin(ctx, store.ProjectPlugin{ProjectID: project2.ID, PluginID: installed.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	life := &recordingLifecycle{failProject: project2.ID}
	installer.SetLifecycle(life)
	writeInstallerManifest(t, source, "2.0.0")
	inspection, err = installer.Inspect(ctx, InspectRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(ctx, inspection.Token, project1.ID); err == nil {
		t.Fatal("update unexpectedly succeeded after second project activation failed")
	}
	got, err := st.GetPluginInstallation(ctx, installed.ID)
	if err != nil || got.Version != "1.0.0" || got.Root != installed.Root {
		t.Fatalf("global installation not rolled back: %#v, %v", got, err)
	}
	if !containsString(life.activated, project1.ID) || !containsString(life.activated, project2.ID) {
		t.Fatalf("activated projects = %v", life.activated)
	}
}

func TestUninstallActivationFailureRestoresInstallationAndProjectSettings(t *testing.T) {
	ctx := context.Background()
	st := openInstallerStore(t, ctx)
	defer st.Close()
	project1, err := st.CreateProject(ctx, protocol.Project{Name: "one", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	project2, err := st.CreateProject(ctx, protocol.Project{Name: "two", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(t.TempDir(), st, time.Now)
	source := writeInstallerPlugin(t, "1.0.0")
	inspection, err := installer.Inspect(ctx, InspectRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installer.Install(ctx, inspection.Token, project1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectPlugin(ctx, store.ProjectPlugin{ProjectID: project2.ID, PluginID: installed.ID, Enabled: true, Settings: map[string]any{"region": "west"}}); err != nil {
		t.Fatal(err)
	}
	installer.SetLifecycle(&recordingLifecycle{failProject: project2.ID})
	if err := installer.Uninstall(ctx, installed.ID, true); err == nil {
		t.Fatal("uninstall unexpectedly succeeded")
	}
	if _, err := st.GetPluginInstallation(ctx, installed.ID); err != nil {
		t.Fatalf("installation was not restored: %v", err)
	}
	for _, id := range []string{project1.ID, project2.ID} {
		ref, err := st.ProjectPlugin(ctx, id, installed.ID)
		if err != nil || !ref.Enabled {
			t.Fatalf("project %s config not restored: %#v, %v", id, ref, err)
		}
	}
	if _, err := os.Stat(installed.Root); err != nil {
		t.Fatalf("managed code removed on failed uninstall: %v", err)
	}
}

type recordingLifecycle struct {
	activated   []string
	failProject string
}

func (l *recordingLifecycle) Activate(_ context.Context, project string) error {
	l.activated = append(l.activated, project)
	if project == l.failProject {
		return errors.New("test activation failure")
	}
	return nil
}
func (*recordingLifecycle) DeferCleanup(string, func()) {}

func writeInstallerPlugin(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	writeInstallerManifest(t, root, version)
	writeInstallerFile(t, filepath.Join(root, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: Greet someone.\n---\n")
	writeInstallerFile(t, filepath.Join(root, "mcp.json"), `{"mcpServers":{"echo":{"type":"stdio","command":"echo-server"}}}`)
	writeInstallerFile(t, filepath.Join(root, "hooks", "hooks.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"hook-helper"}]}]}}`)
	return root
}

func writeInstallerManifest(t *testing.T, root, version string) {
	t.Helper()
	writeInstallerFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"sample-plugin","version":"`+version+`","extensions":{"com.openai":{"hooks":"./hooks/hooks.json"}}}`)
}

func writeInstallerFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openInstallerStore(t *testing.T, ctx context.Context) *store.Store {
	t.Helper()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func assertPluginErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	var pluginErr *Error
	if !errors.As(err, &pluginErr) || pluginErr.Diagnostic.Code != code {
		t.Fatalf("error = %#v, want plugin code %s", err, code)
	}
}
