package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed pytorch/requirements.txt
var pytorchRequirements string

const probeScript = `import importlib.util,importlib.metadata,json,sys
missing=[]
for module,dist,minimum,maximum in (("torch","torch",(2,6),3),("safetensors","safetensors",(0,6),1),("PIL","Pillow",(11,0),13),("numpy","numpy",(2,0),3)):
 try:
  if importlib.util.find_spec(module) is None: raise ImportError(module)
  version=importlib.metadata.version(dist).split(".")
  pair=(int(version[0]),int(version[1]))
  if pair<minimum or pair[0]>=maximum: missing.append(dist)
 except Exception: missing.append(dist)
print(json.dumps({"version":sys.version.split()[0],"missing":missing}))`

type PythonCandidate struct {
	Path    string   `json:"path"`
	Version string   `json:"version,omitempty"`
	Missing []string `json:"missing"`
	Ready   bool     `json:"ready"`
	Source  string   `json:"source"`
	Error   string   `json:"error,omitempty"`
}
type RuntimeState struct {
	Selected     string            `json:"selected"`
	Candidates   []PythonCandidate `json:"candidates"`
	Ready        bool              `json:"ready"`
	Missing      []string          `json:"missing"`
	Installing   bool              `json:"installing"`
	InstallError string            `json:"installError,omitempty"`
	ManagedPath  string            `json:"managedPath"`
}
type RuntimeService struct {
	home         string
	mu           sync.Mutex
	installing   bool
	installError string
}

