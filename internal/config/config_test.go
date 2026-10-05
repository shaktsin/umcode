package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
