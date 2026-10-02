// Package skills loads Agent Skills (folders with a SKILL.md) from the skill
// directories, prepares their runtimes, and exposes them to the agent through
// the skill.get_instructions and skill.run_script tools.
//
// SKILL.md format (same as the Python app):
//
//	---
//	name: weather-fetch                # lowercase, digits, hyphens
//	description: What it does and when to use it
//	risk_level: green                  # optional: green | yellow | red (default yellow)
//	runtime:
//	  type: python                     # python | node | shell
//	  requirements: requirements.txt   # installed into <skill>/.venv
//	  timeout_seconds: 30
//	  env: {KEY: value}
//	  extra_path: [/opt/tools/bin]
//	scripts:
//	  fetch:
//	    path: scripts/fetch.py
//	    description: Fetch the forecast
//	    input_schema: {type: object, properties: {...}}
//	---
//	Markdown instructions…
package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"

	"github.com/shaktsin/umcode/internal/config"
)

var nameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// Runtime is the runtime: block of SKILL.md.
type Runtime struct {
	Type           string            `yaml:"type" json:"type"`
	Requirements   string            `yaml:"requirements" json:"requirements,omitempty"`
	NodeBin        string            `yaml:"node_bin" json:"nodeBin,omitempty"`
	PythonBin      string            `yaml:"python_bin" json:"pythonBin,omitempty"`
	TimeoutSeconds int               `yaml:"timeout_seconds" json:"timeoutSeconds"`
	Env            map[string]string `yaml:"env" json:"-"`
	ExtraPath      []string          `yaml:"extra_path" json:"extraPath,omitempty"`
}

// Script is one runnable entry under scripts:.
type Script struct {
	Path        string         `yaml:"path" json:"path"`
	Description string         `yaml:"description" json:"description"`
	InputSchema map[string]any `yaml:"input_schema" json:"inputSchema,omitempty"`
}

// Skill is a loaded skill.
type Skill struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	License     string            `json:"license,omitempty"`
	Version     string            `json:"version,omitempty"`
	RiskLevel   string            `json:"riskLevel"`
	Runtime     Runtime           `json:"runtime"`
	Scripts     map[string]Script `json:"scripts,omitempty"`
	Body        string            `json:"-"`
	Dir         string            `json:"dir"`
	Error       string            `json:"error,omitempty"` // set for invalid skills (listed, not usable)
}

type frontmatter struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	License     string            `yaml:"license"`
	Version     any               `yaml:"version"`
	RiskLevel   string            `yaml:"risk_level"`
	Runtime     Runtime           `yaml:"runtime"`
	Scripts     map[string]Script `yaml:"scripts"`
}

// Parse reads dir/SKILL.md.
func Parse(dir string) (*Skill, error) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, errors.New("SKILL.md must start with a --- frontmatter block")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, errors.New("SKILL.md frontmatter is not closed with ---")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		return nil, fmt.Errorf("SKILL.md frontmatter: %w", err)
	}
	s := &Skill{
		Name: strings.TrimSpace(fm.Name), Description: strings.TrimSpace(fm.Description),
		License: fm.License, RiskLevel: strings.ToLower(strings.TrimSpace(fm.RiskLevel)),
		Runtime: fm.Runtime, Scripts: fm.Scripts, Dir: dir,
		Body: strings.TrimSpace(strings.Join(lines[end+1:], "\n")),
	}
	if fm.Version != nil {
		s.Version = fmt.Sprint(fm.Version)
	}
	switch {
	case s.Name == "":
		return nil, errors.New("missing required field: name")
	case len(s.Name) > 64 || !nameRE.MatchString(s.Name) || strings.Contains(s.Name, "--"):
		return nil, fmt.Errorf("invalid name %q: use lowercase letters, digits and single hyphens", s.Name)
	case s.Description == "":
		return nil, errors.New("missing required field: description")
	case len(s.Description) > 1024:
		return nil, errors.New("description exceeds 1024 characters")
	}
	if s.RiskLevel == "" {
		s.RiskLevel = "yellow"
	}
	s.Runtime.Type = strings.ToLower(s.Runtime.Type)
	if s.Runtime.Type == "" {
		s.Runtime.Type = "shell"
	}
	if s.Runtime.TimeoutSeconds <= 0 {
		s.Runtime.TimeoutSeconds = 30
	}
	for name, sc := range s.Scripts {
		clean := filepath.Clean(sc.Path)
		if sc.Path == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return nil, fmt.Errorf("script %q: path must be relative to the skill folder", name)
		}
	}
	return s, nil
}

