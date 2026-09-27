// Package projects manages the folders the agent is allowed to work in. A
// project is the sandbox boundary: file and shell tools resolve every path
// against its root and refuse anything outside it.
package projects

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/compute"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// Service is the project registry.
type Service struct {
	st  *store.Store
	cfg *config.Config

	mu    sync.Mutex
	cache map[string]*instructionCache
}

// New returns a project service.
func New(st *store.Store, cfg *config.Config) *Service {
	return &Service{st: st, cfg: cfg, cache: map[string]*instructionCache{}}
}

// ErrNoProject is returned when a chat without a project tries to change files.
var ErrNoProject = errors.New("this chat is not attached to a project, so it cannot read or change files; open a project first")

// ExpandPath expands ~ and makes a path absolute and clean.
func ExpandPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path is empty")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// Create registers a folder as a project. The folder must exist; a project for
// the same folder (after resolving symlinks) is returned as an error.
func (s *Service) Create(ctx context.Context, p protocol.ProjectCreateParams) (protocol.Project, error) {
	if err := validateComputeTools(p.Tools); err != nil {
		return protocol.Project{}, err
	}
	root, err := ExpandPath(p.Root)
	if err != nil {
		return protocol.Project{}, err
	}
	// Store the resolved spelling, so a folder reached two ways (on macOS
	// /var and /private/var name the same place) is one project, not two.
	root = pathutil.Resolved(root)
	st, err := os.Stat(root)
	if err != nil {
		return protocol.Project{}, fmt.Errorf("%s cannot be opened: %w", root, err)
	}
	if !st.IsDir() {
		return protocol.Project{}, fmt.Errorf("%s is a file, not a folder", root)
	}
	if home, err := os.UserHomeDir(); err == nil && (pathutil.SameFolder(root, home) || root == "/") {
		return protocol.Project{}, fmt.Errorf("%s is too broad to be a project; pick the folder you actually work in", root)
	}
	if pathutil.Within(s.cfg.Home, root) {
		return protocol.Project{}, fmt.Errorf("%s is inside UMCode's own data folder", root)
	}
	// A row written before this normalisation may hold the other spelling.
	if existing, err := s.st.ListProjects(ctx, true); err == nil {
		for _, e := range existing {
			if pathutil.SameFolder(e.Root, root) {
				return e, fmt.Errorf("%s is already a project (%s)", root, e.Name)
			}
		}
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = filepath.Base(root)
	}
	rec, err := s.st.CreateProject(ctx, protocol.Project{
		Name: name, Root: root, Settings: p.Settings, Tools: p.Tools,
	})
	if errors.Is(err, store.ErrDuplicate) {
		existing, gerr := s.st.ProjectByRoot(ctx, root)
		if gerr == nil {
			return existing, fmt.Errorf("%s is already a project (%s)", root, existing.Name)
		}
	}
	if err != nil {
		return rec, err
	}
	ensureUmcodeGitignored(root)
	return s.decorate(rec), nil
}

// ensureUmcodeGitignored keeps this app's own bookkeeping for a project
// (Computer Use and Visual QA screenshots, under <root>/.umcode) out of the
// person's version control, so a screenshot taken while approving an action
// never ends up committed alongside their real files. Best-effort: a
// project that isn't a Git repo, or whose .gitignore can't be written, is
// left alone rather than failing the project creation over it.
func ensureUmcodeGitignored(root string) {
	if st, err := os.Stat(filepath.Join(root, ".git")); err != nil || !st.IsDir() {
		return
	}
	path := filepath.Join(root, ".gitignore")
	existing, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "/")) == ".umcode" {
			return
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	_, _ = f.WriteString(prefix + ".umcode/\n")
}

// Get returns one project with its live folder and git state.
func (s *Service) Get(ctx context.Context, id string) (protocol.Project, error) {
	p, err := s.st.GetProject(ctx, id)
	if err != nil {
		return p, err
	}
	return s.decorate(p), nil
}

// List returns the known projects, most recently opened first.
func (s *Service) List(ctx context.Context, includeArchived bool) ([]protocol.Project, error) {
	list, err := s.st.ListProjects(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = s.decorate(list[i])
	}
	return list, nil
}

