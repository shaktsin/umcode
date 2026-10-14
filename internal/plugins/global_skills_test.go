package plugins

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/skills"
)

func TestInstalledSkillsWorkWithoutProjectEnablement(t *testing.T) {
	root := writeManagerPlugin(t, "1.0.0", "http://127.0.0.1:1", true)
	writeInstallerFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"sample-plugin","version":"1.0.0","extensions":{"dev.umcode":{"hooks":"./hooks/hooks.json"}}}`)
	writeInstallerFile(t, filepath.Join(root, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: Greet someone.\nruntime:\n  type: shell\nscripts:\n  greet:\n    path: scripts/greet.sh\n---\nSay hello from the global skill.")
	writeInstallerFile(t, filepath.Join(root, "skills", "greet", "scripts", "greet.sh"), "echo global-hello")
	writeInstallerFile(t, filepath.Join(root, "hooks", "hooks.json"), `{"hooks":{"TurnStart":[{"hooks":[{"type":"command","command":"missing-command"}]}]}}`)
	pkg, err := LoadPackage(root)
	if err != nil || len(pkg.Hooks) != 1 {
		t.Fatalf("invalid hook fixture: %#v, %v", pkg.Hooks, err)
	}
	manager, st, project, installed := pluginTestManager(t, t.Context(), root)
	for _, projectID := range []string{"", "unselected-project"} {
		snapshot, err := manager.Acquire(t.Context(), projectID)
		if err != nil {
			t.Fatal(err)
		}
		ctx := skills.WithSnapshot(t.Context(), snapshot.SkillSnapshot())
		registry := skills.NewRegistry(config.Default(t.TempDir()))
		instructions, err := registry.InstructionsContext(ctx, "sample-plugin:greet")
		if err != nil || !strings.Contains(instructions, "Say hello from the global skill.") {
			t.Fatalf("instructions=%q err=%v", instructions, err)
		}
		output, err := registry.RunScript(ctx, "sample-plugin:greet", "greet", json.RawMessage(`{}`))
		if err != nil || !strings.Contains(output, "global-hello") {
			t.Fatalf("output=%q err=%v", output, err)
		}
		if len(snapshot.Tools()) != 0 || len(snapshot.Hooks().Declarations) != 0 {
			t.Fatal("global skills enabled MCP or hooks")
		}
		snapshot.Release()
	}
	// Removing the installation must update cached projectless generations too.
	if err := st.DeleteProjectPlugin(t.Context(), project.ID, installed.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePluginInstallation(t.Context(), installed.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.ActivateMany(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Acquire(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	registry := skills.NewRegistry(config.Default(t.TempDir()))
	if _, ok := registry.GetContext(skills.WithSnapshot(t.Context(), snapshot.SkillSnapshot()), "sample-plugin:greet"); ok {
		t.Fatal("uninstalled skill remained globally visible")
	}
}

func TestGlobalUnconfiguredSkillInstructionsRemainAvailable(t *testing.T) {
	root := t.TempDir()
	writeInstallerFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"sample-plugin","version":"1.0.0","settings":[{"name":"api_key","secret":true,"required":true}]}`)
	writeInstallerFile(t, filepath.Join(root, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: Greet someone.\nruntime:\n  type: shell\n  env:\n    API_KEY: '{{secret:api_key}}'\nscripts:\n  greet:\n    path: scripts/greet.sh\n---\nConfigure the API key to use the script.")
	writeInstallerFile(t, filepath.Join(root, "skills", "greet", "scripts", "greet.sh"), "echo should-not-run")
	manager, _, _, _ := pluginTestManager(t, t.Context(), root)
	snapshot, err := manager.Acquire(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	registry := skills.NewRegistry(config.Default(t.TempDir()))
	ctx := skills.WithSnapshot(t.Context(), snapshot.SkillSnapshot())
	if _, err := registry.InstructionsContext(ctx, "sample-plugin:greet"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RunScript(ctx, "sample-plugin:greet", "greet", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Fatalf("expected configuration error, got %v", err)
	}
	if _, err := registry.EnvFor(ctx, "sample-plugin:greet"); err == nil {
		t.Fatal("unresolved environment allowed")
	}
}

func TestGlobalSkillNamesDisambiguateMultiplePluginSources(t *testing.T) {
	root := t.TempDir()
	writeInstallerManifest(t, root, "1.0.0")
	writeInstallerFile(t, filepath.Join(root, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: Greet.\n---\nFirst source instructions.")
	manager, st, _, installed := pluginTestManager(t, t.Context(), root)
	secondRoot := t.TempDir()
	writeInstallerManifest(t, secondRoot, "1.0.0")
	writeInstallerFile(t, filepath.Join(secondRoot, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: Greet.\n---\nSecond source instructions.")
	second := installed
	second.ID = "other/sample-plugin"
	second.SourceID = "other"
	second.Root = secondRoot
	var err error
	second.Digest, err = digestTree(secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertPluginInstallation(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Acquire(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	registry := skills.NewRegistry(config.Default(t.TempDir()))
	ctx := skills.WithSnapshot(t.Context(), snapshot.SkillSnapshot())
	for name, want := range map[string]string{"source.sample-plugin:greet": "First source instructions.", "other.sample-plugin:greet": "Second source instructions."} {
		body, err := registry.InstructionsContext(ctx, name)
		if err != nil || !strings.Contains(body, want) {
			t.Fatalf("%s instructions=%q err=%v", name, body, err)
		}
		if _, ok := skills.Invocation("/" + name + " hello"); !ok {
			t.Fatalf("disambiguated command not recognized: %s", name)
		}
	}
}
