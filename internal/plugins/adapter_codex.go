package plugins

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type codexAdapter struct{}

func (codexAdapter) Format() Format { return FormatCodex }

func (codexAdapter) Detect(root string) (bool, error) {
	exists, err := fileExists(filepath.Join(root, ".codex-plugin", "plugin.json"))
	if err != nil {
		return false, pluginError("detect", "detect/read", ".codex-plugin/plugin.json", "cannot inspect Codex plugin manifest", "Check the file and its permissions.", err)
	}
	return exists, nil
}

type rawLegacyManifest struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	Skills     json.RawMessage `json:"skills"`
	MCPServers json.RawMessage `json:"mcpServers"`
	Hooks      json.RawMessage `json:"hooks"`
}

func (codexAdapter) Load(root string) (Package, error) {
	var manifest rawLegacyManifest
	if err := readJSON(filepath.Join(root, ".codex-plugin", "plugin.json"), &manifest, "adapt"); err != nil {
		return Package{}, err
	}
	pkg := Package{ID: strings.TrimSpace(manifest.Name), Name: strings.TrimSpace(manifest.Name), Version: strings.TrimSpace(manifest.Version), Format: FormatCodex, Root: root}
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
		pkg.MCPServers, err = loadMCPBytes(wrapped, ".codex-plugin/plugin.json#mcpServers")
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
	addSupportedComponents(&pkg)
	return pkg, nil
}

func rawJSONString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}
