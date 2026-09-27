// Package config loads ~/.umcode/config.yaml. It reads the same keys as the
// Python app; unknown keys are ignored so existing files keep working.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Config is the subset of config.yaml the Go engine understands today.
type Config struct {
	Version int           `yaml:"version"`
	LLM     LLMConfig     `yaml:"llm"`
	Agents  AgentsConfig  `yaml:"agents"`
	Tools   ToolsConfig   `yaml:"tools"`
	Policy  PolicyConfig  `yaml:"policy"`
	Storage StorageConfig `yaml:"storage"`
	Runtime RuntimeConfig `yaml:"runtime"`
	Models  ModelsConfig  `yaml:"models"`

	// SkillDirs are extra directories scanned for skills (each subfolder with a SKILL.md).
	SkillDirs []string `yaml:"skill_dirs"`
	// Skills holds runtime overrides: "defaults" plus one entry per skill name.
	Skills map[string]SkillRuntimeOverride `yaml:"skills"`
	// MCPServers are Model Context Protocol servers whose tools the agent can use.
	MCPServers []MCPServerConfig `yaml:"mcp_servers"`

	// Path is where the config was loaded from ("" if defaults only).
	Path string `yaml:"-"`
	// Home is the resolved UMCode home directory.
	Home string `yaml:"-"`
}

type LLMConfig struct {
	Provider        string                       `yaml:"provider"`
	Model           string                       `yaml:"model"`
	APIKey          string                       `yaml:"api_key"` // legacy; migrated to Keychain
	ReasoningEffort string                       `yaml:"reasoning_effort"`
	Providers       map[string]LLMProviderConfig `yaml:"providers"`
}

type LLMProviderConfig struct {
	Enabled      *bool    `yaml:"enabled"`
	APIKey       string   `yaml:"api_key"` // legacy; migrated to Keychain
	BaseURL      string   `yaml:"base_url"`
	Models       []string `yaml:"models"`
	DefaultModel string   `yaml:"default_model"`
}

// IsEnabled defaults to true when unset.
func (p LLMProviderConfig) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

type AgentModelConfig struct {
	Provider        string `yaml:"provider"`
	Model           string `yaml:"model"`
	ReasoningEffort string `yaml:"reasoning_effort"`
}

type AgentsConfig struct {
	Enabled                   bool             `yaml:"enabled"`
	ContextFile               string           `yaml:"context_file"`
	Orchestrator              AgentModelConfig `yaml:"orchestrator"`
	Worker                    AgentModelConfig `yaml:"worker"`
	MaxAgentIterations        int              `yaml:"max_agent_iterations"`
	MaxOrchestratorIterations int              `yaml:"max_orchestrator_iterations"`
	TokensPerMinute           int              `yaml:"tokens_per_minute"`
}

type WorkspaceACL struct {
	Read        *bool `yaml:"read"`
	Write       *bool `yaml:"write"`
	CreateFiles *bool `yaml:"create_files"`
	DeleteFiles *bool `yaml:"delete_files"`
	Shell       *bool `yaml:"shell"`
}

type WorkspaceConfig struct {
	Name    string       `yaml:"name"`
	Path    string       `yaml:"path"`
	ACL     WorkspaceACL `yaml:"acl"`
	Default bool         `yaml:"default"`
}

type ToolsConfig struct {
	ShellEnabled bool `yaml:"shell_enabled"`
	// HostSandbox confines host shell commands (Seatbelt on macOS,
	// bubblewrap on Linux): "auto" (default) uses it when available, "off"
	// runs commands unconfined.
	HostSandbox string            `yaml:"host_sandbox"`
	Workspaces  []WorkspaceConfig `yaml:"workspaces"`
}

type PolicyConfig struct {
	ConfirmationStrictness string `yaml:"confirmation_strictness"` // normal | strict
	// ApprovalMode is the engine-wide default auto-approve tier, used for a
	// project that has not picked its own mode (see protocol.Project.ApprovalMode
	// and policy.Gate.Check): normal | auto_approve_workspace | auto_approve_all.
	// Projects normally set this themselves, from the project's settings panel
	// or the selector in the chat header, so this is mostly a fallback default.
	ApprovalMode     string   `yaml:"approval_mode"`
	AutoApproveTools []string `yaml:"auto_approve_tools"`
	// AutoApproveShellCommands is not currently read anywhere; use
	// auto_approve_tools (e.g. "shell.*") or a project's approval_mode instead.
	AutoApproveShellCommands []string `yaml:"auto_approve_shell_commands"`
	// ShellForbidCommands are command prefixes (word by word, per pipeline
	// segment, e.g. "git push") that are refused without asking.
	ShellForbidCommands    []string `yaml:"shell_forbid_commands"`
	ApprovalTimeoutMinutes int      `yaml:"approval_timeout_minutes"`
}

