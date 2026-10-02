package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/hooks"
)

func TestPackageValidationRejectsInvalidSettingSchema(t *testing.T) {
	pkg := Package{ID: "valid-plugin", Settings: []Setting{{Name: "", Type: "string"}, {Name: "mode", Type: "map"}}}
	diagnostics := ValidatePackage(pkg)
	if !hasErrorDiagnostic(diagnostics) {
		t.Fatalf("invalid setting schema passed validation: %#v", diagnostics)
	}
}

func TestAdaptersNormalizeEquivalentCapabilities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		root   string
		format Format
	}{
		{name: "portable", root: "testdata/portable", format: FormatAgent},
		{name: "codex", root: "testdata/codex", format: FormatCodex},
		{name: "claude", root: "testdata/claude", format: FormatClaude},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg, err := LoadPackage(tc.root)
			if err != nil {
				t.Fatalf("LoadPackage() error = %v", err)
			}
			if pkg.Format != tc.format {
				t.Fatalf("Format = %q, want %q", pkg.Format, tc.format)
			}
			if pkg.ID != "sample-plugin" || pkg.Name != "sample-plugin" || pkg.Version != "1.0.0" {
				t.Fatalf("identity = (%q, %q, %q), want sample-plugin 1.0.0", pkg.ID, pkg.Name, pkg.Version)
			}
			if len(pkg.Skills) != 1 || pkg.Skills[0].Name != "greet" || pkg.Skills[0].Path != "skills/greet" {
				t.Fatalf("Skills = %#v, want greet at skills/greet", pkg.Skills)
			}
			if len(pkg.MCPServers) != 1 || pkg.MCPServers[0].Name != "echo" {
				t.Fatalf("MCPServers = %#v, want echo", pkg.MCPServers)
			}
			if got := pkg.MCPServers[0].Config.Command; got != "echo-server" {
				t.Fatalf("MCP command = %q, want echo-server", got)
			}
			if len(pkg.Hooks) != 1 || pkg.Hooks[0].Event != hooks.TurnStart || pkg.Hooks[0].Command != "hook-helper" {
				t.Fatalf("Hooks = %#v, want TurnStart hook-helper", pkg.Hooks)
			}
			if diagnostics := ValidatePackage(pkg); hasErrorDiagnostic(diagnostics) {
				t.Fatalf("ValidatePackage() diagnostics = %#v, want no errors", diagnostics)
			}
		})
	}
}

func TestPortableManifestWinsOverCodexOverlayIdentity(t *testing.T) {
	t.Parallel()

	pkg, err := LoadPackage("testdata/portable")
	if err != nil {
		t.Fatalf("LoadPackage() error = %v", err)
	}
	if pkg.Format != FormatAgent || pkg.ID != "sample-plugin" || pkg.Version != "1.0.0" {
		t.Fatalf("portable identity = (%q, %q, %q), want agent sample-plugin 1.0.0", pkg.Format, pkg.ID, pkg.Version)
	}
}

func TestUnsupportedComponentsRemainVisible(t *testing.T) {
	t.Parallel()

	pkg, err := LoadPackage("testdata/claude")
	if err != nil {
		t.Fatalf("LoadPackage() error = %v", err)
	}
	var found bool
	for _, component := range pkg.Components {
		if component.Kind == "agent" && component.Name == "reviewer" && !component.Supported {
			found = true
		}
	}
	if !found {
		t.Fatalf("Components = %#v, want visible unsupported reviewer agent", pkg.Components)
	}
	var warned bool
	for _, diagnostic := range pkg.Diagnostics {
		if diagnostic.Code == "compat/unsupported_component" && diagnostic.Component == "agent:reviewer" {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("Diagnostics = %#v, want unsupported component warning", pkg.Diagnostics)
	}
}

func TestLoadPackageRejectsEscapingResources(t *testing.T) {
	t.Parallel()

	t.Run("parent traversal", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), `{"name":"escape","skills":"../skills"}`)
		assertPathEscape(t, root)
	})

	t.Run("absolute path", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), `{"name":"escape","hooks":"/tmp/hooks.json"}`)
		assertPathEscape(t, root)
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), `{"name":"escape","skills":"./skills"}`)
		writeTestFile(t, filepath.Join(outside, "greet", "SKILL.md"), "---\nname: greet\ndescription: outside\n---\n")
		if err := os.Symlink(outside, filepath.Join(root, "skills")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertPathEscape(t, root)
	})
}

func TestUnrelatedRootPluginJSONFallsBackToCodexManifest(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "plugin.json"), `{"name":"unrelated-app-config"}`)
	writeTestFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), `{"name":"codex-fallback","version":"1.0.0"}`)
	pkg, err := LoadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Format != FormatCodex || pkg.Name != "codex-fallback" {
		t.Fatalf("package = %#v", pkg)
	}
}

func assertPathEscape(t *testing.T, root string) {
	t.Helper()
	_, err := LoadPackage(root)
	if err == nil {
		t.Fatal("LoadPackage() error = nil, want path escape rejection")
	}
	var pluginErr *Error
	if !errors.As(err, &pluginErr) {
		t.Fatalf("LoadPackage() error type = %T, want *plugins.Error", err)
	}
	if pluginErr.Diagnostic.Phase != "validate" || pluginErr.Diagnostic.Code != "validate/path_escape" {
		t.Fatalf("diagnostic = %#v, want validate/path_escape", pluginErr.Diagnostic)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasErrorDiagnostic(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}
