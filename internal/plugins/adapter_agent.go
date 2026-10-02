package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type agentAdapter struct{}

func (agentAdapter) Format() Format { return FormatAgent }

func (agentAdapter) Detect(root string) (bool, error) {
	path := filepath.Join(root, "plugin.json")
	exists, err := fileExists(path)
	if err != nil {
		return false, pluginError("detect", "detect/read", "plugin.json", "cannot inspect root plugin.json", "Check the file and its permissions.", err)
	}
	if !exists {
		return false, nil
	}
	var manifest struct {
		Schema string `json:"$schema"`
	}
	if err := readJSON(path, &manifest, "detect"); err != nil {
		return false, err
	}
	if manifest.Schema == agentPluginSchema {
		return true, nil
	}
	// A root plugin.json is portable only when it declares an Agent Plugins
	// schema. Other tools commonly use the same filename, so let the Codex and
	// Claude adapters inspect their dedicated manifest folders.
	if strings.Contains(manifest.Schema, "agent-plugins.org/") {
		return true, nil
	}
	return false, nil
}

type rawAgentManifest struct {
	Schema      string                     `json:"$schema"`
	Name        string                     `json:"name"`
	Version     string                     `json:"version"`
	Description string                     `json:"description"`
	Extensions  map[string]json.RawMessage `json:"extensions"`
	Settings    []Setting                  `json:"settings"`
}

type rawOpenAIExtension struct {
	Hooks json.RawMessage `json:"hooks"`
}

func (agentAdapter) Load(root string) (Package, error) {
	var manifest rawAgentManifest
	if err := readJSON(filepath.Join(root, "plugin.json"), &manifest, "adapt"); err != nil {
		return Package{}, err
	}
	if manifest.Schema != agentPluginSchema {
		return Package{}, pluginError("adapt", "adapt/unsupported_schema", "plugin.json", fmt.Sprintf("unsupported Agent Plugins schema %q", manifest.Schema), "Use https://agent-plugins.org/schemas/1.0.0/plugin.schema.json.", nil)
	}
	pkg := Package{ID: strings.TrimSpace(manifest.Name), Name: strings.TrimSpace(manifest.Name), Version: strings.TrimSpace(manifest.Version), Format: FormatAgent, Root: root, Settings: manifest.Settings}
	var err error
	pkg.Skills, err = loadSkills(root, []string{"./skills"})
	if err != nil {
		return Package{}, err
	}
	pkg.MCPServers, err = loadMCPPath(root, "./mcp.json")
	if err != nil {
		return Package{}, err
	}

	var hookRaw json.RawMessage
	if extension, ok := manifest.Extensions["com.openai"]; ok {
		var parsed rawOpenAIExtension
		if err := json.Unmarshal(extension, &parsed); err != nil {
			return Package{}, pluginError("adapt", "adapt/invalid_extension", "extensions.com.openai", "invalid OpenAI plugin extension", "Correct the extension JSON.", err)
		}
		hookRaw = parsed.Hooks
	} else if extension, ok := manifest.Extensions["dev.umcode"]; ok {
		var parsed rawOpenAIExtension
		if err := json.Unmarshal(extension, &parsed); err != nil {
			return Package{}, pluginError("adapt", "adapt/invalid_extension", "extensions.dev.umcode", "invalid UMCode plugin extension", "Correct the extension JSON.", err)
		}
		hookRaw = parsed.Hooks
	}
	if len(hookRaw) == 0 {
		overlayPath := filepath.Join(root, ".codex-plugin", "plugin.json")
		if exists, _ := fileExists(overlayPath); exists {
			var overlay rawLegacyManifest
			if err := readJSON(overlayPath, &overlay, "adapt"); err != nil {
				return Package{}, err
			}
			hookRaw = overlay.Hooks
		}
	}
	paths, err := decodePaths(hookRaw, "")
	if err != nil {
		return Package{}, pluginError("adapt", "adapt/invalid_hooks", "hooks", "hooks must be a path or path array", "Reference a package-relative hooks file.", err)
	}
	for _, path := range paths {
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

func readRawFile(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	return json.RawMessage(data), err
}