type StorageConfig struct {
	DBPath   string `yaml:"db_path"`
	VaultDir string `yaml:"vault_dir"`
}

type RuntimeConfig struct {
	LogDir  string `yaml:"log_dir"`
	WSHost  string `yaml:"ws_host"`
	WSPort  int    `yaml:"ws_port"`
	WSToken string `yaml:"ws_token"`
	// EngineWSPort is the Go engine's protocol WebSocket (0 disables it).
	// It is separate from ws_port so the Python gateway can run alongside
	// during the migration.
	EngineWSPort int `yaml:"engine_ws_port"`
	// SocketPath is the engine's Unix socket (new in the Go engine).
	SocketPath string `yaml:"socket_path"`
}

// ModelsConfig holds new model-selection settings.
type ModelsConfig struct {
	DefaultComplexity string                      `yaml:"default_complexity"`
	Complexity        map[string]ComplexityPreset `yaml:"complexity"`
	ExecutionLimits   ExecutionLimits             `yaml:"execution_limits"`
	// Roles overrides the model per role: title, intent, orchestrator, worker.
	Roles map[string]AgentModelConfig `yaml:"roles"`
	// Configured is the user-approved model list shown in the app.
	Configured []ConfiguredModel `yaml:"configured"`
	// Pools are ordered groups used for automatic cross-provider fallback.
	Pools       []ModelPool `yaml:"pools"`
	DefaultPool string      `yaml:"default_pool"`
}

// ExecutionLimits bound a whole agent turn independently from reasoning level.
type ExecutionLimits struct {
	MaxDurationMinutes int     `yaml:"max_duration_minutes"`
	MaxTokens          int64   `yaml:"max_tokens"`
	MaxCostUSD         float64 `yaml:"max_cost_usd"`
	MaxToolRounds      int     `yaml:"max_tool_rounds"`
}

type ConfiguredModel struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Enabled  *bool  `yaml:"enabled"`
}

func (m ConfiguredModel) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

type ModelPool struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Strategy string   `yaml:"strategy"`
	Models   []string `yaml:"models"`
	Enabled  *bool    `yaml:"enabled"`
}

func (p ModelPool) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

type ComplexityPreset struct {
	Reasoning       string `yaml:"reasoning"`
	MaxToolSteps    int    `yaml:"max_tool_steps"` // deprecated; use models.execution_limits.max_tool_rounds
	MultiAgent      string `yaml:"multi_agent"`
	MaxOutputTokens int    `yaml:"max_output_tokens"`
}

// SkillRuntimeOverride overrides a skill's runtime (python/node binaries, PATH, env).
type SkillRuntimeOverride struct {
	PythonBin string            `yaml:"python_bin"`
	NodeBin   string            `yaml:"node_bin"`
	ExtraPath []string          `yaml:"extra_path"`
	Env       map[string]string `yaml:"env"`
	// Config is passed to the skill's scripts as the "config" object on stdin.
	Config map[string]any `yaml:"config"`
}

// MCPServerConfig is one MCP server (same keys as the Python app).
type MCPServerConfig struct {
	Name              string            `yaml:"name"`
	Transport         string            `yaml:"transport"` // stdio (default) | http
	Command           string            `yaml:"command"`
	Args              []string          `yaml:"args"`
	Env               map[string]string `yaml:"env"`
	EnvVars           []string          `yaml:"env_vars"`
	Cwd               string            `yaml:"cwd"`
	URL               string            `yaml:"url"`
	BearerTokenEnvVar string            `yaml:"bearer_token_env_var"`
	HTTPHeaders       map[string]string `yaml:"http_headers"`
	EnvHTTPHeaders    map[string]string `yaml:"env_http_headers"`
	Enabled           *bool             `yaml:"enabled"`
	Required          bool              `yaml:"required"`
	StartupTimeoutSec float64           `yaml:"startup_timeout_sec"`
	ToolTimeoutSec    float64           `yaml:"tool_timeout_sec"`
	EnabledTools      []string          `yaml:"enabled_tools"`
	DisabledTools     []string          `yaml:"disabled_tools"`
	// RiskLevel overrides the risk of every tool on this server: green | yellow | red.
	RiskLevel string `yaml:"risk_level"`
}

// IsEnabled defaults to true when unset.
func (m MCPServerConfig) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

