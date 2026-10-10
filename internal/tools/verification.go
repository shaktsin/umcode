package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shaktsin/umcode/internal/optimization"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type plannedCheck struct {
	Label           string `json:"label"`
	Command         string `json:"command"`
	Directory       string `json:"directory,omitempty"`
	Reason          string `json:"reason"`
	RequiredCommand string `json:"required_command,omitempty"`
	Kind            string `json:"kind"`
}

type verificationPlan struct{ designedWorkflow bool }

func (*verificationPlan) Name() string { return "verification.plan" }

const verificationPlanDefaultDescription = "Inspect changed files and project manifests without executing project code, then return a compact, reviewable verification plan with a reason for every check. Call this before verification.run and pass its non-browser checks through unchanged."
const verificationPlanLegacyDescription = "Inspect project verification checks before implementation. Without a project, plan acceptance of a specific proposed approach by human workflow approval; link that criterion to its gated decision. Planning does not approve or verify anything."

func (t *verificationPlan) Description() string {
	if t.designedWorkflow {
		return verificationPlanLegacyDescription
	}
	return verificationPlanDefaultDescription
}
func (t *verificationPlan) DescriptionContext(ctx context.Context) string {
	if p, ok := optimization.FromContext(ctx); ok {
		if p.AutomaticWorkflow {
			return "Inspect project verification checks without executing project code and return a compact plan with reasons. Without a project, report the available evidence-based checks. Planning does not approve or verify anything."
		}
		if !p.DesignedWorkflow {
			return verificationPlanDefaultDescription
		}
		return verificationPlanLegacyDescription
	}
	return t.Description()
}
func (*verificationPlan) Schema() json.RawMessage { return schema(`{"type":"object","properties":{}}`) }
func (*verificationPlan) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Inspect project verification configuration"
}
func (t *verificationPlan) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	scope := ScopeFrom(ctx)
	automatic := false
	designed := t.designedWorkflow
	if p, ok := optimization.FromContext(ctx); ok {
		designed = p.DesignedWorkflow
		automatic = p.AutomaticWorkflow
	}
	if scope == nil || scope.Root == "" {
		if automatic && scope != nil && scope.ThreadID != "" {
			return `{"checks":[{"label":"Evidence-linked approach and completed deliverable","command":"workflow:evidence","kind":"evidence","reason":"Link this non-executable criterion to the selected decision and deliverable tasks. Completion requires supporting observed evidence and completed tasks; it is neither human acceptance nor an execution test pass."}],"summary":"No executable project checks are available. Link the evidence criterion to the approach and tasks; report execution checks as not run."}`, nil
		}
		if designed && scope != nil && scope.ThreadID != "" {
			return `{"checks":[{"label":"Human acceptance of the linked proposed approach","command":"workflow:approval","kind":"approval","reason":"Only approval of the exact linked decision satisfies this criterion; this is not execution or outcome verification."}],"summary":"Link this acceptance criterion to the specific decision requiring human review. Other requirements need their own verification."}`, nil
		}
		return "", ErrNoProject
	}
	changed := gitChangedFiles(ctx, scope.Root)
	checks, manifests := planChecks(scope.Root, changed)
	result := struct {
		ChangedFiles []string       `json:"changed_files"`
		Manifests    []string       `json:"manifests"`
		Checks       []plannedCheck `json:"checks"`
		VisualQA     string         `json:"visual_qa"`
		Summary      string         `json:"summary"`
	}{ChangedFiles: changed, Manifests: manifests, Checks: checks}
	if frontendChanged(changed) {
		result.VisualQA = "required: start the preview, then use visual.start, visual.act for affected user flows, and visual.inspect with a final screenshot"
	} else {
		result.VisualQA = "not_applicable"
	}
	if len(checks) == 0 {
		result.Summary = "No configured executable checks matched the changed files; report verification as not run and explain that the project exposes no applicable check."
	} else {
		result.Summary = fmt.Sprintf("Run %d configured check(s) in order; browser checks must use browser.verify so artifacts are collected.", len(checks))
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func frontendChanged(changed []string) bool {
	for _, path := range changed {
		lower := strings.ToLower(filepath.ToSlash(path))
		ext := strings.ToLower(filepath.Ext(lower))
		switch ext {
		case ".html", ".css", ".scss", ".sass", ".less", ".svelte", ".vue", ".jsx", ".tsx":
			return true
		}
		if strings.Contains(lower, "/components/") || strings.Contains(lower, "/pages/") || strings.Contains(lower, "/routes/") {
			return true
		}
	}
	return false
}

func gitChangedFiles(ctx context.Context, root string) []string {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "-C", root, "status", "--porcelain=v1", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]bool{}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.Trim(strings.TrimSpace(line[3:]), `"`)
		if arrow := strings.LastIndex(path, " -> "); arrow >= 0 {
			path = path[arrow+4:]
		}
		path = filepath.ToSlash(path)
		if path != "" && !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

func planChecks(root string, changed []string) ([]plannedCheck, []string) {
	var checks []plannedCheck
	var manifests []string
	sourceChanged := len(changed) == 0
	for _, path := range changed {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" && ext != ".rst" {
			sourceChanged = true
		}
	}
	packagePath := filepath.Join(root, "package.json")
	if data, err := os.ReadFile(packagePath); err == nil {
		manifests = append(manifests, "package.json")
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			manager := "npm"
			for _, lock := range []struct{ file, manager string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}} {
				if _, err := os.Stat(filepath.Join(root, lock.file)); err == nil {
					manager = lock.manager
					break
				}
			}
			addScript := func(kind, reason string, names ...string) {
				for _, name := range names {
					if _, ok := pkg.Scripts[name]; !ok {
						continue
					}
					command := manager + " run " + name
					checks = append(checks, plannedCheck{Label: name, Command: command, Reason: reason, RequiredCommand: manager, Kind: kind})
					return
				}
			}
			if sourceChanged {
				addScript("static", "Catch type and static-analysis regressions in the changed frontend or JavaScript/TypeScript code.", "typecheck", "check", "lint")
				addScript("test", "Exercise the project-defined automated tests affected by source changes.", "test:unit", "test")
				addScript("build", "Confirm the changed application still produces its configured production build.", "build")
				addScript("browser", "Exercise configured browser flows and collect screenshots, traces, console errors, and failed requests.", "test:e2e", "test:browser", "e2e", "playwright", "cypress")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		manifests = append(manifests, "go.mod")
		if sourceChanged {
			checks = append(checks,
				plannedCheck{Label: "go test", Command: "go test ./...", Reason: "Run package tests for the changed Go dependency graph.", RequiredCommand: "go", Kind: "test"},
				plannedCheck{Label: "go vet", Command: "go vet ./...", Reason: "Check changed Go code for compiler-assisted correctness issues.", RequiredCommand: "go", Kind: "static"})
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err == nil {
		manifests = append(manifests, "Cargo.toml")
		if sourceChanged {
			checks = append(checks,
				plannedCheck{Label: "cargo fmt", Command: "cargo fmt --check", Reason: "Verify formatting of changed Rust sources.", RequiredCommand: "cargo", Kind: "format"},
				plannedCheck{Label: "cargo test", Command: "cargo test", Reason: "Exercise the changed Rust crate graph.", RequiredCommand: "cargo", Kind: "test"})
		}
	}
	for _, name := range []string{"pyproject.toml", "pytest.ini", "tox.ini"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			manifests = append(manifests, name)
			if sourceChanged {
				checks = append(checks, plannedCheck{Label: "pytest", Command: "python3 -m pytest", Reason: "Run the configured Python test suite for changed Python code.", RequiredCommand: "python3", Kind: "test"})
			}
			break
		}
	}
	return checks, manifests
}

type browserArtifact struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type,omitempty"`
	Bytes    int64  `json:"bytes"`
}

