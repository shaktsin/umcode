package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/tools"
)

const weatherSkill = `---
name: weather-fetch
description: Fetch the weather forecast for a city
risk_level: green
version: 1.2.0
triggers: ["weather"]
runtime:
  type: shell
  timeout_seconds: 5
  env:
    UNITS: metric
scripts:
  forecast:
    path: scripts/forecast.sh
    description: Print the forecast
    input_schema:
      type: object
      properties:
        city: {type: string}
      required: [city]
  slow:
    path: scripts/slow.sh
    description: Sleeps
---
# Weather

Use the forecast script.
`

func writeSkill(t *testing.T, dir, name, body string, files map[string]string) string {
	t.Helper()
	d := filepath.Join(dir, name)
	os.MkdirAll(filepath.Join(d, "scripts"), 0o755)
	os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644)
	for p, c := range files {
		os.WriteFile(filepath.Join(d, p), []byte(c), 0o755)
	}
	return d
}

func newTestRegistry(t *testing.T) (*Registry, *config.Config, string) {
	home := t.TempDir()
	extra := t.TempDir()
	cfg := config.Default(home)
	cfg.SkillDirs = []string{extra}
	cfg.Skills = map[string]config.SkillRuntimeOverride{
		"defaults":      {Env: map[string]string{"FROM_DEFAULTS": "d"}},
		"weather-fetch": {Env: map[string]string{"UNITS": "imperial"}, Config: map[string]any{"api": "x"}},
	}
	t.Chdir(t.TempDir())
	return NewRegistry(cfg), cfg, extra
}

