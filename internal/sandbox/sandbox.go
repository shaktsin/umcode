// Package sandbox confines commands the agent runs on the host: writes are
// limited to the project (plus temp and, optionally, package-manager caches),
// reads are limited to the project and supporting runtime files,
// the network is denied unless the project allows it, and credential
// directories are unreadable. It uses Seatbelt on macOS and bubblewrap on
// Linux. Where neither is available Detect returns nil and callers keep their
// previous behaviour.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	// RootReadOnly mounts/allows Root read-only. Writable entries stay
	// writable; nothing under Root can be created, changed or removed.
	RootReadOnly bool
	// ReadOnlyPaths are read-only locations that may lie outside Root (a git
	// worktree's gitdir and its common dir). They are mounted/denied after the
	// writable locations; callers also list them in Readable.
	ReadOnlyPaths []string
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
	out := make([]string, 0, len(in))
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

// protectGit makes the repository metadata of an agent checkout read-only so
// an agent cannot commit, rewrite config/hooks or delete refs. Agent policies
// only; the lead keeps the narrower .git/hooks and .git/config rules.
//   - .git directory: the whole directory.
//   - .git file (linked worktree): the pointer file, plus the gitdir it names
//     and that gitdir's commondir (the main repository's .git), which live
//     outside root; those are also made readable so git log/diff keep working.
//   - no .git: <root>/.git is still listed. Seatbelt denies it even though it
//     does not exist (no `git init`); bwrap skips missing paths, so there an
//     agent could still run `git init` in a root that has no .git at all.
func protectGit(p *Policy) {
	dotgit := filepath.Join(p.Root, ".git")
	p.ReadOnlyInRoot = []string{dotgit}
	st, err := os.Lstat(dotgit)
	if err != nil {
		return
	}
	if st.Mode()&os.ModeSymlink != 0 {
		// A symlinked .git: keep the lexical path and also protect a real git
		// directory it resolves to. A target that is not a git directory is
		// never mounted or made readable.
		if real := realPath(dotgit); isDir(real) && looksLikeGitDir(real) {
			p.ReadOnlyInRoot = append(p.ReadOnlyInRoot, real)
		}
		return
	}
	if st.IsDir() {
		p.ReadOnlyInRoot = []string{realPathOrSelf(dotgit)}
		return
	}
	// The pointer is written by the agent's own checkout, so it is untrusted:
	// nothing it names is trusted unless the named directory points back.
	data, err := os.ReadFile(dotgit)
	if err != nil {
		return
	}
	ptr, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return
	}
	gitdir := realPath(resolveFrom(p.Root, strings.TrimSpace(ptr)))
	// A linked worktree's admin dir has HEAD but no objects/; its proof is the
	// back-reference below.
	if !isDir(gitdir) || !exists(filepath.Join(gitdir, "HEAD")) {
		return
	}
	back, err := os.ReadFile(filepath.Join(gitdir, "gitdir"))
	if err != nil || realPath(resolveFrom(gitdir, strings.TrimSpace(string(back)))) != realPath(dotgit) {
		return
	}
	dirs := []string{gitdir}
	if cd, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		if common := realPath(resolveFrom(gitdir, strings.TrimSpace(string(cd)))); isDir(common) && looksLikeGitDir(common) {
			dirs = append(dirs, common)
		}
	}
	p.ReadOnlyPaths = append(p.ReadOnlyPaths, dirs...)
	p.Readable = dedupe(append(p.Readable, dirs...))
}

// resolveFrom makes path absolute against base.
func resolveFrom(base, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return path
}

// looksLikeGitDir reports whether dir has the files every git directory has.
func looksLikeGitDir(dir string) bool {
	return exists(filepath.Join(dir, "HEAD")) && isDir(filepath.Join(dir, "objects"))
}

// ReadOnlyChildPolicy is IsolatedChildPolicy with the whole project root
// read-only (including all of .git). Only the command-private temporary
// directory is writable.
func ReadOnlyChildPolicy(root string, network bool, temporary string) Policy {
	p := IsolatedChildPolicy(root, network, temporary)
	p.RootReadOnly = true
	protectGit(&p)
	return p
}

// AgentWritePolicy is IsolatedChildPolicy for a write-capable agent: the
// checkout is writable except for the whole .git directory.
func AgentWritePolicy(root string, network bool, temporary string) Policy {
	p := IsolatedChildPolicy(root, network, temporary)
	protectGit(&p)
	return p
}

// SelfCheckReadOnly proves that sb really enforces RootReadOnly on this host:
// a file in a scratch root can be read, but creating one fails and leaves the
// root unchanged. It returns an error when sb is nil.
func SelfCheckReadOnly(sb Sandbox, tmp string) error {
	if sb == nil {
		return errors.New("sandbox: no sandbox available for the read-only self-check")
	}
	root, err := os.MkdirTemp(tmp, "umcode-ro-root-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	scratch, err := os.MkdirTemp(tmp, "umcode-ro-tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("ok"), 0o644); err != nil {
		return err
	}
	p := ReadOnlyChildPolicy(root, false, scratch)
	run := func(script string) error {
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Dir = p.Root
		if err := sb.Wrap(cmd, p); err != nil {
			return err
		}
		return cmd.Run()
	}
	if err := run("cat a.txt"); err != nil {
		return fmt.Errorf("sandbox read-only self-check: reading a file failed: %w", err)
	}
	_ = run("echo x > b.txt")
	if exists(filepath.Join(root, "b.txt")) {
		return errors.New("sandbox read-only self-check: a file was created in the read-only root")
	}
	return nil
}
