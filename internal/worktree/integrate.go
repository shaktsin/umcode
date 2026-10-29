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

// UntrackedError reports untracked, non-ignored files in the parent checkout.
// It is a parent-changed refusal (errors.Is(err, ErrParentChanged)).
type UntrackedError struct {
	Paths []string
}

func (e *UntrackedError) Error() string {
	return fmt.Sprintf("%v: untracked files at %s", ErrParentChanged, strings.Join(e.Paths, ", "))
}

func (e *UntrackedError) Unwrap() error { return ErrParentChanged }

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
// Note: eol/text .gitattributes can make freshly applied files read as
// unstaged edits (phantom diffs); such a parent is refused, not normalised.
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
		return &UntrackedError{Paths: untracked}
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
	// The agent may still be able to write its checkout, including .git. Every
	// Git call inside it uses the hardened runner, whose command-line overrides
	// disable hooks, fsmonitor, pagers and transports whatever the agent's
	// config says, and only plumbing that never runs filters or diff drivers.
	ws, err := m.lookupWith(ctx, workspaceID, parent, hardenedOutput)
	if err != nil {
		return fail(err)
	}
	if ws.SourceCommit != base {
		return ApplyResult{}, errors.New("owned checkout base revision changed")
	}
	if err = ValidateOwnedMetadata(ctx, ws.Path); err != nil {
		return fail(err)
	}
	if integrateHook != nil {
		integrateHook("validated")
	}
	if err = validateAgentHead(ctx, ws.Path, result); err != nil {
		return fail(err)
	}
	// Early rejection with precise messages; the authoritative checks below
	// run on the parent-side result of applying the exact patch bytes, because
	// agent objects can be swapped under the same ids between two reads.
	entries, err := scanRevision(ctx, ws.Path, base, result)
	if err != nil {
		return fail(err)
	}
	if integrateHook != nil {
		integrateHook("scanned")
	}
	patch, err := hardenedBytes(ctx, ws.Path, nil, "diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--no-color", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/", base, result, "--")
	if err != nil {
		return fail(err)
	}
	// Re-check after the last Git call in the agent checkout: a configuration
	// change during the calls above was inert, but it must not be accepted.
	if err = ValidateOwnedMetadata(ctx, ws.Path); err != nil {
		return fail(err)
	}
	paths := entryPaths(entries)
	if err = checkParentState(ctx, parent, base, expectedTree); err != nil {
		return fail(err)
	}
	if len(patch) == 0 {
		if len(entries) != 0 {
			return ApplyResult{}, errors.New("agent revision changed during integration")
		}
		return ApplyResult{Paths: paths, TreeAfter: expectedTree}, nil
	}
	if err = ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	predicted, changedPaths, err := predictTree(ctx, parent, expectedTree, patch, paths)
	if err != nil {
		return fail(err)
	}
	if _, stderr, err := HardenedGit(ctx, parent, patch, "apply", "--check", "--index", "--binary", "--whitespace=nowarn", "-"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ApplyResult{}, ctxErr
		}
		return ApplyResult{}, classifyApplyCheck(stderr, paths, err)
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
	if _, stderr, err := HardenedGit(done, parent, patch, "apply", "--index", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return ApplyResult{}, fmt.Errorf("integration could not apply after a successful check; inspect the parent: %s", commandError(err, stderr))
	}
	tree, err := parentTree(done, parent)
	if err != nil {
		return ApplyResult{Paths: changedPaths}, fmt.Errorf("revision applied but the parent tree could not be read: %w", err)
	}
	if tree != predicted {
		return ApplyResult{Paths: changedPaths, TreeAfter: tree}, fmt.Errorf("revision applied but the parent tree %s differs from the verified prediction %s; inspect the parent", tree, predicted)
	}
	return ApplyResult{Paths: changedPaths, TreeAfter: tree}, nil
}