type browserVerify struct{ shell *shellRun }

func (*browserVerify) Name() string { return "browser.verify" }
func (*browserVerify) Description() string {
	return "Run configured headless browser tests inside isolated compute and return results, console/request diagnostics, screenshots, videos, reports, and traces as reviewable task-workspace artifacts. Missing configuration or browser dependencies are reported as not_run."
}
func (*browserVerify) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"command":{"type":"string"},"directory":{"type":"string"},"framework":{"type":"string","enum":["playwright","cypress","other"]},"artifact_paths":{"type":"array","maxItems":8,"items":{"type":"string"}},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}}}`)
}
func (t *browserVerify) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct {
		Command string `json:"command"`
	}](args)
	command, _ := json.Marshal(map[string]string{"command": a.Command})
	risk, _ := t.shell.Assess(command)
	return risk, "Run configured browser verification"
}
func (t *browserVerify) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		Command        string   `json:"command"`
		Directory      string   `json:"directory"`
		Framework      string   `json:"framework"`
		ArtifactPaths  []string `json:"artifact_paths"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}](args)
	if err != nil {
		return "", err
	}
	scope := ScopeFrom(ctx)
	if scope == nil || scope.Root == "" {
		return "", ErrNoProject
	}
	result := struct {
		Status      string            `json:"status"`
		Framework   string            `json:"framework,omitempty"`
		Command     string            `json:"command,omitempty"`
		Directory   string            `json:"directory,omitempty"`
		DurationMS  int64             `json:"duration_ms,omitempty"`
		ExitCode    int               `json:"exit_code,omitempty"`
		Output      string            `json:"output,omitempty"`
		Diagnostics []string          `json:"diagnostics"`
		Artifacts   []browserArtifact `json:"artifacts"`
		Reason      string            `json:"reason,omitempty"`
	}{Status: "not_run", Framework: a.Framework, Command: a.Command, Directory: a.Directory,
		Diagnostics: []string{}, Artifacts: []browserArtifact{}}
	finish := func() (string, error) {
		b, _ := json.Marshal(result)
		SetRaw(ctx, string(b))
		return clip(string(b)), nil
	}
	if !scope.UseCompute {
		result.Reason = "browser verification requires isolated compute; host execution is not used as a fallback"
		return finish()
	}
	dir := scope.Root
	if a.Directory != "" {
		dir, err = scope.Resolve(a.Directory)
		if err != nil {
			return "", err
		}
	}
	framework, command := detectBrowserCheck(dir, a.Framework, a.Command)
	result.Framework, result.Command = framework, command
	if command == "" {
		result.Reason = "no configured Playwright or Cypress browser-test command was found"
		return finish()
	}
	if missing := missingBrowserDependency(dir, framework); missing != "" {
		result.Reason = missing
		return finish()
	}
	started := time.Now()
	callArgs, _ := json.Marshal(map[string]any{"command": command, "workspace": a.Directory, "timeout_seconds": a.TimeoutSeconds})
	output, runErr := t.shell.Call(ctx, callArgs)
	result.DurationMS, result.Output = time.Since(started).Milliseconds(), clipAt(output, 24<<10)
	if runErr != nil {
		result.Status, result.Reason = "blocked", runErr.Error()
		return finish()
	}
	result.ExitCode = shellExitCode(output)
	if missing := missingBrowserRuntime(output); missing != "" {
		result.Status, result.Reason = "not_run", missing
	} else if result.ExitCode == 0 {
		result.Status = "passed"
	} else {
		result.Status = "failed"
	}
	result.Diagnostics = browserDiagnostics(output)
	paths := append([]string{"playwright-report", "test-results", "cypress/screenshots", "cypress/videos"}, a.ArtifactPaths...)
	result.Artifacts = collectBrowserArtifacts(scope, dir, paths, started)
	return finish()
}

