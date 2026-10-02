package plugins

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/skills"
)

const agentPluginSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"

func LoadPackage(root string) (Package, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return Package{}, pluginError("detect", "detect/invalid_root", "", "invalid plugin root", "Choose a readable plugin directory.", err)
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return Package{}, pluginError("detect", "detect/invalid_root", "", "invalid plugin root", "Choose a readable plugin directory.", err)
	}
	adapters := []Adapter{agentAdapter{}, codexAdapter{}, claudeAdapter{}}
	for _, adapter := range adapters {
		detected, detectErr := adapter.Detect(rootAbs)
		if detectErr != nil {
			return Package{}, detectErr
		}
		if !detected {
			continue
		}
		pkg, loadErr := adapter.Load(rootAbs)
		if loadErr != nil {
			return Package{}, loadErr
		}
		pkg.Root = rootAbs
		if diagnostics := ValidatePackage(pkg); hasDiagnosticErrors(diagnostics) {
			for _, diagnostic := range diagnostics {
				if diagnostic.Severity == SeverityError {
					return Package{}, &Error{Diagnostic: diagnostic}
				}
			}
		}
		return pkg, nil
	}
	return Package{}, pluginError("detect", "detect/no_manifest", "manifest", "no supported plugin manifest found", "Add plugin.json, .codex-plugin/plugin.json, or .claude-plugin/plugin.json.", nil)
}

func hasDiagnosticErrors(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}

func fileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func readJSON(path string, dst any, phase string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return pluginError(phase, phase+"/read", filepath.Base(path), "cannot read plugin declaration", "Check the file and its permissions.", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return pluginError(phase, phase+"/invalid_json", filepath.Base(path), "invalid plugin JSON", "Correct the JSON declaration.", err)
	}
	return nil
}

func decodePaths(raw json.RawMessage, fallback string) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		if fallback == "" {
			return nil, nil
		}
		return []string{fallback}, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many, nil
	}
	return nil, fmt.Errorf("expected a path or path array")
}

func loadSkills(root string, roots []string) ([]SkillComponent, error) {
	var components []SkillComponent
	seen := map[string]bool{}
	for _, declared := range roots {
		_, absolute, err := resolveResource(root, declared)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(absolute); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != "SKILL.md" {
				return nil
			}
			dir := filepath.Dir(path)
			skill, err := skills.Parse(dir)
			if err != nil {
				return pluginError("adapt", "adapt/invalid_skill", filepath.ToSlash(dir), "invalid plugin skill", "Correct the SKILL.md frontmatter.", err)
			}
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				return err
			}
			if _, _, err := resolveResource(root, rel); err != nil {
				return err
			}
			if !seen[skill.Name] {
				components = append(components, SkillComponent{Name: skill.Name, Path: filepath.ToSlash(rel)})
				seen[skill.Name] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	return components, nil
}

type rawMCPFile struct {
	Servers map[string]rawMCPServer `json:"mcpServers"`
}

type rawMCPServer struct {
	Type              string            `json:"type"`
	Transport         string            `json:"transport"`
	Command           string            `json:"command"`
	Args              []string          `json:"args"`
	Env               map[string]string `json:"env"`
	EnvVars           []string          `json:"env_vars"`
	Cwd               string            `json:"cwd"`
	URL               string            `json:"url"`
	BearerTokenEnvVar string            `json:"bearer_token_env_var"`
	HTTPHeaders       map[string]string `json:"http_headers"`
	EnvHTTPHeaders    map[string]string `json:"env_http_headers"`
	Required          bool              `json:"required"`
	StartupTimeoutSec float64           `json:"startup_timeout_sec"`
	ToolTimeoutSec    float64           `json:"tool_timeout_sec"`
	EnabledTools      []string          `json:"enabled_tools"`
	DisabledTools     []string          `json:"disabled_tools"`
	RiskLevel         string            `json:"risk_level"`
}

func loadMCPPath(root, resource string) ([]MCPComponent, error) {
	rel, absolute, err := resolveResource(root, resource)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absolute)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadMCPBytes(data, rel)
}

func loadMCPBytes(data []byte, source string) ([]MCPComponent, error) {
	var raw rawMCPFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, pluginError("adapt", "adapt/invalid_mcp", source, "invalid MCP declaration", "Correct the MCP JSON.", err)
	}
	keys := make([]string, 0, len(raw.Servers))
	for name := range raw.Servers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	components := make([]MCPComponent, 0, len(keys))
	for _, name := range keys {
		rawServer := raw.Servers[name]
		transport := rawServer.Transport
		if transport == "" {
			transport = rawServer.Type
		}
		if transport == "" {
			transport = "stdio"
		}
		cfg := config.MCPServerConfig{Name: name, Transport: transport, Command: rawServer.Command, Args: rawServer.Args, Env: rawServer.Env, EnvVars: rawServer.EnvVars, Cwd: rawServer.Cwd, URL: rawServer.URL, BearerTokenEnvVar: rawServer.BearerTokenEnvVar, HTTPHeaders: rawServer.HTTPHeaders, EnvHTTPHeaders: rawServer.EnvHTTPHeaders, Required: rawServer.Required, StartupTimeoutSec: rawServer.StartupTimeoutSec, ToolTimeoutSec: rawServer.ToolTimeoutSec, EnabledTools: rawServer.EnabledTools, DisabledTools: rawServer.DisabledTools, RiskLevel: rawServer.RiskLevel}
		components = append(components, MCPComponent{Name: name, Path: source, Config: cfg, Required: cfg.Required})
	}
	return components, nil
}