// PredictIntegration returns the tree ApplyOnIntegrated would produce by
// applying the owned checkout's base→result revision onto expectedTree. It is
// read-only: the same agent-side validation and patch as ApplyOnIntegrated,
// applied to a private temporary index (predictTree), so the parent's HEAD,
// index and worktree are never read for state or touched (only unreachable
// objects may be added to its object store, ruling R4). The parent's current
// state is irrelevant, so it can be used to recognise an apply that finished
// before a crash. A revision that does not apply onto expectedTree yields a
// *ConflictError.
func (m *Manager) PredictIntegration(ctx context.Context, workspaceID, parent, base, expectedTree, result string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ws, err := m.lookupWith(ctx, workspaceID, parent, hardenedOutput)
	if err != nil {
		return "", err
	}
	if ws.SourceCommit != base {
		return "", errors.New("owned checkout base revision changed")
	}
	if err = ValidateOwnedMetadata(ctx, ws.Path); err != nil {
		return "", err
	}
	if err = validateAgentHead(ctx, ws.Path, result); err != nil {
		return "", err
	}
	entries, err := scanRevision(ctx, ws.Path, base, result)
	if err != nil {
		return "", err
	}
	patch, err := hardenedBytes(ctx, ws.Path, nil, "diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--no-color", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/", base, result, "--")
	if err != nil {
		return "", err
	}
	if err = ValidateOwnedMetadata(ctx, ws.Path); err != nil {
		return "", err
	}
	if len(patch) == 0 {
		if len(entries) != 0 {
			return "", errors.New("agent revision changed during prediction")
		}
		return expectedTree, nil
	}
	predicted, _, err := predictTree(ctx, parent, expectedTree, patch, entryPaths(entries))
	return predicted, err
}

// predictTree applies the exact patch bytes to a private temporary index
// holding expectedTree, inside the parent repository, and validates the
// resulting tree: path rules, the same path set the agent revision reported,
// and containment of every symlink. The parent index, worktree and HEAD are
// never touched; only unreachable objects may be added to its object store.
func predictTree(ctx context.Context, parent, expectedTree string, patch []byte, paths []string) (string, []string, error) {
	dir, err := os.MkdirTemp("", "umcode-integrate-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	if _, stderr, err := hardenedRunEnv(ctx, parent, env, nil, "read-tree", expectedTree); err != nil {
		return "", nil, fmt.Errorf("integration could not prepare a private index: %s", commandError(err, stderr))
	}
	if _, stderr, err := hardenedRunEnv(ctx, parent, env, patch, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", nil, ctxErr
		}
		return "", nil, classifyApplyCheck(stderr, paths, err)
	}
	out, stderr, err := hardenedRunEnv(ctx, parent, env, nil, "write-tree")
	if err != nil {
		return "", nil, fmt.Errorf("integration could not write the predicted tree: %s", commandError(err, stderr))
	}
	predicted := strings.TrimSpace(string(out))
	entries, err := scanRevision(ctx, parent, expectedTree, predicted)
	if err != nil {
		return "", nil, err
	}
	scanned := map[string]bool{}
	for _, p := range paths {
		scanned[p] = true
	}
	deleted, changed := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		changed[e.path] = true
		if !scanned[e.path] {
			return "", nil, fmt.Errorf("agent revision changed during integration: the patch touches unscanned path %q", e.path)
		}
		if e.status == "D" {
			deleted[e.path] = true
		}
	}
	if err = checkSymlinks(ctx, parent, parent, predicted, deleted, changed); err != nil {
		return "", nil, err
	}
	return predicted, entryPaths(entries), nil
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
		switch e.newMode {
		case "000000", "100644", "100755", "120000":
		default:
			return nil, fmt.Errorf("revision uses unsupported mode %s for %q", e.newMode, e.path)
		}
		if err = validateRel(e.path); err != nil {
			return nil, err
		}
		for _, component := range strings.Split(e.path, "/") {
			if isDotGit(component) || component == ".." || component == "." || component == "" {
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

// checkSymlinks resolves every symlink of tree (read from the object store of
// objects) as it would exist in the checkout root, following chains through
// other symlinks, including ignored on-disk symlinks that are not tracked, so
// no symlink can escape the checkout or reach repository metadata. Paths in
// deleted are removed by the pending apply and are not followed.
func checkSymlinks(ctx context.Context, root, objects, tree string, deleted, changed map[string]bool) error {
	type node struct{ mode, id string }
	files := map[string]node{}
	dirs := map[string]bool{}
	// Paths changed by the revision and their ancestors: a pre-existing alias
	// pair in the parent is the user's state, an introduced one is refused.
	involved := map[string]bool{}
	for p := range changed {
		for ; p != "."; p = path.Dir(p) {
			involved[p] = true
		}
	}
	aliases := map[string]string{}
	addAlias := func(p string) error {
		key := pathAliasKey(p)
		if q, ok := aliases[key]; ok && q != p {
			if changed == nil || involved[p] || involved[q] {
				return fmt.Errorf("path %q aliases %q on case-insensitive filesystems", p, q)
			}
			return nil
		}
		aliases[key] = p
		return nil
	}
	listing, err := hardenedNULList(ctx, objects, "ls-tree", "-r", "-z", "--full-tree", tree)
	if err != nil {
		return err
	}
	for _, line := range listing {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return errors.New("invalid tree listing")
		}
		files[name] = node{mode: f[0], id: f[2]}
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	all := make([]string, 0, len(files)+len(dirs))
	for name := range files {
		all = append(all, name)
	}
	for dir := range dirs {
		all = append(all, dir)
	}
	sort.Strings(all)
	for _, p := range all {
		if err = addAlias(p); err != nil {
			return err
		}
	}
	readTarget := func(id string) (string, error) {
		size, err := hardenedOutput(ctx, objects, "cat-file", "-s", id)
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(size); err != nil || n > maxSymlinkTarget {
			return "", errors.New("symlink target is too long")
		}
		b, err := hardenedBytes(ctx, objects, nil, "cat-file", "blob", id)
		if err != nil {
			return "", err
		}
		if len(b) > maxSymlinkTarget {
			return "", errors.New("symlink target is too long")
		}
		return string(b), nil
	}
	lookup := func(p string) (bool, string, error) {
		if n, ok := files[p]; ok {
			if n.mode != "120000" {
				return false, "", nil
			}
			target, err := readTarget(n.id)
			return true, target, err
		}
		if dirs[p] || deleted[p] {
			return false, "", nil
		}
		// On case-insensitive filesystems this component is
		// another tracked entry (possibly a symlink): never treat it as missing.
		if q, ok := aliases[pathAliasKey(p)]; ok {
			return false, "", fmt.Errorf("path %q aliases %q on case-insensitive filesystems", p, q)
		}
		// Untracked but ignored content is not in the tree; consult the
		// filesystem so an ignored symlink cannot be used as a stepping stone.
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return false, "", nil
		}
		target, err := os.Readlink(filepath.Join(root, filepath.FromSlash(p)))
		return true, target, err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n := files[name]
		if n.mode != "120000" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if hasNonASCII(name) {
			return fmt.Errorf("symlink %q has a non-ASCII path; non-ASCII symlinks are unsupported by integration", name)
		}
		target, err := readTarget(n.id)
		if err != nil {
			return fmt.Errorf("symlink %q: %w", name, err)
		}
		if hasNonASCII(target) {
			return fmt.Errorf("symlink %q has a non-ASCII target; non-ASCII symlinks are unsupported by integration", name)
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
			if isDotGit(c) {
				return nil, fmt.Errorf("symlink %q resolves into repository metadata", link)
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

// integrateHook lets tests act between integration steps (for example to
// change the agent's configuration after it was validated). Nil in production.
var integrateHook func(stage string)

// validateAgentHead is the hardened replacement of ValidateCommitted for the
// untrusted agent checkout: plumbing only, so no index refresh, fsmonitor or
// clean filter runs. The agent's working tree is irrelevant because only the
// committed base→result diff is used.
func validateAgentHead(ctx context.Context, root, result string) error {
	top, err := hardenedOutput(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, root) {
		return errors.New("owned checkout is not its own Git repository root")
	}
	head, err := hardenedOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if head != result {
		return errors.New("owned checkout HEAD differs from the submitted revision")
	}
	return nil
}

// isDotGit reports whether a path component names repository metadata on
// some filesystem: ".git" in any case, with trailing dots/spaces or an NTFS
// stream suffix (Windows), with HFS+ ignorable code points (macOS), or the
// NTFS 8.3 short name "git~1". It mirrors Git's own verify_path rules.
func isDotGit(name string) bool {
	// A backslash separates components on Windows.
	if strings.Contains(name, "\\") {
		for _, part := range strings.Split(name, "\\") {
			if isDotGit(part) {
				return true
			}
		}
		return false
	}
	name = strings.Map(func(r rune) rune {
		if hfsIgnorable(r) {
			return -1
		}
		return r
	}, name)
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimRight(name, ". ")
	return strings.EqualFold(name, ".git") || strings.EqualFold(name, "git~1")
}

func hfsIgnorable(r rune) bool {
	return r == 0x200c || r == 0x200d || r == 0x200e || r == 0x200f ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x206a && r <= 0x206f) || r == 0xfeff
}

// pathAliasKey is aliasKey applied to every component of a slash path.
func pathAliasKey(p string) string {
	parts := strings.Split(p, "/")
	for i, c := range parts {
		parts[i] = aliasKey(c)
	}
	return strings.Join(parts, "/")
}

// aliasKey maps a path component to the key a case-insensitive filesystem
// would treat as the same entry: ASCII letters are lowercased and every other
// byte is kept verbatim. Unicode normalization and folding are deliberately
// not modelled (ruling R5): checkSymlinks refuses every symlink whose path or
// target contains a non-ASCII byte, so a symlink can only be named, and a
// target can only name, ASCII components, and those cannot alias through
// NFC/NFD, canonical singletons, HFS-ignorable code points or Unicode case
// folds. Aliasing between non-ASCII regular entries is harmless without such
// symlinks (a real directory pops the same way on `..`).
func aliasKey(name string) string {
	b := []byte(name)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func hasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// unquoteGitPath undoes Git's C-style quoting of a path ("\303\251", "\t",
// "\"", ...). Unquoted text is returned unchanged.
func unquoteGitPath(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	in := s[1 : len(s)-1]
	var out []byte
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c != '\\' || i+1 >= len(in) {
			out = append(out, c)
			continue
		}
		i++
		switch e := in[i]; e {
		case 'a':
			out = append(out, '\a')
		case 'b':
			out = append(out, '\b')
		case 't':
			out = append(out, '\t')
		case 'n':
			out = append(out, '\n')
		case 'v':
			out = append(out, '\v')
		case 'f':
			out = append(out, '\f')
		case 'r':
			out = append(out, '\r')
		default:
			if e >= '0' && e <= '3' && i+2 < len(in) && isOctal(in[i+1]) && isOctal(in[i+2]) {
				out = append(out, (e-'0')<<6|(in[i+1]-'0')<<3|(in[i+2]-'0'))
				i += 2
			} else {
				out = append(out, e)
			}
		}
	}
	return string(out)
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// classifyApplyCheck turns a failed `git apply` check into a ConflictError
// only when every reported error is a genuine content conflict with the
// integrated state (the patch preimage does not match, a path to add already
// exists in the index, a path to change no longer exists or changed type).
// A worktree that no longer matches the index means the parent changed
// (ErrParentChanged). Locks, permissions, files in the way, symlink traversal,
// I/O and anything unrecognised are ordinary errors the caller may retry.
func classifyApplyCheck(stderr string, paths []string, cause error) error {
	plain := func() error { return fmt.Errorf("integration check failed: %s", commandError(cause, stderr)) }
	known := map[string]bool{}
	for _, p := range paths {
		known[p] = true
	}
	conflictSuffixes := []string{": patch does not apply", ": already exists in index", ": does not exist in index", ": wrong type"}
	// Detail lines git prints next to a per-path conflict line.
	detailPrefixes := []string{"patch failed: ", "the patch applies to ", "removal patch leaves file contents", "binary patch does not apply to ",
		"binary patch to ", "deleted file "}
	var conflicts []string
	seen := map[string]bool{}
	changed := ""
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "warning: ") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "error: ")
		if !ok {
			return plain()
		}
		if p, ok := strings.CutSuffix(rest, ": does not match index"); ok && known[unquoteGitPath(p)] {
			changed = unquoteGitPath(p)
			continue
		}
		detail := false
		for _, prefix := range detailPrefixes {
			if strings.HasPrefix(rest, prefix) {
				detail = true
				break
			}
		}
		if detail {
			continue
		}
		p := ""
		for _, suffix := range conflictSuffixes {
			if trimmed, ok := strings.CutSuffix(rest, suffix); ok {
				p = unquoteGitPath(trimmed)
				break
			}
		}
		if p == "" || !known[p] {
			return plain()
		}
		if !seen[p] {
			seen[p] = true
			conflicts = append(conflicts, p)
		}
	}
	if changed != "" {
		return fmt.Errorf("%w: worktree differs from the index at %s", ErrParentChanged, changed)
	}
	if len(conflicts) == 0 {
		return plain()
	}
	sort.Strings(conflicts)
	return &ConflictError{Paths: conflicts, Detail: strings.TrimSpace(stderr)}
}

// ValidateOwnedMetadata rejects owned-checkout Git metadata that could run
// commands or redirect object/ref lookups outside the checkout: symlinks in
// .git, alternates, commondir, grafts, replace refs and any local
// configuration key outside a fixed allow list. Shared by delegation.
func ValidateOwnedMetadata(ctx context.Context, root string) error {
	metadata := filepath.Join(root, ".git")
	if info, err := os.Lstat(metadata); err != nil {
		return err
	} else if !info.IsDir() {
		return errors.New("private Git metadata is not a directory")
	}
	if err := filepath.WalkDir(metadata, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("private Git metadata contains a symlink")
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	for _, p := range []string{"objects/info/alternates", "objects/info/http-alternates", "commondir", "info/grafts", "refs/replace"} {
		if _, err := os.Lstat(filepath.Join(metadata, p)); err == nil {
			return errors.New("private Git metadata redirects outside its owned snapshot")
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
			return fmt.Errorf("unsupported private Git configuration %q; retain workspace for inspection", key)
		}
	}
	return nil
}

// HardenedGit runs Git with hooks, fsmonitor, pagers and every transport
// disabled by command-line overrides (which win over repository config), no
// system/global configuration, no inherited GIT_* environment and no optional
// index writes. Stdout and stderr are returned separately so diagnostics never
// enter parsed output or a patch.
func HardenedGit(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, string, error) {
	return hardenedRunEnv(ctx, dir, nil, stdin, args...)
}

// hardenedRunEnv is HardenedGit with extra GIT_* variables (for example a
// private GIT_INDEX_FILE) added after the inherited ones are removed.
func hardenedRunEnv(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, string, error) {
	prefix := []string{"--no-pager", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null",
		"-c", "protocol.allow=never", "-C", dir}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

func hardenedBytes(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	out, stderr, err := HardenedGit(ctx, dir, stdin, args...)
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
