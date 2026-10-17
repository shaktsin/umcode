// Package sandbox confines commands the agent runs on the host: writes are
// limited to the project (plus temp and, optionally, package-manager caches),
// reads are limited to the project and supporting runtime files,
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
	".npm", ".pnpm-store", ".yarn/cache", ".bun/install/cache", ".cargo/registry", ".cargo/git",
	"go/pkg", ".gradle/caches", ".gradle/wrapper/dists", ".m2/repository", ".nuget/packages", ".local/share/pnpm/store",
	".cache/go-build", ".cache/pip", ".cache/uv", ".cache/yarn", ".cache/pnpm", ".cache/ms-playwright",
	"Library/Caches/go-build", "Library/Caches/pip", "Library/Caches/uv", "Library/Caches/Yarn", "Library/Caches/pnpm", "Library/Caches/node-gyp", "Library/Caches/ms-playwright",
}

// These locations contain development runtimes, not arbitrary PATH entries or
// the user's entire application-data/cache directories.
var toolchainDirs = []string{
	".nvm/versions/node", ".fnm/node-versions", ".fnm/current", ".volta/bin", ".volta/tools",
	".local/share/mise/shims", ".local/share/mise/installs", ".asdf/shims", ".asdf/installs", ".asdf/bin", ".asdf/lib",
	".pyenv/bin", ".pyenv/shims", ".pyenv/versions", ".pyenv/libexec", ".pyenv/plugins", ".pyenv/version",
	".rustup/toolchains", ".rustup/settings.toml", ".cargo/bin", ".bun/bin",
}

// Git treats an existing but unreadable global config as fatal, even for
// project-local status/diff commands. Grant individual standard settings files,
// not the containing application configuration directory.
var developmentConfigFiles = []string{
	".gitconfig", ".config/git/config", ".config/git/ignore", ".config/git/attributes",
}

var commandRuntimePaths = []string{
	"/Library/Frameworks/Python.framework", "/opt/homebrew/Frameworks/Python.framework", "/usr/local/Frameworks/Python.framework",
	"/System/Library", "/System/Volumes/Preboot/Cryptexes/OS", "/private/preboot/Cryptexes/OS/System/Library",
	"/usr/bin", "/usr/sbin", "/usr/lib", "/usr/libexec", "/usr/share", "/bin", "/sbin", "/lib", "/lib64", "/dev",
	"/usr/local/bin", "/usr/local/lib", "/usr/local/share", "/usr/local/go", "/usr/local/Cellar", "/usr/local/opt",
	"/opt/homebrew/Cellar", "/opt/homebrew/opt", "/opt/homebrew/bin", "/opt/homebrew/lib", "/opt/homebrew/share",
	"/Library/Developer/CommandLineTools", "/Applications/Xcode.app/Contents/Developer",
	"/etc/gitconfig", "/usr/local/etc/gitconfig", "/opt/homebrew/etc/gitconfig",
	"/etc/ssl/certs", "/etc/ssl/cert.pem", "/etc/ssl/openssl.cnf", "/etc/pki/tls/certs", "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/passwd", "/etc/localtime",
}

// credentialDirs are never readable from a sandboxed command: a command that
// could read them could copy them into the project, where the model reads them.
var credentialDirs = []string{
	".ssh", ".aws", ".gnupg", ".kube", ".docker", ".netrc", ".config/gcloud",
	".config/gh", ".azure", ".npmrc", ".pypirc",
}

// DefaultPolicy builds a Policy for a project at root. Paths that do not exist
// are left out; symlinks are resolved so the rules match real paths.
// readonly contains trusted skill/runtime paths supplied by the application,
// never paths inferred from an arbitrary inherited PATH or shell command.
func DefaultPolicy(root string, network bool, readonly ...string) Policy {
	p := Policy{Root: realPath(root), Network: network, Readable: []string{realPath(root)}}
	add := func(list *[]string, path string) {
		if path = realPath(path); path != "" {
			*list = append(*list, path)
		}
	}
	read := func(path string) {
		if exists(path) {
			add(&p.Readable, path)
			// Linux distributions often symlink /bin and /lib into /usr.
			// Preserve the lexical mount location as well as its real target.
			if filepath.IsAbs(path) && filepath.Clean(path) != realPath(path) {
				p.Readable = append(p.Readable, filepath.Clean(path))
			}
		}
	}
	for _, path := range commandRuntimePaths {
		read(path)
	}
	for _, path := range readonly {
		read(path)
	}
	for _, t := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		if isDir(t) {
			add(&p.Writable, t)
			read(t)
		}
	}
	p.Writable = dedupe(p.Writable)
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		for _, c := range developmentConfigFiles {
			read(filepath.Join(home, c))
		}
		for _, c := range toolchainDirs {
			read(filepath.Join(home, c))
		}
		for _, c := range cacheDirs {
			if d := filepath.Join(home, c); isDir(d) {
				read(d)
				if network {
					add(&p.Writable, d)
				}
			}
		}
		for _, c := range credentialDirs {
			if d := filepath.Join(home, c); exists(d) {
				p.DenyRead = append(p.DenyRead, realPathOrSelf(d))
			}
		}
	}
	p.Writable = dedupe(p.Writable)
	p.Readable = dedupe(p.Readable)
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

// IsolatedChildPolicy retains trusted runtimes but grants writes only to the
// owned checkout and a fresh command-private temporary directory. Shared temp
// roots and package caches must not expose a sibling/parent checkout.
func IsolatedChildPolicy(root string, network bool, temporary string) Policy {
	p := DefaultPolicy(root, network)
	p.Writable = []string{realPath(temporary)}
	shared := map[string]bool{}
	for _, path := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		shared[realPath(path)] = true
	}
	reads := []string{realPath(root), realPath(temporary)}
	for _, path := range p.Readable {
		if !shared[realPath(path)] {
			reads = append(reads, path)
		}
	}
	p.Readable = dedupe(reads)
	return p
}
