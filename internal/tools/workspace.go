package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shaktsin/umcode/internal/config"
)

// Operation is a file-system operation checked against a workspace ACL.
type Operation string

const (
	OpRead   Operation = "read"
	OpWrite  Operation = "write"
	OpCreate Operation = "create"
	OpDelete Operation = "delete"
	OpShell  Operation = "shell"
)

// Workspace is a directory the agent may operate in.
type Workspace struct {
	Name                                         string
	Path                                         string
	Read, Write, CreateFiles, DeleteFiles, Shell bool
	Default                                      bool
}

// Workspaces is the set of configured workspaces.
type Workspaces struct {
	list []Workspace
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// NewWorkspaces builds workspaces from config. With none configured it uses
// ~/.umcode/workspace (created on demand) with deletes disabled.
func NewWorkspaces(cfg *config.Config) *Workspaces {
	w := &Workspaces{}
	for _, c := range cfg.Tools.Workspaces {
		if c.Path == "" {
			continue
		}
		w.list = append(w.list, Workspace{
			Name: c.Name, Path: filepath.Clean(c.Path), Default: c.Default,
			Read: boolOr(c.ACL.Read, true), Write: boolOr(c.ACL.Write, true),
			CreateFiles: boolOr(c.ACL.CreateFiles, true), DeleteFiles: boolOr(c.ACL.DeleteFiles, false),
			Shell: boolOr(c.ACL.Shell, true),
		})
	}
	if len(w.list) == 0 {
		w.list = []Workspace{{Name: "default", Path: filepath.Join(cfg.Home, "workspace"), Default: true,
			Read: true, Write: true, CreateFiles: true, Shell: true}}
	}
	hasDefault := false
	for _, ws := range w.list {
		hasDefault = hasDefault || ws.Default
	}
	if !hasDefault {
		w.list[0].Default = true
	}
	return w
}

// List returns all workspaces.
func (w *Workspaces) List() []Workspace { return w.list }

// Get returns a workspace by name, or the default for "".
func (w *Workspaces) Get(name string) (Workspace, error) {
	for _, ws := range w.list {
		if (name == "" && ws.Default) || (name != "" && ws.Name == name) {
			return ws, nil
		}
	}
	return Workspace{}, fmt.Errorf("unknown workspace %q", name)
}

// Resolve returns the absolute path for p inside ws after checking containment
// and the ACL for op. Relative paths are relative to the workspace root.
func (ws Workspace) Resolve(p string, op Operation) (string, error) {
	if err := os.MkdirAll(ws.Path, 0o755); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(ws.Path)
	if err != nil {
		return "", err
	}
	if p == "" {
		p = "."
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	// Resolve symlinks on the longest existing prefix so links cannot escape.
	real, err := evalExisting(p)
	if err != nil {
		return "", err
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s is outside workspace %q (%s)", p, ws.Name, ws.Path)
	}
	allowed := map[Operation]bool{OpRead: ws.Read, OpWrite: ws.Write, OpCreate: ws.CreateFiles,
		OpDelete: ws.DeleteFiles, OpShell: ws.Shell}[op]
	if !allowed {
		return "", fmt.Errorf("workspace %q does not allow %s", ws.Name, op)
	}
	return real, nil
}

func evalExisting(p string) (string, error) {
	var rest []string
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}