// Open marks a project as the most recently used one.
func (s *Service) Open(ctx context.Context, id string) (protocol.Project, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return p, err
	}
	if err := s.st.TouchProject(ctx, id); err != nil {
		return p, err
	}
	return p, nil
}

// Update changes a project's name, settings, tools or archived flag.
func (s *Service) Update(ctx context.Context, p protocol.ProjectUpdateParams) (protocol.Project, error) {
	if p.Root != nil {
		root, err := s.validateRootChange(ctx, p.ProjectID, *p.Root)
		if err != nil {
			return protocol.Project{}, err
		}
		p.Root = &root
	}
	if p.Tools != nil {
		if err := validateComputeTools(*p.Tools); err != nil {
			return protocol.Project{}, err
		}
	}
	if p.InstructionsPath != nil && *p.InstructionsPath != "" {
		cur, err := s.st.GetProject(ctx, p.ProjectID)
		if err != nil {
			return cur, err
		}
		abs, err := Resolve(cur.Root, *p.InstructionsPath)
		if err != nil {
			return cur, fmt.Errorf("instructions file: %w", err)
		}
		rel := abs
		*p.InstructionsPath = rel
	}
	updated, err := s.st.UpdateProject(ctx, p)
	if err != nil {
		return updated, err
	}
	s.invalidate(updated.ID)
	return s.decorate(updated), nil
}

func (s *Service) validateRootChange(ctx context.Context, projectID, requested string) (string, error) {
	current, err := s.st.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	root, err := ExpandPath(requested)
	if err != nil {
		return "", err
	}
	root = pathutil.Resolved(root)
	if pathutil.SameFolder(current.Root, root) {
		return current.Root, nil
	}
	if current.Threads > 0 {
		return "", errors.New("this project's folder cannot be changed while it has chats; create a new project to preserve their isolated workspaces")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("project folder %s cannot be opened: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is a file, not a folder", root)
	}
	if home, err := os.UserHomeDir(); err == nil && (pathutil.SameFolder(root, home) || root == string(filepath.Separator)) {
		return "", fmt.Errorf("%s is too broad to be a project; choose the folder you actually work in", root)
	}
	if pathutil.Within(s.cfg.Home, root) {
		return "", fmt.Errorf("%s is inside UMCode's own data folder", root)
	}
	projects, err := s.st.ListProjects(ctx, true)
	if err != nil {
		return "", err
	}
	for _, other := range projects {
		if other.ID != projectID && pathutil.SameFolder(other.Root, root) {
			return "", fmt.Errorf("%s is already a project (%s)", root, other.Name)
		}
	}
	return root, nil
}

func validateComputeTools(t protocol.ProjectTools) error {
	if t.Compute != nil && *t.Compute {
		if err := compute.CheckHostAvailability(); err != nil {
			return err
		}
	}
	if t.ComputeVCPUs != nil && (*t.ComputeVCPUs < 1 || *t.ComputeVCPUs > 8) {
		return fmt.Errorf("compute vCPU limit must be between 1 and 8")
	}
	if t.ComputeMemoryMiB != nil && (*t.ComputeMemoryMiB < 512 || *t.ComputeMemoryMiB > 8192) {
		return fmt.Errorf("compute memory limit must be between 512 and 8192 MiB")
	}
	if t.ComputeDiskMiB != nil && (*t.ComputeDiskMiB < 1024 || *t.ComputeDiskMiB > 16384) {
		return fmt.Errorf("compute workspace limit must be between 1024 and 16384 MiB")
	}
	return nil
}

// Delete forgets a project. The folder is left alone.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.st.DeleteProject(ctx, id); err != nil {
		return err
	}
	s.invalidate(id)
	return nil
}

// decorate adds the state that is not in the database.
func (s *Service) decorate(p protocol.Project) protocol.Project {
	if st, err := os.Stat(p.Root); err != nil || !st.IsDir() {
		p.Missing = true
		return p
	}
	if v := gitInfo(p.Root); v != nil {
		p.VCS = v
	}
	return p
}

// Resolve joins rel to root and refuses anything that escapes the project,
// including through symlinks. It returns an absolute path.
func Resolve(root, rel string) (string, error) { return pathutil.Contain(root, rel) }

func within(root, target string) bool { return pathutil.Within(root, target) }

// Rel returns the slash-separated path of abs inside the project.
func Rel(root, abs string) string { return pathutil.Rel(root, abs) }