// Registry discovers skills in a set of directories.
type Registry struct {
	cfg  *config.Config
	mu   sync.RWMutex
	byID map[string]*Skill
	bad  []*Skill
	sig  string
	rt   *runtimes
}

// Root contributes one plugin's skill directories and non-secret settings.
type Root struct {
	PluginID      string
	Paths         []string
	Config        map[string]any
	RuntimeValues map[string]string
	SecretValues  []string
}

// Snapshot is an immutable set of namespaced plugin skills for one project
// capability generation.
type Snapshot struct {
	byID    map[string]*Skill
	config  map[string]map[string]any
	secrets map[string][]string
}

type snapshotContextKey struct{}

func CompileSnapshot(roots []Root) (*Snapshot, []error) {
	snapshot := &Snapshot{byID: map[string]*Skill{}, config: map[string]map[string]any{}, secrets: map[string][]string{}}
	var errs []error
	for _, root := range roots {
		for _, skillPath := range root.Paths {
			skill, err := Parse(skillPath)
			if err != nil {
				errs = append(errs, fmt.Errorf("plugin %s skill %s: %w", root.PluginID, skillPath, err))
				continue
			}
			name := root.PluginID + ":" + skill.Name
			if _, exists := snapshot.byID[name]; exists {
				errs = append(errs, fmt.Errorf("duplicate plugin skill %s", name))
				continue
			}
			copy := *skill
			copy.Name = name
			copy.Runtime.Env = cloneStringMap(skill.Runtime.Env)
			for key, value := range copy.Runtime.Env {
				for token, replacement := range root.RuntimeValues {
					value = strings.ReplaceAll(value, token, replacement)
				}
				if strings.Contains(value, "{{secret:") || strings.Contains(value, "{{setting:") {
					errs = append(errs, fmt.Errorf("plugin %s skill %s runtime references an undeclared or unset plugin setting", root.PluginID, skillPath))
					continue
				}
				copy.Runtime.Env[key] = value
			}
			snapshot.byID[name] = &copy
			snapshot.secrets[name] = append([]string(nil), root.SecretValues...)
			if root.Config != nil {
				snapshot.config[name] = cloneAnyMap(root.Config)
			}
		}
	}
	return snapshot, errs
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func WithSnapshot(ctx context.Context, snapshot *Snapshot) context.Context {
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, snapshotContextKey{}, snapshot)
}

func snapshotFromContext(ctx context.Context) *Snapshot {
	if ctx == nil {
		return nil
	}
	snapshot, _ := ctx.Value(snapshotContextKey{}).(*Snapshot)
	return snapshot
}

func (s *Snapshot) Config(name string) map[string]any {
	if s == nil {
		return nil
	}
	return cloneAnyMap(s.config[name])
}

func (s *Snapshot) redact(name, value string) string {
	if s == nil {
		return value
	}
	for _, secret := range s.secrets[name] {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// NewRegistry builds a registry for the configured skill directories.
func NewRegistry(cfg *config.Config) *Registry {
	return &Registry{cfg: cfg, byID: map[string]*Skill{}, rt: newRuntimes(cfg)}
}

// Config returns the engine config the registry was built with.
func (r *Registry) Config() *config.Config { return r.cfg }

// Dirs returns the directories scanned, in priority order (earlier wins on name clash).
func (r *Registry) Dirs() []string {
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "skills"))
	}
	dirs = append(dirs, InstallDir(r.cfg))
	dirs = append(dirs, r.cfg.SkillDirs...)
	return dirs
}

// InstallDir is where `skill install` puts skills.
func InstallDir(cfg *config.Config) string { return filepath.Join(cfg.Home, "skills") }

