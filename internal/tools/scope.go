package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shaktsin/umcode/internal/pathutil"
)

// Scope is the project a turn is working in. It travels on the context, so
// every tool — built-in, skill or MCP — sees the same boundary.
type Scope struct {
	ThreadID         string
	ProjectID        string
	ProjectName      string
	Root             string
	AllowShell       bool
	AllowNet         bool
	UseCompute       bool
	AllowVisualQA    bool
	AllowComputerUse bool
	// ApprovalMode is this project's auto-approve tier ("", "normal",
	// "auto_workspace", or "auto_all"); see policy.Gate.Check.
	ApprovalMode string
	// ComputerUseApps restricts Computer Use to these apps (case-insensitive
	// match against app_name, bundle_id, or app_path's basename). Empty means
	// unrestricted.
	ComputerUseApps  []string
	ComputeVCPUs     int
	ComputeMemoryMiB int
	ComputeDiskMiB   int
	// Progress streams command output while a tool is running.
	Progress func(output string)
	// Record is called after a successful write with the file's previous
	// content (nil when it did not exist) or deleted=true.
	Record func(ctx context.Context, abs string, before *string, deleted bool)
}

type scopeKey struct{}

// WithScope attaches a project scope to a context.
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom returns the scope of the running turn, or nil.
func ScopeFrom(ctx context.Context) *Scope {
	s, _ := ctx.Value(scopeKey{}).(*Scope)
	return s
}

// ErrNoProject is returned when a turn that changes files has no project.
var ErrNoProject = errors.New("this chat has no project, so files cannot be changed; open a project and try again")

// Resolve joins rel to the project root and refuses anything outside it,
// including paths that reach out through a symlink.
func (s *Scope) Resolve(rel string) (string, error) {
	abs, err := pathutil.Contain(s.Root, rel)
	if err != nil && s.ProjectName != "" && strings.Contains(err.Error(), "outside the project folder") {
		return "", fmt.Errorf("%s (project %s)", err, s.ProjectName)
	}
	return abs, err
}

// Rel is the project-relative, slash-separated form of abs.
func (s *Scope) Rel(abs string) string { return pathutil.Rel(s.Root, abs) }

// Snapshot reads a file so a write can be diffed and undone. It returns nil
// when the file does not exist yet.
func Snapshot(abs string) *string {
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return nil
	}
	if st.Size() > 1<<20 {
		big := ""
		return &big
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	s := string(data)
	return &s
}

// networkCommands are refused when a project has network access switched off.
var networkCommands = map[string]bool{
	"curl": true, "wget": true, "ssh": true, "scp": true, "sftp": true, "rsync": true,
	"nc": true, "netcat": true, "telnet": true, "ftp": true,
}

// NeedsNetwork reports whether a shell command obviously reaches the network,
// and which program does it. It is a guard rail, not a sandbox: it catches the
// common cases so a project with networking off behaves predictably.
func NeedsNetwork(command string) (string, bool) {
	fields := strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '|' || r == ';' || r == '&' || r == '(' || r == ')'
	})
	for i, f := range fields {
		name := filepath.Base(strings.Trim(f, "\"'`"))
		if networkCommands[name] {
			return name, true
		}
		// `git fetch|pull|push|clone|ls-remote` talk to remotes.
		if name == "git" && i+1 < len(fields) {
			switch fields[i+1] {
			case "fetch", "pull", "push", "clone", "ls-remote", "remote":
				return "git " + fields[i+1], true
			}
		}
		// Package managers that install from the network.
		switch name {
		case "pip", "pip3", "npm", "pnpm", "yarn", "brew", "go":
			if i+1 < len(fields) && (fields[i+1] == "install" || fields[i+1] == "add" || fields[i+1] == "get" || fields[i+1] == "ci") {
				return name + " " + fields[i+1], true
			}
		}
	}
	return "", false
}
