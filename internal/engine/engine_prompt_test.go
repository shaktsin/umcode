package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestSystemPromptIgnoresGlobalAgentMD(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	if err := os.WriteFile(filepath.Join(e.Cfg.Home, "AGENT.md"), []byte("SECRET-GLOBAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := e.systemPrompt(t.Context(), "hello", nil, "", nil)
	if strings.Contains(prompt, "SECRET-GLOBAL") || strings.Contains(prompt, "User context (AGENT.md)") {
		t.Fatalf("global AGENT.md reached the prompt: %s", prompt)
	}
}

var clockLine = regexp.MustCompile(`Current time: [^\n]*\n`)

// promptCases builds the system prompt for the inputs the golden files pin.
func promptCases(t *testing.T) map[string]func(e *Engine) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte("Root guidance."), 0o644)
	os.WriteFile(filepath.Join(root, "web", "UMCODE.md"), []byte("Web guidance."), 0o644)
	proj := &protocol.Project{ID: "prj_test", Name: "demo", Root: root}
	norm := func(s string) string {
		return strings.ReplaceAll(clockLine.ReplaceAllString(s, "Current time: <t>\n"), root, "<root>")
	}
	return map[string]func(e *Engine) string{
		"noproject": func(e *Engine) string {
			return norm(e.systemPrompt(t.Context(), "hi", nil, "", nil))
		},
		"project": func(e *Engine) string {
			return norm(e.systemPrompt(t.Context(), "hi", proj, "web/App.svelte", nil))
		},
		"hooks": func(e *Engine) string {
			return norm(e.systemPrompt(t.Context(), "hi", proj, "", []string{"", "  ", "ctx A"}))
		},
	}
}

func TestSystemPromptGolden(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	e.Projects = projects.New(e.Store, e.Cfg)
	for name, build := range promptCases(t) {
		got := build(e)
		path := filepath.Join("testdata", "system_prompt_"+name+".txt")
		if os.Getenv("UPDATE_GOLDEN") != "" {
			os.MkdirAll("testdata", 0o755)
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Fatalf("%s: prompt drifted from golden\n--- got ---\n%s", name, got)
		}
	}
}

func TestSystemPromptLayerOrder(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	e.Projects = projects.New(e.Store, e.Cfg)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte("Root guidance."), 0o644)
	proj := &protocol.Project{ID: "prj_layers", Name: "demo", Root: root}
	cases := []struct {
		name  string
		proj  *protocol.Project
		hooks []string
		want  []string
	}{
		{"noproject", nil, nil, []string{layerCore, layerClock, layerNotice}},
		{"project", proj, nil, []string{layerCore, layerClock, layerProject, layerInstructions}},
		{"hooks", proj, []string{"", "  ", "ctx A"}, []string{layerCore, layerClock, layerPlugin, layerProject, layerInstructions}},
	}
	for _, c := range cases {
		layers := e.systemPromptLayers(t.Context(), "hi", c.proj, "", c.hooks)
		var names []string
		for _, l := range layers {
			if l.Text == "" {
				t.Errorf("%s: empty layer %q", c.name, l.Name)
			}
			names = append(names, l.Name)
		}
		if strings.Join(names, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: layers = %v, want %v", c.name, names, c.want)
		}
	}
}