func TestParseAndRun(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	r, _, extra := newTestRegistry(t)
	writeSkill(t, extra, "weather-fetch", weatherSkill, map[string]string{
		"scripts/forecast.sh": "#!/bin/bash\nread -r input\necho \"input=$input units=$UNITS defaults=$FROM_DEFAULTS key=${OPENAI_API_KEY:-none} dir=$(basename $SKILL_DIR)\"\n",
		"scripts/slow.sh":     "#!/bin/bash\nsleep 10\n",
	})
	writeSkill(t, extra, "Bad_Name", "---\nname: Bad_Name\ndescription: x\n---\n", nil)

	list := r.List()
	if len(list) != 2 || list[0].Name != "weather-fetch" || list[1].Error == "" {
		t.Fatalf("list = %+v", list)
	}
	s, _ := r.Get("weather-fetch")
	if s.Version != "1.2.0" || s.RiskLevel != "green" || s.Runtime.TimeoutSeconds != 5 {
		t.Fatalf("parsed = %+v", s)
	}
	out, err := r.RunScript(context.Background(), "weather-fetch", "forecast", []byte(`{"city":"Pune"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`input={"config":{"api":"x"},"input":{"city":"Pune"}}`, "units=imperial", "defaults=d", "key=none", "dir=weather-fetch"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	if _, err := r.RunScript(context.Background(), "weather-fetch", "slow", nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout: %v", err)
	}
	if _, err := r.RunScript(context.Background(), "weather-fetch", "nope", nil); err == nil {
		t.Error("unknown script should fail")
	}
	if !strings.Contains(r.Catalog(), "weather-fetch: Fetch the weather") {
		t.Errorf("catalog = %s", r.Catalog())
	}
	if m, ok := r.Match("what's the weather forecast in Pune"); !ok || m.Name != "weather-fetch" {
		t.Error("match failed")
	}
	if _, ok := r.Match("hello there"); ok {
		t.Error("unexpected match")
	}
}

func TestPluginSkillsAreNamespacedAndContextBound(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "greet")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: greet\ndescription: Greet someone from a plugin.\n---\nPlugin instructions."), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, errs := CompileSnapshot([]Root{{PluginID: "acme", Paths: []string{skillDir}, Config: map[string]any{"tone": "warm"}}})
	if len(errs) != 0 {
		t.Fatalf("CompileSnapshot() errors = %v", errs)
	}
	cfg := config.Default(t.TempDir())
	registry := NewRegistry(cfg)
	if _, ok := registry.Get("acme:greet"); ok {
		t.Fatal("plugin skill leaked into legacy registry")
	}
	ctx := WithSnapshot(context.Background(), snapshot)
	skill, ok := registry.GetContext(ctx, "acme:greet")
	if !ok || skill.Name != "acme:greet" || skill.Dir != skillDir {
		t.Fatalf("GetContext() = %#v, %v", skill, ok)
	}
	if catalog := registry.CatalogContext(ctx); !strings.Contains(catalog, "acme:greet: Greet someone") {
		t.Fatalf("CatalogContext() = %q", catalog)
	}
	matched, ok := registry.MatchContext(ctx, "please greet someone")
	if !ok || matched.Name != "acme:greet" {
		t.Fatalf("MatchContext() = %#v, %v", matched, ok)
	}
	if got := snapshot.Config("acme:greet"); !reflect.DeepEqual(got, map[string]any{"tone": "warm"}) {
		t.Fatalf("snapshot config = %#v", got)
	}
}

func TestPluginSkillResolvesRuntimeReferenceAndRedactsOutput(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "secure")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: secure\ndescription: Secure plugin skill.\nruntime:\n  type: shell\n  env:\n    TOKEN: '{{secret:api_key}}'\nscripts:\n  show:\n    path: show.sh\n---\nSecure.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "show.sh"), []byte("#!/bin/sh\nprintf '%s' \"$TOKEN\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot, errs := CompileSnapshot([]Root{{
		PluginID: "acme", Paths: []string{skillDir},
		RuntimeValues: map[string]string{"{{secret:api_key}}": "plugin-secret"}, SecretValues: []string{"plugin-secret"},
	}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	registry := NewRegistry(config.Default(t.TempDir()))
	out, err := registry.RunScript(WithSnapshot(t.Context(), snapshot), "acme:secure", "show", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "[REDACTED]" {
		t.Fatalf("script output = %q", out)
	}
	if _, err := registry.EnvFor(WithSnapshot(t.Context(), snapshot), "acme:secure"); err == nil {
		t.Fatal("secret-bearing plugin skill environment was exposed to shell.run")
	}
}

func TestToolsAndInstall(t *testing.T) {
	r, cfg, _ := newTestRegistry(t)
	src := writeSkill(t, t.TempDir(), "weather-fetch", weatherSkill, map[string]string{
		"scripts/forecast.sh": "#!/bin/bash\ncat\n", "scripts/slow.sh": "#!/bin/bash\n",
	})
	os.MkdirAll(filepath.Join(src, ".venv", "bin"), 0o755) // must not be copied
	s, err := r.Install(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(s.Dir) != InstallDir(cfg) {
		t.Fatalf("installed at %s", s.Dir)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ".venv")); err == nil {
		t.Error(".venv was copied")
	}
	if _, err := r.Install(context.Background(), src, ""); err == nil {
		t.Error("second install should fail")
	}

	reg := tools.NewRegistry()
	r.Register(reg)
	gi, _ := reg.Get("skill__get_instructions")
	out, err := gi.Call(context.Background(), json.RawMessage(`{"skill_name":"weather-fetch"}`))
	if err != nil || !strings.Contains(out, "Use the forecast script.") || !strings.Contains(out, `"required":["city"]`) {
		t.Fatalf("instructions = %q %v", out, err)
	}
	rs, _ := reg.Get("skill.run_script")
	if risk, _ := rs.Assess(json.RawMessage(`{"skill":"weather-fetch","script":"forecast"}`)); risk != tools.RiskGreen {
		t.Errorf("risk = %s", risk)
	}
	if _, err := rs.Call(context.Background(), json.RawMessage(`{"skill":"weather-fetch","script":"forecast","args":{}}`)); err == nil || !strings.Contains(err.Error(), "city") {
		t.Errorf("required check: %v", err)
	}
	out, err = rs.Call(context.Background(), json.RawMessage(`{"skill":"weather-fetch","script":"forecast","args":{"city":"Oslo"}}`))
	if err != nil || !strings.Contains(out, "Oslo") {
		t.Fatalf("run = %q %v", out, err)
	}
	if err := r.Remove("weather-fetch"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("weather-fetch"); ok {
		t.Error("still installed")
	}
}

func TestTemplateSkillParses(t *testing.T) {
	s, err := Parse(filepath.Join("..", "..", "skill-template"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "example" || len(s.Scripts) != 3 || s.Scripts["hello_python"].Path != "scripts/hello.py" {
		t.Fatalf("template = %+v", s)
	}
}

func TestTemplateScriptsRun(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	cfg := config.Default(t.TempDir())
	cfg.SkillDirs = []string{root}
	cfg.Skills = map[string]config.SkillRuntimeOverride{"example": {Config: map[string]any{"greeting_prefix": "Namaste"}}}
	t.Chdir(t.TempDir())
	r := NewRegistry(cfg)
	for _, script := range []string{"hello_python", "hello_bash"} {
		out, err := r.RunScript(context.Background(), "example", script, []byte(`{"name":"Ada"}`))
		if err != nil {
			t.Fatalf("%s: %v", script, err)
		}
		if !strings.Contains(out, "Namaste, Ada!") {
			t.Errorf("%s output = %s", script, out)
		}
	}
}