func missingBrowserRuntime(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "executable doesn't exist") && strings.Contains(lower, "playwright install"):
		return "Playwright is configured, but its compatible browser executable is unavailable; UMCode did not download it automatically"
	case strings.Contains(lower, "cypress executable not found"), strings.Contains(lower, "cypress failed to start") && strings.Contains(lower, "missing"):
		return "Cypress is configured, but its browser runtime is unavailable; UMCode did not download it automatically"
	}
	return ""
}

func detectBrowserCheck(dir, framework, command string) (string, string) {
	if command != "" {
		if framework == "" {
			lower := strings.ToLower(command)
			switch {
			case strings.Contains(lower, "playwright"):
				framework = "playwright"
			case strings.Contains(lower, "cypress"):
				framework = "cypress"
			default:
				framework = "other"
			}
		}
		return framework, command
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return framework, ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return framework, ""
	}
	for _, candidate := range []struct{ name, kind string }{{"test:e2e", "playwright"}, {"test:browser", "playwright"}, {"e2e", "playwright"}, {"playwright", "playwright"}, {"cypress", "cypress"}} {
		if script, ok := pkg.Scripts[candidate.name]; ok {
			kind := candidate.kind
			if strings.Contains(strings.ToLower(script), "cypress") {
				kind = "cypress"
			}
			return kind, "npm run " + candidate.name
		}
	}
	return framework, ""
}

func missingBrowserDependency(dir, framework string) string {
	if framework != "playwright" && framework != "cypress" {
		return ""
	}
	bin := filepath.Join(dir, "node_modules", ".bin", framework)
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return fmt.Sprintf("%s is configured but %s is unavailable; dependencies were not installed automatically", framework, filepath.ToSlash(filepath.Join("node_modules", ".bin", framework)))
	}
	return ""
}

func browserDiagnostics(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "console.error") || strings.Contains(lower, "pageerror") || strings.Contains(lower, "requestfailed") ||
			strings.Contains(lower, "failed request") || strings.Contains(lower, "uncaught") || strings.Contains(lower, "net::err_") {
			out = append(out, strings.TrimSpace(line))
			if len(out) == 50 {
				break
			}
		}
	}
	return out
}

func collectBrowserArtifacts(scope *Scope, dir string, paths []string, started time.Time) []browserArtifact {
	seen := map[string]bool{}
	var artifacts []browserArtifact
	for _, candidate := range paths {
		if len(artifacts) >= 100 || candidate == "" {
			break
		}
		abs := candidate
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, candidate)
		}
		if _, err := scope.Resolve(scope.Rel(abs)); err != nil {
			continue
		}
		_ = filepath.WalkDir(abs, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || len(artifacts) >= 100 || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().Before(started.Add(-2*time.Second)) {
				return nil
			}
			rel := scope.Rel(path)
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			kind, mime := browserArtifactType(path)
			artifacts = append(artifacts, browserArtifact{Path: rel, Kind: kind, MimeType: mime, Bytes: info.Size()})
			return nil
		})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts
}

func browserArtifactType(path string) (string, string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "screenshot", "image/png"
	case ".jpg", ".jpeg":
		return "screenshot", "image/jpeg"
	case ".webp":
		return "screenshot", "image/webp"
	case ".zip":
		return "trace", "application/zip"
	case ".webm":
		return "video", "video/webm"
	case ".json":
		return "result", "application/json"
	case ".html":
		return "report", "text/html"
	default:
		return "artifact", "application/octet-stream"
	}
}
