package server_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/plugins"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
)

func TestPluginInspectInstallListGet(t *testing.T) {
	h := newHarness(t, nil)
	root := writeServerPlugin(t, "1.0.0")
	var inspection protocol.PluginInspection
	h.call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: root}, &inspection)
	if inspection.Token == "" || inspection.Plugin.Name != "server-plugin" {
		t.Fatalf("inspection = %#v", inspection)
	}
	var installed protocol.PluginInfo
	h.call(protocol.MethodPluginInstall, protocol.PluginInstallParams{Token: inspection.Token, ProjectID: h.proj.ID}, &installed)
	var list protocol.PluginListResult
	h.call(protocol.MethodPluginList, protocol.PluginListParams{ProjectID: h.proj.ID}, &list)
	if len(list.Plugins) != 1 || !list.Plugins[0].Enabled || list.Plugins[0].ID != installed.ID {
		t.Fatalf("plugins = %#v", list.Plugins)
	}
	var got protocol.PluginInfo
	h.call(protocol.MethodPluginGet, protocol.PluginGetParams{PluginID: installed.ID, ProjectID: h.proj.ID}, &got)
	if got.ID != installed.ID || len(got.Components) != 1 {
		t.Fatalf("plugin = %#v", got)
	}
}

func TestPluginGetIncludesHealthAndHookFailures(t *testing.T) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	if err := h.eng.Store.RecordPluginHookRun(h.ctx, store.PluginHookRun{
		PluginID: installed.ID, PluginVersion: "1.0.0", ProjectID: h.proj.ID,
		Event: "TurnStart", Status: "failed", Error: "fixture hook failed", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var got protocol.PluginInfo
	h.call(protocol.MethodPluginGet, protocol.PluginGetParams{PluginID: installed.ID, ProjectID: h.proj.ID}, &got)
	if got.Health != "partially_unavailable" || len(got.HookFailures) != 1 || got.HookFailures[0].PluginVersion != "1.0.0" {
		t.Fatalf("plugin health = %#v", got)
	}
}

func TestOptionalMCPFailureAppearsInPluginHealth(t *testing.T) {
	h := newHarness(t, nil)
	root := writeServerPlugin(t, "1.0.0")
	if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(`{"mcpServers":{"optional":{"type":"stdio","command":"./missing","required":false,"startup_timeout_sec":0.1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installed := installServerPlugin(t, h, root, plugins.InstallManaged)
	var got protocol.PluginInfo
	h.call(protocol.MethodPluginGet, protocol.PluginGetParams{PluginID: installed.ID, ProjectID: h.proj.ID}, &got)
	if got.Health != "partially_unavailable" {
		t.Fatalf("plugin health = %q", got.Health)
	}
	var failed bool
	for _, component := range got.Components {
		if component.Kind == "mcp" && component.Name == "optional" && component.Health == "failed" && component.Error != "" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("components = %#v", got.Components)
	}
}

func TestPluginConfigureStoresSecretsOutsideDatabase(t *testing.T) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	var configured protocol.PluginInfo
	h.call(protocol.MethodPluginConfigure, protocol.PluginConfigureParams{PluginID: installed.ID, ProjectID: h.proj.ID,
		Settings: map[string]any{"region": "west"}, Secrets: map[string]string{"api_key": "super-secret"}}, &configured)
	if configured.Settings["region"] != "west" {
		t.Fatalf("settings = %#v", configured.Settings)
	}
	raw, _ := json.Marshal(configured)
	if strings.Contains(string(raw), "super-secret") {
		t.Fatalf("secret leaked in response: %s", raw)
	}
	secret, err := h.eng.Secrets.Get("plugin/" + installed.ID + "/api_key")
	if err != nil || secret != "super-secret" {
		t.Fatalf("secret store = %q, %v", secret, err)
	}
	projectPlugin, err := h.eng.Store.ProjectPlugin(h.ctx, h.proj.ID, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(projectPlugin.Settings)
	if strings.Contains(string(stored), "super-secret") {
		t.Fatalf("secret entered sqlite settings: %s", stored)
	}
}

func TestPluginWithRequiredConfigurationInstallsDisabledThenEnables(t *testing.T) {
	h := newHarness(t, nil)
	root := writeServerPlugin(t, "1.0.0")
	manifest := filepath.Join(root, "plugin.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"name":"api_key","secret":true`, `"name":"api_key","secret":true,"required":true`, 1))
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	installed := installServerPlugin(t, h, root, plugins.InstallManaged)
	if installed.Enabled || installed.Health != "configuration_required" {
		t.Fatalf("new required-config plugin = %#v", installed)
	}
	var configured protocol.PluginInfo
	h.call(protocol.MethodPluginConfigure, protocol.PluginConfigureParams{
		PluginID: installed.ID, ProjectID: h.proj.ID, Settings: map[string]any{"region": "west"}, Secrets: map[string]string{"api_key": "configured"},
	}, &configured)
	h.call(protocol.MethodPluginSetEnabled, protocol.PluginSetEnabledParams{PluginID: installed.ID, ProjectID: h.proj.ID, Enabled: true}, &configured)
	if !configured.Enabled || configured.Health != "healthy" {
		t.Fatalf("configured plugin = %#v", configured)
	}
}

