package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrParentChanged is wrapped by refusals caused by the parent checkout no
// longer matching the integrated state the caller expects.
var ErrParentChanged = errors.New("parent changed")

// ConflictError reports a revision whose diff does not apply onto the current
// integrated parent state. The parent is untouched when it is returned.
type ConflictError struct {
	Paths  []string
	Detail string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("integration conflict on %s: %s", strings.Join(e.Paths, ", "), e.Detail)
}

// ApplyResult is the evidence of one applied revision: the paths its diff
// touched and the parent index tree after applying it.
type ApplyResult struct {
	Paths     []string
	TreeAfter string
}

const maxSymlinkTarget = 4096

// ParentTree returns the parent index tree (git write-tree) after rejecting
// index flags that would hide tracked bytes.
func (m *Manager) ParentTree(ctx context.Context, parent string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return parentTree(ctx, parent)
}

func parentTree(ctx context.Context, parent string) (string, error) {
	if err := ValidateIndexFlags(ctx, parent); err != nil {
		return "", err
	}
	return hardenedOutput(ctx, parent, "write-tree")
}

// CheckParentState verifies the parent is exactly the integrated state:
// HEAD == base, index tree == expectedTree, no unstaged differences, no
// untracked non-ignored files and only contained symlinks/regular files.
func (m *Manager) CheckParentState(ctx context.Context, parent, base, expectedTree string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return checkParentState(ctx, parent, base, expectedTree)
}

