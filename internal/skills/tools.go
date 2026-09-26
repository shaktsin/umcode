package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/tools"
)

// Register adds skill.get_instructions and skill.run_script to the tool registry.
func (r *Registry) Register(reg *tools.Registry) {
	reg.Add(&getInstructions{r})
	reg.Add(&runScript{r})
}

type getInstructions struct{ r *Registry }

func (*getInstructions) Name() string { return "skill.get_instructions" }
func (*getInstructions) Description() string {
	return "Get the full instructions, scripts and input schemas for an installed skill."
}
func (*getInstructions) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"skill_name":{"type":"string","description":"Skill name from the skills list"}},"required":["skill_name"]}`)
}
func (*getInstructions) Assess(args json.RawMessage) (tools.Risk, string) {
	var a struct {
		SkillName string `json:"skill_name"`
	}
	_ = json.Unmarshal(args, &a)
	return tools.RiskGreen, "Read instructions for skill " + a.SkillName
}
func (t *getInstructions) Call(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	s, ok := t.r.Get(strings.TrimSpace(a.SkillName))
	if !ok {
		var names []string
		for _, x := range t.r.List() {
			if x.Error == "" {
				names = append(names, x.Name)
			}
		}
		return "", fmt.Errorf("skill %q not found; installed skills: %s", a.SkillName, strings.Join(names, ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s\nDirectory: %s\nRuntime: %s\n\n%s\n", s.Name, s.Dir, s.Runtime.Type, s.Body)
	if len(s.Scripts) > 0 {
		b.WriteString("\n## Scripts (run with skill.run_script)\n")
		names := make([]string, 0, len(s.Scripts))
		for n := range s.Scripts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			sc := s.Scripts[n]
			schema, _ := json.Marshal(sc.InputSchema)
			fmt.Fprintf(&b, "- %s: %s\n  input: %s\n", n, sc.Description, schema)
		}
	}
	return b.String(), nil
}

type runScript struct{ r *Registry }

type runArgs struct {
	Skill  string          `json:"skill"`
	Script string          `json:"script"`
	Args   json.RawMessage `json:"args"`
}

func (*runScript) Name() string { return "skill.run_script" }
func (*runScript) Description() string {
	return "Run a script declared by a skill. The script receives {\"input\": args, \"config\": skill config} as JSON on stdin; returns its output."
}
func (*runScript) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"skill":{"type":"string"},"script":{"type":"string","description":"Script name from skill.get_instructions"},"args":{"type":"object","description":"Arguments matching the script's input schema"}},"required":["skill","script"]}`)
}
func (t *runScript) Assess(args json.RawMessage) (tools.Risk, string) {
	var a runArgs
	_ = json.Unmarshal(args, &a)
	risk := tools.RiskYellow
	if s, ok := t.r.Get(a.Skill); ok {
		if r, ok := tools.ParseRisk(s.RiskLevel); ok {
			risk = r
		}
	}
	summary := fmt.Sprintf("Run %s/%s", a.Skill, a.Script)
	if len(a.Args) > 0 && string(a.Args) != "null" {
		summary += " " + string(a.Args)
	}
	return risk, summary
}
func (t *runScript) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a runArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if s, ok := t.r.Get(a.Skill); ok {
		if sc, ok := s.Scripts[a.Script]; ok {
			if err := checkRequired(sc.InputSchema, a.Args); err != nil {
				return "", err
			}
		}
	}
	return t.r.RunScript(ctx, a.Skill, a.Script, a.Args)
}

// checkRequired verifies required top-level properties are present.
func checkRequired(schema map[string]any, args json.RawMessage) error {
	req, _ := schema["required"].([]any)
	if len(req) == 0 {
		return nil
	}
	var m map[string]any
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &m); err != nil {
			return fmt.Errorf("args must be a JSON object: %w", err)
		}
	}
	var missing []string
	for _, r := range req {
		if k, ok := r.(string); ok {
			if _, present := m[k]; !present {
				missing = append(missing, k)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required argument(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// Install copies a skill from a local folder or clones it from a git URL into
// the install directory. It returns the installed skill.
func (r *Registry) Install(ctx context.Context, source, name string) (*Skill, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("source is empty")
	}
	tmp, err := os.MkdirTemp("", "umcode-skill-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	staged := filepath.Join(tmp, "skill")
	if isGitURL(source) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(cctx, "git", "clone", "--depth", "1", "--", source, staged).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("git clone: %v: %s", err, tail(string(out), 500))
		}
		os.RemoveAll(filepath.Join(staged, ".git"))
	} else {
		src := expandHome(source)
		if st, err := os.Stat(src); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("%s is not a folder or git URL", source)
		}
		if err := copyTree(src, staged); err != nil {
			return nil, err
		}
	}
	s, err := Parse(staged)
	if err != nil {
		return nil, fmt.Errorf("not a valid skill: %w", err)
	}
	if name == "" {
		name = s.Name
	}
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("invalid install name %q", name)
	}
	dest := filepath.Join(InstallDir(r.cfg), name)
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("a skill is already installed at %s; remove it first", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(staged, dest); err != nil {
		if err := copyTree(staged, dest); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.sig = "\x00stale" // force rescan
	r.mu.Unlock()
	installed, ok := r.Get(s.Name)
	if !ok {
		return Parse(dest)
	}
	return installed, nil
}

// Remove deletes an installed skill. Only skills inside the install directory can be removed.
func (r *Registry) Remove(name string) error {
	s, ok := r.Get(name)
	if !ok {
		return fmt.Errorf("skill %q is not installed", name)
	}
	root, _ := filepath.Abs(InstallDir(r.cfg))
	dir, _ := filepath.Abs(s.Dir)
	if filepath.Dir(dir) != root {
		return fmt.Errorf("skill %q lives in %s, outside %s; remove it there", name, s.Dir, root)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	r.mu.Lock()
	r.sig = "\x00stale"
	r.mu.Unlock()
	return nil
}

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "ssh://") || strings.HasSuffix(s, ".git")
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == ".venv" || strings.HasPrefix(rel, ".venv"+string(filepath.Separator)) ||
			rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) ||
			strings.Contains(rel, "__pycache__") || strings.Contains(rel, "node_modules") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode()&os.ModeSymlink != 0:
			return nil // skip symlinks for safety
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		}
	})
}
