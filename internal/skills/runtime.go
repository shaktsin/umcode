package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/procutil"
)

// runtimes prepares per-skill environments; venvs are created lazily on first use.
type runtimes struct {
	cfg   *config.Config
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newRuntimes(cfg *config.Config) *runtimes {
	return &runtimes{cfg: cfg, locks: map[string]*sync.Mutex{}}
}

func (rt *runtimes) lock(dir string) *sync.Mutex {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	l, ok := rt.locks[dir]
	if !ok {
		l = &sync.Mutex{}
		rt.locks[dir] = l
	}
	return l
}

// Env is a resolved skill environment.
type Env struct {
	Vars      []string // KEY=VALUE, passed to subprocesses
	PythonBin string
	NodeBin   string // directory containing node ("" = PATH)
	Timeout   time.Duration
}

// baseEnv is what every skill process starts from: no API keys or tokens from
// the engine's own environment.
func baseEnv() map[string]string {
	env := map[string]string{}
	for _, k := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "SHELL", "TERM"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if env["PATH"] == "" {
		env["PATH"] = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	// Homebrew locations are not on launchd's default PATH.
	env["PATH"] = env["PATH"] + ":/opt/homebrew/bin:/usr/local/bin"
	return env
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = expandHome(p)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Resolve builds a skill's environment, provisioning its venv when the skill
// declares requirements (precedence: skills.<name> > skills.defaults > SKILL.md).
func (rt *runtimes) Resolve(ctx context.Context, s *Skill) (Env, error) {
	defaults := rt.cfg.Skills["defaults"]
	per := rt.cfg.Skills[s.Name]
	env := baseEnv()

	python := firstExisting(per.PythonBin, defaults.PythonBin, s.Runtime.PythonBin)
	if python == "" {
		if p, err := exec.LookPath("python3"); err == nil {
			python = p
		} else {
			python = "python3"
		}
	}
	nodeDir := ""
	for _, d := range []string{per.NodeBin, defaults.NodeBin, s.Runtime.NodeBin} {
		if d = expandHome(d); d != "" {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				nodeDir = d
				break
			}
		}
	}

	if s.Runtime.Requirements != "" {
		req := filepath.Join(s.Dir, filepath.Clean(s.Runtime.Requirements))
		if _, err := os.Stat(req); err == nil {
			venvPython, err := rt.ensureVenv(ctx, s.Dir, python, req)
			if err != nil {
				return Env{}, fmt.Errorf("skill %s: preparing Python environment: %w", s.Name, err)
			}
			python = venvPython
			env["VIRTUAL_ENV"] = filepath.Join(s.Dir, ".venv")
		}
	}

	var prepend []string
	add := func(p string) {
		p = expandHome(p)
		for _, x := range prepend {
			if x == p {
				return
			}
		}
		if p != "" {
			prepend = append(prepend, p)
		}
	}
	for _, p := range per.ExtraPath {
		add(p)
	}
	for _, p := range defaults.ExtraPath {
		add(p)
	}
	for _, p := range s.Runtime.ExtraPath {
		add(p)
	}
	add(nodeDir)
	if filepath.IsAbs(python) {
		add(filepath.Dir(python))
	}
	if len(prepend) > 0 {
		env["PATH"] = strings.Join(prepend, ":") + ":" + env["PATH"]
	}
	for _, m := range []map[string]string{defaults.Env, s.Runtime.Env, per.Env} {
		for k, v := range m {
			env[k] = v
		}
	}
	env["SKILL_DIR"] = s.Dir

	out := Env{PythonBin: python, NodeBin: nodeDir, Timeout: time.Duration(s.Runtime.TimeoutSeconds) * time.Second}
	for k, v := range env {
		out.Vars = append(out.Vars, k+"="+v)
	}
	return out, nil
}

// ensureVenv creates or updates <skill>/.venv. It reuses venvs made by the
// Python app (same .req_hash format). Uses uv when available.
func (rt *runtimes) ensureVenv(ctx context.Context, dir, python, req string) (string, error) {
	l := rt.lock(dir)
	l.Lock()
	defer l.Unlock()
	venv := filepath.Join(dir, ".venv")
	venvPython := filepath.Join(venv, "bin", "python")
	data, err := os.ReadFile(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	hashFile := filepath.Join(venv, ".req_hash")
	if old, err := os.ReadFile(hashFile); err == nil && strings.TrimSpace(string(old)) == hash {
		if _, err := os.Stat(venvPython); err == nil {
			return venvPython, nil
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(cctx, name, args...)
		cmd.Dir = dir
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, tail(out.String(), 800))
		}
		return nil
	}
	if uv, err := exec.LookPath("uv"); err == nil {
		if err := run(uv, "venv", "--python", python, venv); err != nil {
			return "", err
		}
		if err := run(uv, "pip", "install", "--python", venvPython, "-r", req); err != nil {
			return "", err
		}
	} else {
		if err := run(python, "-m", "venv", venv); err != nil {
			return "", err
		}
		if err := run(venvPython, "-m", "pip", "install", "--quiet", "-r", req); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(hashFile, []byte(hash), 0o644); err != nil {
		return "", err
	}
	return venvPython, nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// ErrUnknownScript is returned for a script the skill does not declare.
var ErrUnknownScript = errors.New("unknown script")

// RunScript runs one declared script. Its stdin is the skill-template envelope
// {"input": <args>, "config": <skills.<name>.config from config.yaml>}.
func (r *Registry) RunScript(ctx context.Context, skill, script string, argsJSON []byte) (string, error) {
	s, ok := r.GetContext(ctx, skill)
	if !ok {
		return "", fmt.Errorf("skill %q is not installed", skill)
	}
	sc, ok := s.Scripts[script]
	if !ok {
		names := make([]string, 0, len(s.Scripts))
		for n := range s.Scripts {
			names = append(names, n)
		}
		return "", fmt.Errorf("%w %q for skill %s (available: %s)", ErrUnknownScript, script, skill, strings.Join(names, ", "))
	}
	env, err := r.rt.Resolve(ctx, s)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.Dir, filepath.Clean(sc.Path))
	var name string
	var args []string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		name, args = env.PythonBin, []string{path}
	case ".js", ".mjs", ".cjs":
		name = "node"
		if env.NodeBin != "" {
			name = filepath.Join(env.NodeBin, "node")
		}
		args = []string{path}
	case ".sh", ".bash":
		name, args = "/bin/bash", []string{path}
	default:
		name = path
	}
	cctx, cancel := context.WithTimeout(ctx, env.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = s.Dir
	procutil.Prepare(cmd)
	cmd.Env = env.Vars
	if len(argsJSON) == 0 || string(argsJSON) == "null" {
		argsJSON = []byte("{}")
	}
	cfgMap := r.cfg.Skills[s.Name].Config
	if snapshot := snapshotFromContext(ctx); snapshot != nil {
		if pluginConfig := snapshot.Config(s.Name); pluginConfig != nil {
			cfgMap = pluginConfig
		}
	}
	if cfgMap == nil {
		cfgMap = map[string]any{}
	}
	stdin, err := json.Marshal(map[string]any{"input": json.RawMessage(argsJSON), "config": cfgMap})
	if err != nil {
		return "", fmt.Errorf("args must be valid JSON: %w", err)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	redact := func(value string) string {
		if snapshot := snapshotFromContext(ctx); snapshot != nil {
			return snapshot.redact(s.Name, value)
		}
		return value
	}
	if cctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("script timed out after %s", env.Timeout)
	}
	out := stdout.String()
	if err != nil {
		return "", fmt.Errorf("script failed (%v): %s", err, redact(tail(stderr.String()+"\n"+out, 4000)))
	}
	if stderr.Len() > 0 {
		out += "\n--- stderr ---\n" + tail(stderr.String(), 4000)
	}
	return redact(out), nil
}

// EnvFor returns a skill's environment for shell.run.
func (r *Registry) EnvFor(ctx context.Context, skill string) ([]string, error) {
	s, ok := r.GetContext(ctx, skill)
	if !ok {
		return nil, fmt.Errorf("skill %q is not installed", skill)
	}
	if snapshot := snapshotFromContext(ctx); snapshot != nil && len(snapshot.secrets[s.Name]) > 0 {
		return nil, fmt.Errorf("plugin skill %q uses protected secrets; run its declared scripts with skill.run_script", skill)
	}
	env, err := r.rt.Resolve(ctx, s)
	if err != nil {
		return nil, err
	}
	return env.Vars, nil
}
