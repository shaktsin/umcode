package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
)

// extraPromptCases pin the prompt for inputs the base golden cases miss: the
// skills layer, the compute and git lines, and blank hook context without a project.
func extraPromptCases(t *testing.T, e *Engine) map[string]string {
	t.Helper()
	root := t.TempDir()
	skillsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillsDir, "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: greet\ndescription: Greet someone warmly.\n---\nSay hello."
	if err := os.WriteFile(filepath.Join(skillsDir, "greet", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	compute := true
	proj := &protocol.Project{ID: "prj_extra", Name: "demo", Root: root,
		Tools: protocol.ProjectTools{Compute: &compute},
		VCS:   &protocol.VCSInfo{Kind: "git", Branch: "main", Dirty: 3}}
	norm := func(s string) string {
		s = clockLine.ReplaceAllString(s, "Current time: <t>\n")
		return strings.ReplaceAll(strings.ReplaceAll(s, root, "<root>"), skillsDir, "<skills>")
	}
	e.Projects = projects.New(e.Store, e.Cfg)
	out := map[string]string{
		"noproject_blankhooks": norm(e.systemPrompt(t.Context(), "hi", nil, "", []string{"", "  "})),
		"compute_vcs":          norm(e.systemPrompt(t.Context(), "hi", proj, "", nil)),
	}
	e.Cfg.SkillDirs = []string{skillsDir}
	out["skills_match"] = norm(e.systemPrompt(t.Context(), "please greet my friend", nil, "", nil))
	return out
}

func TestSystemPromptGoldenExtra(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	for name, got := range extraPromptCases(t, e) {
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
