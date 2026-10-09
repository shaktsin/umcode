package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryConfigDefaultsDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	c := Default(home)
	if c.Memory.AutoPromote || c.Memory.TargetFileBytes != 4096 {
		t.Fatalf("default memory = %+v", c.Memory)
	}
	path := filepath.Join(home, "config.yaml")
	for _, contents := range []string{"models: {}\n", "memory: {}\n", "memory: {auto_promote: true}\n"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if c.Memory.TargetFileBytes != 4096 || c.Memory.AutoPromote != (contents == "memory: {auto_promote: true}\n") {
			t.Fatalf("loaded memory = %+v for %q", c.Memory, contents)
		}
	}
}

func TestMemoryConfigValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	path := filepath.Join(home, "config.yaml")
	for _, size := range []int{1024, 32768, 0, -1, 1023, 32769} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			if err := os.WriteFile(path, []byte(fmt.Sprintf("memory: {auto_promote: true, target_file_bytes: %d}\n", size)), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			valid := size == 1024 || size == 32768
			if (err == nil) != valid {
				t.Fatalf("size=%d err=%v", size, err)
			}
			if valid && (c.Memory.TargetFileBytes != size || !c.Memory.AutoPromote || c.Models.DesignedWorkflow) {
				t.Fatalf("config = %+v", c)
			}
		})
	}
}

func TestLoadPythonConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	t.Setenv("UMCODE_LLM_MODEL", "env-model")
	cfg := filepath.Join(home, "config.yaml")
	os.WriteFile(cfg, []byte(`
llm:
  provider: anthropic
  model: claude-sonnet-4-6
  providers:
    Google: {default_model: gemini-2.5-pro}
control_panel: {enabled: true, ui_type: web}   # unknown to the Go engine: ignored
tools:
  workspaces:
    - {name: projects, path: ~/projects, default: true, acl: {delete_files: true}}
storage: {db_path: ~/.umcode/custom.db}
models:
  default_complexity: deep
  complexity: {quick: {max_tool_steps: 2}}
`), 0o600)
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Provider != "claude" || c.LLM.Model != "env-model" {
		t.Fatalf("llm = %+v", c.LLM)
	}
	if _, ok := c.LLM.Providers["gemini"]; !ok {
		t.Fatalf("providers not normalized: %v", c.LLM.Providers)
	}
	uh, _ := os.UserHomeDir()
	if c.Tools.Workspaces[0].Path != filepath.Join(uh, "projects") || !*c.Tools.Workspaces[0].ACL.DeleteFiles {
		t.Fatalf("workspace = %+v", c.Tools.Workspaces[0])
	}
	if c.Storage.DBPath != filepath.Join(uh, ".umcode", "custom.db") || c.Models.DefaultComplexity != "deep" {
		t.Fatalf("storage/models = %+v %+v", c.Storage, c.Models)
	}
	if c.Runtime.SocketPath != filepath.Join(home, "run", "engine.sock") || c.Runtime.EngineWSPort != 8766 {
		t.Fatalf("runtime = %+v", c.Runtime)
	}
	os.WriteFile(cfg, []byte("models: {default_complexity: extreme}\n"), 0o600)
	if _, err := Load(cfg); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLegacyContextFileKeyIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	cfg := filepath.Join(home, "config.yaml")
	os.WriteFile(cfg, []byte("agents:\n  enabled: true\n  context_file: ~/.umcode/AGENT.md\n"), 0o600)
	c, err := Load(cfg)
	if err != nil {
		t.Fatalf("legacy context_file key broke loading: %v", err)
	}
	if !c.Agents.Enabled {
		t.Fatalf("agents = %+v", c.Agents)
	}
}

func TestStorageVaultKeysOptional(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	cfg := filepath.Join(home, "config.yaml")
	os.WriteFile(cfg, []byte("storage: {vault_max_object_bytes: 1024, retention_redacted_days: 3}\n"), 0o600)
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Storage
	if s.VaultMaxObjectBytes != 1024 || s.RetentionRedactedDays != 3 || s.RetentionRawDays != 0 || s.VaultDir == "" {
		t.Fatalf("storage = %+v", s)
	}
}

func TestContextCompilerFlagDefaultsFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	cfg := filepath.Join(home, "config.yaml")
	os.WriteFile(cfg, []byte("llm: {provider: anthropic}\n"), 0o600)
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.Models.ContextCompiler {
		t.Fatal("the context compiler must be off unless configured")
	}
	os.WriteFile(cfg, []byte("models: {context_compiler: true}\n"), 0o600)
	c, err = Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Models.ContextCompiler {
		t.Fatal("models.context_compiler: true must enable it")
	}
}

func TestToolResultReducersFlagDefaultsFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	cfg := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfg, []byte("models: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.Models.ToolResultReducers {
		t.Fatal("tool result reducers must be off unless configured")
	}
	if err := os.WriteFile(cfg, []byte("models: {tool_result_reducers: true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Models.ToolResultReducers {
		t.Fatal("models.tool_result_reducers: true must enable it")
	}
}

func TestDesignedWorkflowFlagDefaultsFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UMCODE_HOME", home)
	cfg := filepath.Join(home, "config.yaml")
	for _, tc := range []struct {
		name string
		yaml string
		want bool
	}{
		{"omitted", "llm: {provider: anthropic}\n", false},
		{"empty_models", "models: {}\n", false},
		{"explicit_false", "models: {designed_workflow: false}\n", false},
		{"enabled", "models: {designed_workflow: true}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(cfg, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if c.Models.DesignedWorkflow != tc.want {
				t.Fatalf("designed workflow = %v, want %v", c.Models.DesignedWorkflow, tc.want)
			}
		})
	}
}
