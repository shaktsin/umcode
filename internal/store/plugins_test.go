package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPluginInstallationPersistence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "umcode.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := PluginInstallation{
		ID: "0123456789abcdef/sample-plugin", Name: "sample-plugin", Version: "1.0.0",
		Format: "agent", Source: "file:///plugins/sample", SourceID: "0123456789abcdef",
		Mode: "managed", Root: "/cache/sample/1.0.0", Digest: "abc123", Active: true,
		DiagnosticsJSON: `[{"code":"compat/example"}]`,
	}
	if err := s.UpsertPluginInstallation(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetPluginInstallation(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPluginInstallationFields(t, got, want)
	if got.InstalledAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not populated: %+v", got)
	}
	list, err := s.ListPluginInstallations(ctx)
	if err != nil || len(list) != 1 || list[0].ID != want.ID {
		t.Fatalf("ListPluginInstallations() = %#v, %v", list, err)
	}
}

func TestProjectPluginEnablementAndSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openPluginTestStore(t, ctx)
	defer s.Close()
	projectA := createPluginTestProject(t, ctx, s, "a")
	projectB := createPluginTestProject(t, ctx, s, "b")
	pluginID := insertPluginTestInstallation(t, ctx, s)

	wantSettings := map[string]any{"greeting": "hello", "retries": float64(2)}
	if err := s.SetProjectPlugin(ctx, ProjectPlugin{ProjectID: projectA.ID, PluginID: pluginID, Enabled: true, Settings: wantSettings}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectPlugin(ctx, ProjectPlugin{ProjectID: projectB.ID, PluginID: pluginID, Enabled: false, Settings: map[string]any{"greeting": "bye"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectPlugin(ctx, projectA.ID, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || !reflect.DeepEqual(got.Settings, wantSettings) {
		t.Fatalf("ProjectPlugin() = %#v, want enabled settings %#v", got, wantSettings)
	}
	list, err := s.ListProjectPlugins(ctx, projectA.ID)
	if err != nil || len(list) != 1 || list[0].PluginID != pluginID {
		t.Fatalf("ListProjectPlugins() = %#v, %v", list, err)
	}
	projects, err := s.ProjectsUsingPlugin(ctx, pluginID)
	if err != nil || !reflect.DeepEqual(projects, []string{projectA.ID}) {
		t.Fatalf("ProjectsUsingPlugin() = %#v, %v, want only enabled project", projects, err)
	}
}

func TestDeleteProjectCascadesPluginEnablement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openPluginTestStore(t, ctx)
	defer s.Close()
	project := createPluginTestProject(t, ctx, s, "cascade")
	pluginID := insertPluginTestInstallation(t, ctx, s)
	if err := s.SetProjectPlugin(ctx, ProjectPlugin{ProjectID: project.ID, PluginID: pluginID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.ProjectPlugin(ctx, project.ID, pluginID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ProjectPlugin() error = %v, want ErrNotFound", err)
	}
}

func TestPluginHookRunPersistence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openPluginTestStore(t, ctx)
	defer s.Close()
	project := createPluginTestProject(t, ctx, s, "hooks")
	pluginID := insertPluginTestInstallation(t, ctx, s)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	runs := []PluginHookRun{
		{PluginID: pluginID, PluginVersion: "1.0.0", ProjectID: project.ID, ThreadID: "thr_a", TurnID: "turn_a", Event: "TurnStart", Status: "continued", DurationMS: 12, CreatedAt: now},
		{PluginID: pluginID, PluginVersion: "1.0.0", ProjectID: project.ID, ThreadID: "thr_a", TurnID: "turn_b", Event: "BeforeToolUse", Status: "blocked", Blocked: true, Reason: "blocked by fixture", DurationMS: 7, CreatedAt: now.Add(time.Second)},
		{PluginID: "other/plugin", PluginVersion: "2", ProjectID: project.ID, ThreadID: "thr_b", TurnID: "turn_b", Event: "AfterToolUse", Status: "failed", Error: "fixture failure", CreatedAt: now.Add(2 * time.Second)},
	}
	for _, run := range runs {
		if err := s.RecordPluginHookRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListPluginHookRuns(ctx, pluginID, project.ID, "turn_b", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListPluginHookRuns() = %#v, %v", got, err)
	}
	if !got[0].Blocked || got[0].Reason != "blocked by fixture" || got[0].Event != "BeforeToolUse" {
		t.Fatalf("hook run = %#v", got[0])
	}
	if err := s.DeletePluginInstallation(ctx, pluginID); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListPluginHookRuns(ctx, pluginID, "", "", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("audit rows after uninstall = %#v, %v, want 2 retained", got, err)
	}
}

func openPluginTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func createPluginTestProject(t *testing.T, ctx context.Context, s *Store, suffix string) protocol.Project {
	t.Helper()
	p, err := s.CreateProject(ctx, protocol.Project{Name: suffix, Root: filepath.Join(t.TempDir(), suffix)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func insertPluginTestInstallation(t *testing.T, ctx context.Context, s *Store) string {
	t.Helper()
	p := PluginInstallation{ID: "0123456789abcdef/sample-plugin", Name: "sample-plugin", Version: "1.0.0", Format: "agent", Source: "file:///plugins/sample", SourceID: "0123456789abcdef", Mode: "managed", Root: "/cache/sample/1.0.0", Digest: "abc123", Active: true}
	if err := s.UpsertPluginInstallation(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func assertPluginInstallationFields(t *testing.T, got, want PluginInstallation) {
	t.Helper()
	got.InstalledAt, got.UpdatedAt = time.Time{}, time.Time{}
	want.InstalledAt, want.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PluginInstallation = %#v, want %#v", got, want)
	}
}
