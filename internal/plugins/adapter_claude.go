package plugins

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type claudeAdapter struct{}

func (claudeAdapter) Format() Format { return FormatClaude }

func (claudeAdapter) Detect(root string) (bool, error) {
	exists, err := fileExists(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return false, pluginError("detect", "detect/read", ".claude-plugin/plugin.json", "cannot inspect Claude plugin manifest", "Check the file and its permissions.", err)
	}
	return exists, nil
}

type rawClaudeManifest struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	Skills     json.RawMessage `json:"skills"`
	MCPServers json.RawMessage `json:"mcpServers"`
	Hooks      json.RawMessage `json:"hooks"`
	Agents     json.RawMessage `json:"agents"`
	Commands   json.RawMessage `json:"commands"`
	LSPServers json.RawMessage `json:"lspServers"`
	Settings   []Setting       `json:"settings"`
}

func (claudeAdapter) Load(root string) (Package, error) {
	var manifest rawClaudeManifest
	if err := readJSON(filepath.Join(root, ".claude-plugin", "plugin.json"), &manifest, "adapt"); err != nil {
		return Package{}, err
	}
	pkg := Package{ID: strings.TrimSpace(manifest.Name), Name: strings.TrimSpace(manifest.Name), Version: strings.TrimSpace(manifest.Version), Format: FormatClaude, Root: root, Settings: manifest.Settings}
	skillRoots, err := decodePaths(manifest.Skills, "./skills")
	if err != nil {
		return Package{}, pluginError("adapt", "adapt/invalid_skills", "skills", "skills must be a path or path array", "Reference package-relative skill roots.", err)
	}
	pkg.Skills, err = loadSkills(root, skillRoots)
	if err != nil {
		return Package{}, err
	}
	if path, ok := rawJSONString(manifest.MCPServers); ok {
		pkg.MCPServers, err = loadMCPPath(root, path)
	} else if len(manifest.MCPServers) > 0 && string(manifest.MCPServers) != "null" {
		wrapped := append([]byte(`{"mcpServers":`), manifest.MCPServers...)
		wrapped = append(wrapped, '}')
		pkg.MCPServers, err = loadMCPBytes(wrapped, ".claude-plugin/plugin.json#mcpServers")
	} else {
		pkg.MCPServers, err = loadMCPPath(root, "./.mcp.json")
	}
	if err != nil {
		return Package{}, err
	}
	hookPaths, err := decodePaths(manifest.Hooks, "./hooks/hooks.json")
	if err != nil {
		return Package{}, pluginError("adapt", "adapt/invalid_hooks", "hooks", "hooks must be a path or path array", "Reference package-relative hooks files.", err)
	}
	for _, path := range hookPaths {
		declarations, diagnostics, err := loadHooksPath(root, pkg.ID, path)
		if err != nil {
			return Package{}, err
		}
		pkg.Hooks = append(pkg.Hooks, declarations...)
		pkg.Diagnostics = append(pkg.Diagnostics, diagnostics...)
	}
	if err := addUnsupportedPaths(root, &pkg, "agent", manifest.Agents); err != nil {
		return Package{}, err
	}
	if err := addUnsupportedPaths(root, &pkg, "command", manifest.Commands); err != nil {
		return Package{}, err
	}
	if err := addUnsupportedObject(&pkg, "lsp", manifest.LSPServers); err != nil {
		return Package{}, err
	}
	addSupportedComponents(&pkg)
	return pkg, nil
}

func addUnsupportedPaths(root string, pkg *Package, kind string, raw json.RawMessage) error {
	paths, err := decodePaths(raw, "")
	if err != nil {
		return pluginError("adapt", "adapt/invalid_component", kind, "unsupported component declaration is malformed", "Use a path or path array.", err)
	}
	for _, path := range paths {
		rel, _, err := resolveResource(root, path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
		pkg.Components = append(pkg.Components, Component{Kind: kind, Name: name, Path: rel, Supported: false})
		pkg.Diagnostics = append(pkg.Diagnostics, Diagnostic{Code: "compat/unsupported_component", Phase: "adapt", Severity: SeverityWarning, Component: kind + ":" + name, Message: kind + " components are not executable in this UMCode release", Remediation: "The component remains inventoried; use a supported skill, MCP server, or hook for runtime behavior."})
	}
	return nil
}

func addUnsupportedObject(pkg *Package, kind string, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return pluginError("adapt", "adapt/invalid_component", kind, "unsupported component declaration is malformed", "Use an object keyed by component name.", err)
	}
	for name := range values {
		pkg.Components = append(pkg.Components, Component{Kind: kind, Name: name, Supported: false})
		pkg.Diagnostics = append(pkg.Diagnostics, Diagnostic{Code: "compat/unsupported_component", Phase: "adapt", Severity: SeverityWarning, Component: kind + ":" + name, Message: kind + " components are not executable in this UMCode release", Remediation: "The component remains inventoried."})
	}
	return nil
}
