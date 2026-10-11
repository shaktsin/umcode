// Package sandbox confines commands the agent runs on the host: writes are
// limited to the project (plus temp and, optionally, package-manager caches),
// the network is denied unless the project allows it, and credential
// directories are unreadable. It uses Seatbelt on macOS and bubblewrap on
// Linux. Where neither is available Detect returns nil and callers keep their
// previous behaviour.
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Policy describes what a sandboxed command may do.
type Policy struct {
	// Readable, when non-nil, restricts data reads to explicit roots.
	Readable []string
	// Root is the project directory: the only workspace location that is writable.
	Root string
	// Network allows outbound network access.
	Network bool
	// Writable are extra writable directories (temp dirs, package caches).
	Writable []string
	// DenyRead are files or directories the command must not read.
	DenyRead []string
	// ReadOnlyInRoot are paths inside Root that stay read-only (git hooks).
	ReadOnlyInRoot []string
}

// Sandbox rewrites a prepared command so it runs confined.
type Sandbox interface {
	// Name identifies the mechanism ("seatbelt", "bwrap").
	Name() string
	// Wrap replaces cmd's program and arguments with a wrapper that runs the
	// original under p. cmd.Dir and cmd.Env are left as they are.
	Wrap(cmd *exec.Cmd, p Policy) error
}

// Detect returns the sandbox for this platform, or nil when none is usable.
func Detect() Sandbox {
	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("sandbox-exec"); err == nil {
			return seatbelt{exe: p}
		}
	case "linux":
		if p, err := exec.LookPath("bwrap"); err == nil && bwrapUsable(p) {
			return bwrap{exe: p}
		}
	}
	return nil
}

// cacheDirs are package-manager caches under the user's home. They are made
// writable only when the project allows the network, because that is when a
// command downloads dependencies.
var cacheDirs = []string{
	".npm", ".cache", ".pnpm-store", ".yarn", ".bun", ".cargo/registry", ".cargo/git",
	"go/pkg", ".gradle", ".m2", ".nuget", ".local/share/pnpm", "Library/Caches",
}

// credentialDirs are never readable from a sandboxed command: a command that
// could read them could copy them into the project, where the model reads them.
var credentialDirs = []string{
	".ssh", ".aws", ".gnupg", ".kube", ".docker", ".netrc", ".config/gcloud",
	".config/gh", ".azure", ".npmrc", ".pypirc",
}

// DefaultPolicy builds a Policy for a project at root. Paths that do not exist
// are left out; symlinks are resolved so the rules match real paths.
func DefaultPolicy(root string, network bool) Policy {
	p := Policy{Root: realPath(root), Network: network}
	add := func(list *[]string, path string) {
		if path = realPath(path); path != "" {
			*list = append(*list, path)
		}
	}
	for _, t := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		if isDir(t) {
			add(&p.Writable, t)
		}
	}
	p.Writable = dedupe(p.Writable)
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if network {
			for _, c := range cacheDirs {
				if d := filepath.Join(home, c); isDir(d) {
					add(&p.Writable, d)
				}
			}
			p.Writable = dedupe(p.Writable)
		}
		for _, c := range credentialDirs {
			if d := filepath.Join(home, c); exists(d) {
				p.DenyRead = append(p.DenyRead, realPathOrSelf(d))
			}
		}
	}
	for _, rel := range []string{".git/hooks", ".git/config"} {
		if d := filepath.Join(p.Root, rel); exists(d) {
			p.ReadOnlyInRoot = append(p.ReadOnlyInRoot, realPathOrSelf(d))
		}
	}
	return p
}

func realPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

func realPathOrSelf(p string) string {
	if r := realPath(p); r != "" {
		return r
	}
	return p
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// EvaluationPolicy confines evaluation commands to a disposable root plus OS
// runtime files. It never grants shared temporary or package-cache writes.
func EvaluationPolicy(root string, readonly []string) Policy {
	p := Policy{Root: realPath(root), Network: false, Readable: []string{realPath(root)}, ReadOnlyInRoot: nil}
	for _, path := range readonly {
		p.ReadOnlyInRoot = append(p.ReadOnlyInRoot, realPath(path))
	}
	for _, path := range []string{"/System/Library", "/System/Volumes/Preboot/Cryptexes/OS", "/private/preboot/Cryptexes/OS/System/Library", "/usr/bin", "/usr/sbin", "/usr/lib", "/usr/libexec", "/usr/share", "/bin", "/sbin", "/lib", "/lib64", "/dev", "/usr/local/go", "/opt/homebrew/Cellar", "/opt/homebrew/opt", "/opt/homebrew/bin", "/opt/homebrew/lib", "/Library/Developer/CommandLineTools"} {
		if exists(path) {
			p.Readable = append(p.Readable, realPath(path))
		}
	}
	return p
}