func NewRuntimeService(home string) *RuntimeService { return &RuntimeService{home: home} }
func (s *RuntimeService) managedPath() string {
	return environmentPythonPath(filepath.Join(s.home, "runtimes", "pytorch"))
}
func environmentPythonPath(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python")
}
func (s *RuntimeService) settingPath() string {
	return filepath.Join(s.home, "runtimes", "python-selection.json")
}
func (s *RuntimeService) saved() string {
	b, err := os.ReadFile(s.settingPath())
	if err != nil {
		return ""
	}
	var v struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.Path
}
func pythonFile(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("enter the full path to a Python executable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("Python executable unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0111 == 0) {
		return errors.New("the selected path is not an executable Python file")
	}
	return nil
}
func probePython(ctx context.Context, path, source string) PythonCandidate {
	c := PythonCandidate{Path: path, Source: source, Missing: []string{}}
	if err := pythonFile(path); err != nil {
		c.Error = err.Error()
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-I", "-c", probeScript)
	cmd.Env = append(os.Environ(), "PYTHONNOUSERSITE=1")
	output, err := cmd.Output()
	if err != nil {
		c.Error = "Unable to inspect this Python environment"
		return c
	}
	var result struct {
		Version string   `json:"version"`
		Missing []string `json:"missing"`
	}
	if json.Unmarshal(output, &result) != nil || result.Version == "" {
		c.Error = "This executable did not return Python environment details"
		return c
	}
	c.Version = result.Version
	c.Missing = result.Missing
	c.Ready = len(c.Missing) == 0
	return c
}
func (s *RuntimeService) candidates(root string) []PythonCandidate {
	seen := map[string]bool{}
	out := []PythonCandidate{}
	add := func(path, source string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, PythonCandidate{Path: path, Source: source})
	}
	add(s.saved(), "Selected")
	add(os.Getenv("UMCODE_WORKBENCH_PYTHON"), "Configured")
	add(s.managedPath(), "UMCode managed")
	if root != "" {
		for _, dir := range []string{".venv", "venv"} {
			add(environmentPythonPath(filepath.Join(root, dir)), "Project")
		}
	}
	for _, key := range []string{"VIRTUAL_ENV", "CONDA_PREFIX"} {
		if dir := os.Getenv(key); dir != "" {
			add(environmentPythonPath(dir), "Active environment")
		}
	}
	if dir := os.Getenv("PYENV_ROOT"); dir != "" {
		matches, _ := filepath.Glob(filepath.Join(dir, "versions", "*", "bin", "python"))
		for i, path := range matches {
			if i >= 4 {
				break
			}
			add(path, "pyenv")
		}
	}
	for _, path := range []string{"/opt/homebrew/bin/python3", "/usr/local/bin/python3", "/usr/bin/python3"} {
		add(path, "System")
	}
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				add(abs, "System")
			}
		}
	}
	return out
}
func (s *RuntimeService) State(ctx context.Context, root string) RuntimeState {
	// Discovery is part of a UI request; keep one broken interpreter from
	// delaying the entire runtime picker for every candidate.
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	s.mu.Lock()
	installing, installError := s.installing, s.installError
	s.mu.Unlock()
	selected := s.saved()
	state := RuntimeState{Selected: selected, Candidates: []PythonCandidate{}, Missing: []string{}, Installing: installing, InstallError: installError, ManagedPath: s.managedPath()}
	for _, candidate := range s.candidates(root) {
		if ctx.Err() != nil {
			break
		}
		if len(state.Candidates) >= 10 {
			break
		}
		if _, err := os.Stat(candidate.Path); err != nil && candidate.Path != selected {
			continue
		}
		checked := probePython(ctx, candidate.Path, candidate.Source)
		state.Candidates = append(state.Candidates, checked)
		if candidate.Path == selected {
			state.Ready = checked.Ready
			state.Missing = checked.Missing
		}
	}
	if selected == "" {
		for _, candidate := range state.Candidates {
			if candidate.Ready && candidate.Source != "Project" {
				state.Selected = candidate.Path
				state.Ready = true
				break
			}
		}
	}
	return state
}
func (s *RuntimeService) Selected(ctx context.Context) string {
	state := s.State(ctx, "")
	if state.Ready {
		return state.Selected
	}
	return ""
}
func (s *RuntimeService) Select(ctx context.Context, path string) (RuntimeState, error) {
	path = strings.TrimSpace(path)
	if err := pythonFile(path); err != nil {
		return RuntimeState{}, err
	}
	candidate := probePython(ctx, path, "Selected")
	if candidate.Error != "" {
		return RuntimeState{}, errors.New(candidate.Error)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.installing {
		return RuntimeState{}, errors.New("wait for the managed runtime installation to finish")
	}
	b, err := json.Marshal(struct {
		Path string `json:"path"`
	}{path})
	if err != nil {
		return RuntimeState{}, err
	}
	if err = os.MkdirAll(filepath.Dir(s.settingPath()), 0700); err != nil {
		return RuntimeState{}, err
	}
	if err = os.WriteFile(s.settingPath(), b, 0600); err != nil {
		return RuntimeState{}, err
	}
	return RuntimeState{Selected: path, Candidates: []PythonCandidate{candidate}, Ready: candidate.Ready, Missing: candidate.Missing, ManagedPath: s.managedPath()}, nil
}
func (s *RuntimeService) Install(ctx context.Context, base string) error {
	if err := pythonFile(base); err != nil {
		return err
	}
	check := probePython(ctx, base, "Base Python")
	if check.Error != "" {
		return errors.New(check.Error)
	}
	s.mu.Lock()
	if s.installing {
		s.mu.Unlock()
		return errors.New("runtime installation is already running")
	}
	s.installing = true
	s.installError = ""
	s.mu.Unlock()
	go func() {
		installCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		err := s.installManaged(installCtx, base)
		s.mu.Lock()
		s.installing = false
		if err != nil {
			s.installError = err.Error()
		}
		s.mu.Unlock()
	}()
	return nil
}
func (s *RuntimeService) installManaged(ctx context.Context, base string) error {
	dir := filepath.Dir(filepath.Dir(s.managedPath()))
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(s.managedPath()); os.IsNotExist(err) {
		cmd := exec.CommandContext(ctx, base, "-I", "-m", "venv", dir)
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("create managed Python environment: %w", err)
		}
	}
	req, err := os.CreateTemp("", "umcode-pytorch-requirements-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(req.Name())
	if _, err = req.WriteString(pytorchRequirements); err != nil {
		req.Close()
		return err
	}
	if err = req.Close(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, s.managedPath(), "-I", "-m", "pip", "install", "-r", req.Name())
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("install PyTorch dependencies: %w", err)
	}
	checked := probePython(ctx, s.managedPath(), "UMCode managed")
	if !checked.Ready {
		return fmt.Errorf("managed environment is incomplete: %s", strings.Join(checked.Missing, ", "))
	}
	b, err := json.Marshal(struct {
		Path string `json:"path"`
	}{s.managedPath()})
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingPath(), b, 0600)
}