// HomeDir returns the UMCode home: $UMCODE_HOME, else ~/.umcode.
func HomeDir() (string, error) {
	if h := os.Getenv("UMCODE_HOME"); h != "" {
		return expand(h), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	home := filepath.Join(userHome, ".umcode")
	return home, nil
}

// Load reads config from path, or from the default locations when path is "".
func Load(path string) (*Config, error) {
	home, err := HomeDir()
	if err != nil {
		return nil, err
	}
	cfg := Default(home)

	candidates := []string{path}
	if path == "" {
		candidates = []string{filepath.Join(home, "config.yaml")}
		if wd, err := os.Getwd(); err == nil {
			candidates = append([]string{filepath.Join(wd, "config.yaml")}, candidates...)
		}
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		p = expand(p)
		data, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			if path != "" {
				return nil, fmt.Errorf("config file %s not found", p)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		cfg.Path = p
		break
	}
	applyEnv(cfg)
	cfg.finalize()
	return cfg, cfg.Validate()
}

// Default returns a config with defaults rooted at home.
func Default(home string) *Config {
	return &Config{
		Version: 1,
		Home:    home,
		LLM:     LLMConfig{Provider: "claude"},
		Storage: StorageConfig{
			DBPath:   filepath.Join(home, "umcode.db"),
			VaultDir: filepath.Join(home, "vault"),
		},
		Runtime: RuntimeConfig{
			LogDir:       filepath.Join(home, "logs"),
			WSHost:       "127.0.0.1",
			WSPort:       8765,
			EngineWSPort: 8766,
			SocketPath:   filepath.Join(home, "run", "engine.sock"),
		},
		Policy: PolicyConfig{
			ConfirmationStrictness: "normal",
			ApprovalMode:           "normal",
			ApprovalTimeoutMinutes: 30,
		},
		Models: ModelsConfig{DefaultComplexity: "auto", ExecutionLimits: ExecutionLimits{
			MaxDurationMinutes: 120, MaxTokens: 1_000_000, MaxCostUSD: 10, MaxToolRounds: 200,
		}},
	}
}

func (c *Config) finalize() {
	c.Storage.DBPath = expand(c.Storage.DBPath)
	c.Storage.VaultDir = expand(c.Storage.VaultDir)
	c.Runtime.LogDir = expand(c.Runtime.LogDir)
	c.Runtime.SocketPath = expand(c.Runtime.SocketPath)
	if c.Runtime.SocketPath == "" {
		c.Runtime.SocketPath = filepath.Join(c.Home, "run", "engine.sock")
	}
	for i := range c.Tools.Workspaces {
		c.Tools.Workspaces[i].Path = expand(c.Tools.Workspaces[i].Path)
	}
	for i := range c.SkillDirs {
		c.SkillDirs[i] = expand(c.SkillDirs[i])
	}
	for i := range c.MCPServers {
		c.MCPServers[i].Cwd = expand(c.MCPServers[i].Cwd)
	}
	if c.Agents.ContextFile != "" {
		c.Agents.ContextFile = expand(c.Agents.ContextFile)
	}
	if c.Policy.ApprovalTimeoutMinutes <= 0 {
		c.Policy.ApprovalTimeoutMinutes = 30
	}
	if c.Models.DefaultComplexity == "" {
		c.Models.DefaultComplexity = "auto"
	}
	if c.Models.ExecutionLimits.MaxDurationMinutes == 0 {
		c.Models.ExecutionLimits.MaxDurationMinutes = 120
	}
	if c.Models.ExecutionLimits.MaxTokens == 0 {
		c.Models.ExecutionLimits.MaxTokens = 1_000_000
	}
	if c.Models.ExecutionLimits.MaxCostUSD == 0 {
		c.Models.ExecutionLimits.MaxCostUSD = 10
	}
	if c.Models.ExecutionLimits.MaxToolRounds == 0 {
		c.Models.ExecutionLimits.MaxToolRounds = 200
	}
	c.LLM.Provider = NormalizeProvider(c.LLM.Provider)
	for i := range c.Models.Configured {
		c.Models.Configured[i].Provider = NormalizeProvider(c.Models.Configured[i].Provider)
	}
	if c.LLM.Providers != nil {
		norm := make(map[string]LLMProviderConfig, len(c.LLM.Providers))
		for k, v := range c.LLM.Providers {
			norm[NormalizeProvider(k)] = v
		}
		c.LLM.Providers = norm
	}
}

// Validate checks values the engine depends on.
func (c *Config) Validate() error {
	switch c.Models.DefaultComplexity {
	case "auto", "quick", "standard", "deep":
	default:
		return fmt.Errorf("models.default_complexity: unknown value %q", c.Models.DefaultComplexity)
	}
	limits := c.Models.ExecutionLimits
	if limits.MaxDurationMinutes < 1 || limits.MaxDurationMinutes > 480 {
		return fmt.Errorf("models.execution_limits.max_duration_minutes must be 1–480")
	}
	if limits.MaxTokens < 10_000 || limits.MaxTokens > 10_000_000 {
		return fmt.Errorf("models.execution_limits.max_tokens must be 10000–10000000")
	}
	if limits.MaxCostUSD < 0.01 || limits.MaxCostUSD > 10_000 {
		return fmt.Errorf("models.execution_limits.max_cost_usd must be 0.01–10000")
	}
	if limits.MaxToolRounds < 1 || limits.MaxToolRounds > 1000 {
		return fmt.Errorf("models.execution_limits.max_tool_rounds must be 1–1000")
	}
	seen := map[string]bool{}
	modelIDs := map[string]bool{}
	for _, m := range c.Models.Configured {
		if m.ID == "" || m.Provider == "" || m.Model == "" {
			return fmt.Errorf("models.configured: id, provider and model are required")
		}
		if modelIDs[m.ID] {
			return fmt.Errorf("models.configured: duplicate id %q", m.ID)
		}
		modelIDs[m.ID] = true
	}
	poolIDs := map[string]bool{}
	for _, p := range c.Models.Pools {
		if p.ID == "" || p.Name == "" {
			return fmt.Errorf("models.pools: id and name are required")
		}
		if poolIDs[p.ID] {
			return fmt.Errorf("models.pools: duplicate id %q", p.ID)
		}
		poolIDs[p.ID] = true
		if p.Strategy != "" && p.Strategy != "priority" && p.Strategy != "balanced" && p.Strategy != "quality" && p.Strategy != "fast" && p.Strategy != "cheap" {
			return fmt.Errorf("models.pools.%s: unknown strategy %q", p.ID, p.Strategy)
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("models.pools.%s: at least one model is required", p.ID)
		}
		for _, id := range p.Models {
			if !modelIDs[id] {
				return fmt.Errorf("models.pools.%s: unknown configured model %q", p.ID, id)
			}
		}
	}
	if c.Models.DefaultPool != "" && !poolIDs[c.Models.DefaultPool] {
		return fmt.Errorf("models.default_pool: unknown pool %q", c.Models.DefaultPool)
	}
	for _, m := range c.MCPServers {
		if m.Name == "" {
			return fmt.Errorf("mcp_servers: every server needs a name")
		}
		if seen[m.Name] {
			return fmt.Errorf("mcp_servers: duplicate name %q", m.Name)
		}
		seen[m.Name] = true
		switch m.Transport {
		case "", "stdio":
			if m.Command == "" && m.IsEnabled() {
				return fmt.Errorf("mcp_servers.%s: command is required for stdio", m.Name)
			}
		case "http":
			if m.URL == "" && m.IsEnabled() {
				return fmt.Errorf("mcp_servers.%s: url is required for http", m.Name)
			}
		default:
			return fmt.Errorf("mcp_servers.%s: unknown transport %q", m.Name, m.Transport)
		}
	}
	if c.Runtime.EngineWSPort < 0 || c.Runtime.EngineWSPort > 65535 {
		return fmt.Errorf("runtime.engine_ws_port out of range: %d", c.Runtime.EngineWSPort)
	}
	return nil
}

// NormalizeProvider maps aliases to canonical provider ids.
func NormalizeProvider(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "anthropic", "claude":
		return "claude"
	case "google", "gemini":
		return "gemini"
	case "openai":
		return "openai"
	case "openai_compatible", "openai-compatible", "ollama", "lmstudio", "openrouter":
		return "openai_compatible"
	}
	return strings.ToLower(strings.TrimSpace(p))
}

func getenv(name string) (string, bool) {
	return os.LookupEnv("UMCODE_" + name)
}

func applyEnv(c *Config) {
	if v, ok := getenv("LLM_PROVIDER"); ok {
		c.LLM.Provider = v
	}
	if v, ok := getenv("LLM_MODEL"); ok {
		c.LLM.Model = v
	}
	if v, ok := getenv("LLM_API_KEY"); ok {
		c.LLM.APIKey = v
	}
	if v, ok := getenv("DB_PATH"); ok {
		c.Storage.DBPath = v
	}
	if v, ok := getenv("LOG_DIR"); ok {
		c.Runtime.LogDir = v
	}
	if v, ok := getenv("WS_HOST"); ok {
		c.Runtime.WSHost = v
	}
	if v, ok := getenv("ENGINE_WS_PORT"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.Runtime.EngineWSPort = n
		}
	}
	if v, ok := getenv("SHELL_TOOL"); ok {
		c.Tools.ShellEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := getenv("APPROVAL_MODE"); ok {
		c.Policy.ApprovalMode = v
	}
}

func expand(p string) string {
	if p == "" {
		return p
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return os.ExpandEnv(p)
}
