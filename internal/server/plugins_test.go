package server_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/plugins"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/skills"
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

func TestPluginReloadLinkedOnly(t *testing.T) {
	h := newHarness(t, nil)
	managed := installServerPlugin(t, h, writeServerPlugin(t, "1.0.0"), plugins.InstallManaged)
	if err := h.callErr(protocol.MethodPluginReload, protocol.PluginIDParams{PluginID: managed.ID}); err == nil {
		t.Fatal("managed reload unexpectedly succeeded")
	}
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
	if err := h.callErr(protocol.MethodPluginUninstall, protocol.PluginUninstallParams{PluginID: installed.ID}); err == nil {
		t.Fatal("enabled plugin uninstall unexpectedly succeeded")
	}
	h.call(protocol.MethodPluginUninstall, protocol.PluginUninstallParams{PluginID: installed.ID, DisableProjects: true}, nil)
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