func checkParentState(ctx context.Context, parent, base, expectedTree string) error {
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	top, err := hardenedOutput(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, canonical) {
		return errors.New("integration requires the parent Git repository root")
	}
	if err = ValidateIndexFlags(ctx, canonical); err != nil {
		return err
	}
	head, err := hardenedOutput(ctx, canonical, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if head != base {
		return fmt.Errorf("%w: parent HEAD moved from %s to %s", ErrParentChanged, base, head)
	}
	tree, err := hardenedOutput(ctx, canonical, "write-tree")
	if err != nil {
		return err
	}
	if tree != expectedTree {
		staged, err := hardenedNULList(ctx, canonical, "diff-index", "--cached", "--name-only", "--no-renames", "-z", expectedTree, "--")
		if err != nil {
			return fmt.Errorf("%w: parent index tree %s differs from expected %s", ErrParentChanged, tree, expectedTree)
		}
		return fmt.Errorf("%w: staged content differs from the integrated state at %s", ErrParentChanged, strings.Join(staged, ", "))
	}
	unstaged, err := hardenedNULList(ctx, canonical, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "--no-renames", "-z", "--")
	if err != nil {
		return err
	}
	if len(unstaged) > 0 {
		return fmt.Errorf("%w: unstaged edits at %s", ErrParentChanged, strings.Join(unstaged, ", "))
	}
	untracked, err := hardenedNULList(ctx, canonical, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	if len(untracked) > 0 {
		return fmt.Errorf("%w: untracked files at %s", ErrParentChanged, strings.Join(untracked, ", "))
	}
	return ValidatePaths(ctx, canonical)
}

// ApplyOnIntegrated applies one agent revision onto the integrated parent
// state. Only `git diff --binary base result` computed in the owned agent
// checkout is used; its history, refs, config and objects are never imported.
// The parent index must equal expectedTree (the tree after previously accepted
// revisions) with HEAD == base; HEAD never moves. Any refusal or conflict
// leaves the parent unchanged. Guards run before any check and again right
// before the mutation. Cancellation is observed until the apply starts; the
// apply itself always runs to completion.
func (m *Manager) ApplyOnIntegrated(ctx context.Context, workspaceID, parent, base, expectedTree, result string, guards ...func() error) (ApplyResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fail := func(err error) (ApplyResult, error) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ApplyResult{}, ctxErr
		}
		return ApplyResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	for _, guard := range guards {
		if err := guard(); err != nil {
			return ApplyResult{}, err
		}
	}
	ws, err := m.lookup(ctx, workspaceID, parent)
	if err != nil {
		return fail(err)
	}
	if ws.SourceCommit != base {
		return ApplyResult{}, errors.New("owned checkout base revision changed")
	}
	// The agent controls its own .git: validate metadata with hardened Git
	// before any command that might consult agent configuration.
	if err = validateOwnedMetadata(ctx, ws.Path); err != nil {
		return fail(err)
	}
	if err = ValidateCommitted(ctx, ws.Path, result); err != nil {
		return fail(err)
	}
	entries, err := scanRevision(ctx, ws.Path, base, result)
	if err != nil {
		return fail(err)
	}
	patch, err := hardenedBytes(ctx, ws.Path, nil, "diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--no-color", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/", base, result, "--")
	if err != nil {
		return fail(err)
	}
	if err = checkParentState(ctx, parent, base, expectedTree); err != nil {
		return fail(err)
	}
	if err = checkSymlinks(ctx, parent, ws.Path, expectedTree, entries); err != nil {
		return fail(err)
	}
	paths := entryPaths(entries)
	if len(patch) == 0 {
		return ApplyResult{Paths: paths, TreeAfter: expectedTree}, nil
	}
	if err = ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	if _, stderr, err := hardenedRun(ctx, parent, patch, "apply", "--check", "--index", "--binary", "--whitespace=nowarn", "-"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ApplyResult{}, ctxErr
		}
		return ApplyResult{}, &ConflictError{Paths: conflictPaths(stderr, paths), Detail: commandError(err, stderr)}
	}
	if err = checkParentState(ctx, parent, base, expectedTree); err != nil {
		return fail(err)
	}
	for _, guard := range guards {
		if err := guard(); err != nil {
			return ApplyResult{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	// Once Git starts writing, finish the bounded operation: cancellation must
	// not kill it halfway through.
	done := context.WithoutCancel(ctx)
	if _, stderr, err := hardenedRun(done, parent, patch, "apply", "--index", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return ApplyResult{}, fmt.Errorf("integration could not apply after a successful check; inspect the parent: %s", commandError(err, stderr))
	}
	tree, err := parentTree(done, parent)
	if err != nil {
		return ApplyResult{Paths: paths}, fmt.Errorf("revision applied but the parent tree could not be read: %w", err)
	}
	return ApplyResult{Paths: paths, TreeAfter: tree}, nil
}

// rawEntry is one `git diff --raw` record of the agent revision.
type rawEntry struct {
	oldMode, newMode, newID, status, path string
}

// scanRevision lists the agent revision's changes and rejects entries that
// must never reach the parent: gitlinks, any `.git` path component and
// unsafe paths. Symlink containment is checked by checkSymlinks.
func scanRevision(ctx context.Context, agent, base, result string) ([]rawEntry, error) {
	fields, err := hardenedNULList(ctx, agent, "diff-tree", "-r", "-z", "--no-renames", "--no-abbrev", base, result, "--")
	if err != nil {
		return nil, err
	}
	if len(fields)%2 != 0 {
		return nil, errors.New("invalid revision diff listing")
	}
	var entries []rawEntry
	for i := 0; i < len(fields); i += 2 {
		header := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(header) != 5 {
			return nil, errors.New("invalid revision diff record")
		}
		e := rawEntry{oldMode: header[0], newMode: header[1], newID: header[3], status: header[4], path: fields[i+1]}
		if e.oldMode == "160000" || e.newMode == "160000" {
			return nil, fmt.Errorf("revision changes submodule %q; submodules are unsupported", e.path)
		}
		if err = validateRel(e.path); err != nil {
			return nil, err
		}
		for _, component := range strings.Split(e.path, "/") {
			if strings.EqualFold(component, ".git") || component == ".." || component == "." || component == "" {
				return nil, fmt.Errorf("revision touches forbidden path %q", e.path)
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func entryPaths(entries []rawEntry) []string {
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.path)
	}
	sort.Strings(paths)
	return paths
}

// treeLink is one path of the predicted post-apply tree.
type treeLink struct {
	mode, id string
	agent    bool // object lives in the agent checkout, not the parent
	deleted  bool
}

// checkSymlinks predicts the post-apply tree (expectedTree overlaid with the
// revision) and resolves every symlink in it, following chains through other
// symlinks, so no symlink can escape the parent checkout after the apply.
func checkSymlinks(ctx context.Context, parent, agent, expectedTree string, entries []rawEntry) error {
	tree := map[string]treeLink{}
	dirs := map[string]bool{}
	listing, err := hardenedNULList(ctx, parent, "ls-tree", "-r", "-z", "--full-tree", expectedTree)
	if err != nil {
		return err
	}
	for _, line := range listing {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return errors.New("invalid parent tree listing")
		}
		tree[name] = treeLink{mode: f[0], id: f[2]}
	}
	for _, e := range entries {
		if e.status == "D" {
			tree[e.path] = treeLink{deleted: true}
			continue
		}
		tree[e.path] = treeLink{mode: e.newMode, id: e.newID, agent: true}
	}
	for name, l := range tree {
		if l.deleted {
			continue
		}
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	readTarget := func(l treeLink) (string, error) {
		dir := parent
		if l.agent {
			dir = agent
		}
		size, err := hardenedOutput(ctx, dir, "cat-file", "-s", l.id)
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(size); err != nil || n > maxSymlinkTarget {
			return "", errors.New("symlink target is too long")
		}
		b, err := hardenedBytes(ctx, dir, nil, "cat-file", "blob", l.id)
		if err != nil {
			return "", err
		}
		if len(b) > maxSymlinkTarget {
			return "", errors.New("symlink target is too long")
		}
		return string(b), nil
	}
	lookup := func(p string) (bool, string, error) {
		if l, ok := tree[p]; ok && !l.deleted {
			if l.mode != "120000" {
				return false, "", nil
			}
			target, err := readTarget(l)
			return true, target, err
		}
		if dirs[p] {
			return false, "", nil
		}
		if _, ok := tree[p]; ok {
			return false, "", nil // deleted by the revision
		}
		// Untracked but ignored parent content is not in the tree; consult the
		// filesystem so an ignored symlink cannot be used as a stepping stone.
		info, err := os.Lstat(filepath.Join(parent, filepath.FromSlash(p)))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return false, "", nil
		}
		target, err := os.Readlink(filepath.Join(parent, filepath.FromSlash(p)))
		return true, target, err
	}
	names := make([]string, 0, len(tree))
	for name := range tree {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		l := tree[name]
		if l.deleted || l.mode != "120000" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		target, err := readTarget(l)
		if err != nil {
			return fmt.Errorf("symlink %q: %w", name, err)
		}
		if err = resolveLink(name, target, lookup); err != nil {
			return err
		}
	}
	return nil
}

// resolveLink resolves a symlink at repository path link (slash separated)
// with the given target, following symlinks reported by lookup, and refuses
// any resolution that leaves the repository or does not terminate.
func resolveLink(link, target string, lookup func(string) (bool, string, error)) error {
	hops := 0
	var walk func(stack []string, target string) ([]string, error)
	walk = func(stack []string, target string) ([]string, error) {
		if strings.HasPrefix(target, "/") || filepath.IsAbs(target) {
			return nil, fmt.Errorf("symlink %q points outside the checkout", link)
		}
		out := append([]string(nil), stack...)
		for _, c := range strings.Split(target, "/") {
			switch c {
			case "", ".":
				continue
			case "..":
				if len(out) == 0 {
					return nil, fmt.Errorf("symlink %q points outside the checkout", link)
				}
				out = out[:len(out)-1]
				continue
			}
			out = append(out, c)
			isLink, next, err := lookup(strings.Join(out, "/"))
			if err != nil {
				return nil, err
			}
			if isLink {
				if hops++; hops > 40 {
					return nil, fmt.Errorf("symlink %q does not resolve (loop)", link)
				}
				if out, err = walk(out[:len(out)-1], next); err != nil {
					return nil, err
				}
			}
		}
		return out, nil
	}
	dir := strings.Split(link, "/")
	_, err := walk(dir[:len(dir)-1], target)
	return err
}

// conflictPaths extracts the revision paths named by `git apply` errors and
// falls back to every revision path when none can be recognised.
func conflictPaths(stderr string, paths []string) []string {
	var out []string
	for _, p := range paths {
		if strings.Contains(stderr, "error: "+p+": ") || strings.Contains(stderr, "error: patch failed: "+p+":") {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), paths...)
	}
	return out
}

// validateOwnedMetadata rejects agent-controlled Git metadata that could run
// commands or redirect object/ref lookups outside the owned checkout.
func validateOwnedMetadata(ctx context.Context, root string) error {
	metadata := filepath.Join(root, ".git")
	if err := filepath.WalkDir(metadata, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("owned Git metadata contains a symlink")
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	for _, p := range []string{"objects/info/alternates", "objects/info/http-alternates", "commondir", "info/grafts", "refs/replace"} {
		if _, err := os.Lstat(filepath.Join(metadata, p)); err == nil {
			return errors.New("owned Git metadata redirects outside its checkout")
		}
	}
	config, err := hardenedOutput(ctx, root, "config", "--local", "--no-includes", "--list")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(config, "\n") {
		if line == "" {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		switch key {
		case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "extensions.objectformat", "user.name", "user.email":
		default:
			return fmt.Errorf("unsupported owned checkout Git configuration %q; workspace retained for inspection", key)
		}
	}
	return nil
}

// hardenedRun runs Git with hooks and fsmonitor disabled, no system/global
// configuration, no inherited GIT_* environment and no optional index writes.
// Stdout and stderr are kept separate so diagnostics never enter a patch.
func hardenedRun(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, string, error) {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null", "-C", dir}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

func hardenedBytes(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	out, stderr, err := hardenedRun(ctx, dir, stdin, args...)
	if err != nil {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), commandError(err, stderr))
	}
	return out, nil
}

func hardenedOutput(ctx context.Context, dir string, args ...string) (string, error) {
	b, err := hardenedBytes(ctx, dir, nil, args...)
	return strings.TrimSpace(string(b)), err
}

func hardenedNULList(ctx context.Context, dir string, args ...string) ([]string, error) {
	b, err := hardenedBytes(ctx, dir, nil, args...)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range bytes.Split(b, []byte{0}) {
		if len(entry) > 0 {
			out = append(out, string(entry))
		}
	}
	return out, nil
}