func TestPluginConfigureRejectsSecretSmugglingAndInvalidSettings(t *testing.T) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	for _, tc := range []struct {
		name     string
		settings map[string]any
	}{
		{name: "secret in settings", settings: map[string]any{"api_key": "leak"}},
		{name: "unknown setting", settings: map[string]any{"unknown": "value"}},
		{name: "wrong type", settings: map[string]any{"region": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.callErr(protocol.MethodPluginConfigure, protocol.PluginConfigureParams{
				PluginID: installed.ID, ProjectID: h.proj.ID, Settings: tc.settings,
			})
		})
	}
	configured, err := h.eng.Store.ProjectPlugin(h.ctx, h.proj.ID, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.Settings) != 0 {
		t.Fatalf("invalid settings persisted: %#v", configured.Settings)
	}
}

func TestPluginSetEnabledInvalidatesProjectSnapshot(t *testing.T) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	old, err := h.eng.Plugins.Acquire(h.ctx, h.proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Release()
	if _, ok := h.eng.Skills.GetContext(skills.WithSnapshot(h.ctx, old.SkillSnapshot()), "server-plugin:greet"); !ok {
		t.Fatal("enabled snapshot missing plugin skill")
	}
	h.call(protocol.MethodPluginSetEnabled, protocol.PluginSetEnabledParams{PluginID: installed.ID, ProjectID: h.proj.ID, Enabled: false}, nil)
	fresh, err := h.eng.Plugins.Acquire(h.ctx, h.proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	if _, ok := h.eng.Skills.GetContext(skills.WithSnapshot(h.ctx, fresh.SkillSnapshot()), "server-plugin:greet"); ok {
		t.Fatal("disabled plugin remained in a newly acquired snapshot")
	}
	if _, ok := h.eng.Skills.GetContext(skills.WithSnapshot(h.ctx, old.SkillSnapshot()), "server-plugin:greet"); !ok {
		t.Fatal("disable mutated the active snapshot")
	}
}

func TestPluginSetEnabledRollsBackWhenActivationFails(t *testing.T) {
	h := newHarness(t, nil)
	root := writeServerPlugin(t, "1.0.0")
	if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(`{"mcpServers":{"broken":{"type":"stdio","command":"./missing","required":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var inspection protocol.PluginInspection
	h.call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: root}, &inspection)
	var installed protocol.PluginInfo
	h.call(protocol.MethodPluginInstall, protocol.PluginInstallParams{Token: inspection.Token}, &installed)
	if err := h.callErr(protocol.MethodPluginSetEnabled, protocol.PluginSetEnabledParams{PluginID: installed.ID, ProjectID: h.proj.ID, Enabled: true}); err == nil {
		t.Fatal("broken plugin enable unexpectedly succeeded")
	}
	state, err := h.eng.Store.ProjectPlugin(h.ctx, h.proj.ID, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Enabled {
		t.Fatalf("failed activation remained enabled: %#v", state)
	}
}

func TestPluginReloadLinkedOnly(t *testing.T) {
	h := newHarness(t, nil)
	managed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	if err := h.callErr(protocol.MethodPluginReload, protocol.PluginIDParams{PluginID: managed.ID}); err == nil {
		t.Fatal("managed reload unexpectedly succeeded")
	}
	h.call(protocol.MethodPluginSetEnabled, protocol.PluginSetEnabledParams{PluginID: managed.ID, ProjectID: h.proj.ID, Enabled: false}, nil)
	linkedRoot := writeServerPlugin(t, "1.0.0")
	linked := installServerPlugin(t, h, linkedRoot, plugins.InstallLinked)
	manifest := filepath.Join(linkedRoot, "plugin.json")
	data, _ := os.ReadFile(manifest)
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(data), `"version":"1.0.0"`, `"version":"2.0.0"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var reloaded protocol.PluginInfo
	h.call(protocol.MethodPluginReload, protocol.PluginIDParams{PluginID: linked.ID}, &reloaded)
	if reloaded.Version != "2.0.0" {
		t.Fatalf("reloaded = %#v", reloaded)
	}
}

func TestPluginUninstallRequiresExplicitProjectDisable(t *testing.T) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	if err := h.eng.Secrets.Set("plugin/"+installed.ID+"/api_key", "uninstall-secret"); err != nil {
		t.Fatal(err)
	}
	if err := h.callErr(protocol.MethodPluginUninstall, protocol.PluginUninstallParams{PluginID: installed.ID}); err == nil {
		t.Fatal("enabled plugin uninstall unexpectedly succeeded")
	}
	h.call(protocol.MethodPluginUninstall, protocol.PluginUninstallParams{PluginID: installed.ID, DisableProjects: true}, nil)
	if _, err := h.eng.Secrets.Get("plugin/" + installed.ID + "/api_key"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("plugin secret remains after uninstall: %v", err)
	}
}

func TestPluginUninstallDefersManagedCodeRemovalUntilSnapshotRelease(t *testing.T) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	record, err := h.eng.Store.GetPluginInstallation(h.ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.eng.Plugins.Acquire(h.ctx, h.proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.call(protocol.MethodPluginUninstall, protocol.PluginUninstallParams{PluginID: installed.ID, DisableProjects: true}, nil)
	if _, err := os.Stat(record.Root); err != nil {
		t.Fatalf("managed code removed while snapshot active: %v", err)
	}
	if _, ok := h.eng.Skills.GetContext(skills.WithSnapshot(h.ctx, snapshot.SkillSnapshot()), "server-plugin:greet"); !ok {
		t.Fatal("active snapshot lost installed skill")
	}
	snapshot.Release()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err = os.Stat(record.Root)
		if os.IsNotExist(err) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("managed code remains after snapshot release: %v", err)
	}
}

func TestPluginErrorsContainPhaseCodeAndRemediation(t *testing.T) {
	h := newHarness(t, nil)
	err := h.callErr(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: filepath.Join(t.TempDir(), "missing")})
	var rpc *protocol.Error
	if !errors.As(err, &rpc) {
		t.Fatalf("error = %T %v", err, err)
	}
	var data protocol.PluginErrorData
	if json.Unmarshal(rpc.Data, &data) != nil || data.Phase == "" || data.Code == "" || data.Remediation == "" {
		t.Fatalf("plugin error data = %s", rpc.Data)
	}
}

func installServerPlugin(t *testing.T, h *harness, root string, mode plugins.InstallMode) protocol.PluginInfo {
	t.Helper()
	var inspection protocol.PluginInspection
	h.call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: root, Mode: string(mode)}, &inspection)
	var installed protocol.PluginInfo
	h.call(protocol.MethodPluginInstall, protocol.PluginInstallParams{Token: inspection.Token, ProjectID: h.proj.ID}, &installed)
	return installed
}

func writeServerPlugin(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "greet"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"server-plugin","version":"` + version + `","settings":[{"name":"region"},{"name":"api_key","secret":true}]}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: greet\ndescription: Greets from the server plugin\n---\nUse this skill to greet.\n"
	if err := os.WriteFile(filepath.Join(root, "skills", "greet", "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