type rawHooksFile struct {
	Hooks map[string][]rawHookGroup `json:"hooks"`
}

type rawHookGroup struct {
	Matcher string         `json:"matcher"`
	Hooks   []rawHookEntry `json:"hooks"`
}

type rawHookEntry struct {
	Type           string            `json:"type"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	Required       bool              `json:"required"`
	Timeout        int               `json:"timeout"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

func loadHooksPath(root, pluginID, resource string) ([]hooks.Declaration, []Diagnostic, error) {
	rel, absolute, err := resolveResource(root, resource)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(absolute)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return loadHooksBytes(root, pluginID, rel, data)
}

func loadHooksBytes(root, pluginID, source string, data []byte) ([]hooks.Declaration, []Diagnostic, error) {
	var raw rawHooksFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, pluginError("adapt", "adapt/invalid_hooks", source, "invalid hooks declaration", "Correct the hooks JSON.", err)
	}
	var declarations []hooks.Declaration
	var diagnostics []Diagnostic
	order := 0
	for sourceEvent, groups := range raw.Hooks {
		event, ok := mapHookEvent(sourceEvent)
		if !ok {
			diagnostics = append(diagnostics, Diagnostic{Code: "compat/unsupported_hook_event", Phase: "adapt", Severity: SeverityWarning, Component: "hook:" + sourceEvent, Message: "hook event has no safe UMCode equivalent", Remediation: "Use a supported lifecycle event."})
			continue
		}
		for _, group := range groups {
			for _, entry := range group.Hooks {
				if entry.Type != "" && entry.Type != "command" {
					diagnostics = append(diagnostics, Diagnostic{Code: "compat/unsupported_hook_type", Phase: "adapt", Severity: SeverityWarning, Component: "hook:" + sourceEvent, Message: "only command hooks are supported", Remediation: "Declare a command hook."})
					continue
				}
				timeout := entry.TimeoutSeconds
				if timeout == 0 {
					timeout = entry.Timeout
				}
				declaration := hooks.Declaration{PluginID: pluginID, Root: root, Source: source, Event: event, Matcher: group.Matcher, Command: strings.TrimSpace(entry.Command), Args: entry.Args, Env: entry.Env, Required: entry.Required, Order: order}
				if timeout > 0 {
					declaration.Timeout = durationSeconds(timeout)
				}
				declarations = append(declarations, declaration)
				order++
			}
		}
	}
	sort.SliceStable(declarations, func(i, j int) bool {
		if declarations[i].Event == declarations[j].Event {
			return declarations[i].Order < declarations[j].Order
		}
		return declarations[i].Event < declarations[j].Event
	})
	return declarations, diagnostics, nil
}

func durationSeconds(seconds int) time.Duration { return time.Duration(seconds) * time.Second }

func mapHookEvent(source string) (hooks.Event, bool) {
	switch source {
	case "SessionStart", "TurnStart", "UserPromptSubmit":
		return hooks.TurnStart, true
	case "PreToolUse", "BeforeToolUse":
		return hooks.BeforeToolUse, true
	case "PostToolUse", "AfterToolUse":
		return hooks.AfterToolUse, true
	case "PostToolUseFailure", "ToolUseFailed":
		return hooks.ToolUseFailed, true
	case "Stop", "SessionEnd", "TurnComplete":
		return hooks.TurnComplete, true
	case "PreCompact", "BeforeCompaction":
		return hooks.BeforeCompaction, true
	case "PostCompact", "AfterCompaction":
		return hooks.AfterCompaction, true
	default:
		return "", false
	}
}

func addSupportedComponents(pkg *Package) {
	for _, skill := range pkg.Skills {
		pkg.Components = append(pkg.Components, Component{Kind: "skill", Name: skill.Name, Path: skill.Path, Required: skill.Required, Supported: true})
	}
	for _, server := range pkg.MCPServers {
		pkg.Components = append(pkg.Components, Component{Kind: "mcp", Name: server.Name, Path: server.Path, Required: server.Required, Supported: true})
	}
	for _, declaration := range pkg.Hooks {
		pkg.Components = append(pkg.Components, Component{Kind: "hook", Name: string(declaration.Event), Path: declaration.Source, Required: declaration.Required, Supported: true})
	}
}