// Refresh rescans the directories if any SKILL.md changed.
func (r *Registry) Refresh() {
	type entry struct{ dir, sig string }
	var entries []entry
	var sig strings.Builder
	for _, d := range r.Dirs() {
		children, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, c := range children {
			if !c.IsDir() && c.Type()&os.ModeSymlink == 0 {
				continue
			}
			p := filepath.Join(d, c.Name())
			st, err := os.Stat(filepath.Join(p, "SKILL.md"))
			if err != nil {
				continue
			}
			e := entry{p, fmt.Sprintf("%s|%d|%d;", p, st.ModTime().UnixNano(), st.Size())}
			entries = append(entries, e)
			sig.WriteString(e.sig)
		}
	}
	r.mu.RLock()
	same := sig.String() == r.sig
	r.mu.RUnlock()
	if same {
		return
	}
	byID := map[string]*Skill{}
	var bad []*Skill
	for _, e := range entries {
		s, err := Parse(e.dir)
		if err != nil {
			bad = append(bad, &Skill{Name: filepath.Base(e.dir), Dir: e.dir, Error: err.Error()})
			continue
		}
		if _, dup := byID[s.Name]; dup {
			continue // earlier directory wins
		}
		byID[s.Name] = s
	}
	r.mu.Lock()
	r.byID, r.bad, r.sig = byID, bad, sig.String()
	r.mu.Unlock()
}

// List returns valid skills sorted by name, followed by invalid ones.
func (r *Registry) List() []*Skill {
	r.Refresh()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, 0, len(r.byID)+len(r.bad))
	for _, s := range r.byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return append(out, r.bad...)
}

// ListContext returns legacy skills plus the context's namespaced plugin skills.
func (r *Registry) ListContext(ctx context.Context) []*Skill {
	out := r.List()
	if snapshot := snapshotFromContext(ctx); snapshot != nil {
		for _, skill := range snapshot.byID {
			out = append(out, skill)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	}
	return out
}

// Get returns a valid skill by name.
func (r *Registry) Get(name string) (*Skill, bool) {
	r.Refresh()
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[name]
	return s, ok
}

func (r *Registry) GetContext(ctx context.Context, name string) (*Skill, bool) {
	if snapshot := snapshotFromContext(ctx); snapshot != nil {
		if skill, ok := snapshot.byID[name]; ok {
			return skill, true
		}
	}
	return r.Get(name)
}

// Catalog is the short skills list placed in the system prompt.
func (r *Registry) Catalog() string {
	return catalog(r.List())
}

func (r *Registry) CatalogContext(ctx context.Context) string {
	return catalog(r.ListContext(ctx))
}

func catalog(skills []*Skill) string {
	var b strings.Builder
	for _, s := range skills {
		if s.Error != "" {
			continue
		}
		desc := s.Description
		if len(desc) > 200 {
			desc = desc[:197] + "..."
		}
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, desc)
	}
	if b.Len() == 0 {
		return ""
	}
	return "# Skills\nThese skills are installed. Before using one, call skill.get_instructions to read its full instructions; run its scripts with skill.run_script, or pass skill=<name> to shell.run to use its environment.\n" + b.String()
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the is it in on to of or and for by as at be do if so no not but are was has had any all its can may use this that with from when what how you your will such also into than them then they been have each make like does used using should`) {
		stopwords[w] = true
	}
}

var tokenRE = regexp.MustCompile(`[a-z0-9]+`)

// Match returns the skill whose description shares the most keywords with
// text (at least one), like the Python app's trigger matching.
func (r *Registry) Match(text string) (*Skill, bool) {
	return matchSkills(r.List(), text)
}

func (r *Registry) MatchContext(ctx context.Context, text string) (*Skill, bool) {
	return matchSkills(r.ListContext(ctx), text)
}

func matchSkills(skills []*Skill, text string) (*Skill, bool) {
	words := map[string]bool{}
	for _, w := range tokenRE.FindAllString(strings.ToLower(text), -1) {
		words[w] = true
	}
	var best *Skill
	bestScore := 0
	for _, s := range skills {
		if s.Error != "" {
			continue
		}
		score := 0
		seen := map[string]bool{}
		for _, w := range tokenRE.FindAllString(strings.ToLower(s.Description+" "+s.Name), -1) {
			if len(w) >= 3 && !stopwords[w] && !seen[w] && words[w] {
				score++
				seen[w] = true
			}
		}
		if score > bestScore {
			best, bestScore = s, score
		}
	}
	return best, best != nil
}
