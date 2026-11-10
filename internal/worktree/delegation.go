package worktree

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Path is deterministic and does not create a checkout. Persist ownership
// before EnsureCommitted so even failed setup has a recoverable identity.
func (m *Manager) Path(id string) string {
	h := sha256.Sum256([]byte(id))
	return filepath.Join(m.root, hex.EncodeToString(h[:]))
}

// Lookup only recognizes a completed owned self-contained checkout. It never
// creates a missing parent workspace or recovers/deletes a partial checkout.
func (m *Manager) Lookup(ctx context.Context, id, source string) (Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lookup(ctx, id, source)
}
func (m *Manager) lookup(ctx context.Context, id, source string) (Workspace, error) {
	return m.lookupWith(ctx, id, source, gitOutput)
}

// lookupWith runs every Git query through run so integration can use the
// hardened runner inside an untrusted agent checkout.
func (m *Manager) lookupWith(ctx context.Context, id, source string, run func(context.Context, string, ...string) (string, error)) (Workspace, error) {
	if id == "" {
		return Workspace{}, errors.New("workspace identity required")
	}
	path := m.Path(id)
	for _, p := range []string{m.root, path, filepath.Join(path, ".git"), path + ".ready"} {
		i, err := os.Lstat(p)
		if err != nil {
			return Workspace{}, err
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return Workspace{}, errors.New("owned workspace path or metadata is a symlink")
		}
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(actual) != filepath.Clean(path) {
		return Workspace{}, errors.New("owned workspace path escaped its canonical root")
	}
	if err = validateExistingWith(ctx, path, run); err != nil {
		return Workspace{}, err
	}
	common, err := run(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Workspace{}, err
	}
	b, err := os.ReadFile(path + ".ready")
	if err != nil {
		return Workspace{}, err
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(f) != 3 || !samePath(f[0], common) {
		return Workspace{}, errors.New("workspace ownership marker does not match its source project")
	}
	return Workspace{Path: path, ProjectRoot: source, SourceCommit: f[1], BaselineCommit: f[2]}, nil
}

// cleanCheckoutRefusal is the refusal for a project checkout that is not a
// clean committed state a delegation workspace can be created from.
const cleanCheckoutRefusal = "delegation requires a clean committed checkout; commit or remove changed and untracked required inputs (including UMCODE.md)"

// ValidateCommitted rejects dirty/untracked inputs and unsupported repository
// layouts before they can be silently copied by ordinary Ensure.
func ValidateCommitted(ctx context.Context, root, expected string) error {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	top, err := gitOutput(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, canonical) {
		return errors.New("delegation requires the Git repository root, not a non-Git folder or project subdirectory")
	}
	head, err := gitOutput(ctx, canonical, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return errors.New("delegation requires an initial committed Git revision")
	}
	if expected != "" && head != expected {
		return errors.New("delegation parent revision changed")
	}
	if err = ValidateIndexFlags(ctx, canonical); err != nil {
		return err
	}
	status, err := gitOutput(ctx, canonical, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New(cleanCheckoutRefusal)
	}
	entries, err := gitNULList(ctx, canonical, "ls-tree", "-r", "-z", "HEAD")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry, "160000 ") {
			return errors.New("delegation submodules are unsupported")
		}
	}
	return ValidatePaths(ctx, canonical)
}

// ValidateIndexFlags rejects index settings that make status/add/diff omit
// actual tracked bytes. User flags are never cleared or rewritten implicitly.
//
// It runs through the hardened runner (no fsmonitor hook, no inherited GIT_*
// environment, no system or global configuration); ls-files never refreshes
// or writes the index.
func ValidateIndexFlags(ctx context.Context, root string) error {
	entries, err := hardenedNULList(ctx, root, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if len(entry) < 2 {
			return errors.New("invalid Git index flag record")
		}
		tag := entry[0]
		if tag == 'S' || (tag >= 'a' && tag <= 'z') {
			return fmt.Errorf("unsupported Git index flag (assume-unchanged or skip-worktree) on %q; inspect and explicitly clear it before delegation", entry[2:])
		}
	}
	return nil
}

// ValidatePaths rejects symlinks that resolve outside the owned checkout,
// special files and nested repositories. Git paths are NUL delimited.
func ValidatePaths(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if path == filepath.Join(root, ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		if d.Name() == ".git" {
			return errors.New("nested Git repositories are unsupported")
		}
		i, err := d.Info()
		if err != nil {
			return err
		}
		if i.Mode()&os.ModeSymlink != 0 {
			p, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("unresolved workspace symlink: %w", err)
			}
			rel, err := filepath.Rel(root, p)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("workspace symlink escapes owned checkout")
			}
		} else if !i.IsDir() && !i.Mode().IsRegular() {
			return errors.New("unsupported workspace special file")
		}
		return nil
	})
}

// EnsureCommitted wraps Ensure's general dirty snapshot behavior with strict
// pre/post checks and tree comparison; newly created checkouts retain exact HEAD.
func (m *Manager) EnsureCommitted(ctx context.Context, id, source, revision string) (Workspace, error) {
	if err := ValidateCommitted(ctx, source, revision); err != nil {
		return Workspace{}, err
	}
	// Never invoke Ensure recovery on an occupied partial owned path.
	if _, err := os.Lstat(m.Path(id)); err == nil {
		ws, err := m.Lookup(ctx, id, source)
		if err != nil {
			return Workspace{}, err
		}
		if ws.SourceCommit != revision {
			return Workspace{}, errors.New("workspace source revision changed")
		}
		return ws, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Workspace{}, err
	}
	ws, err := m.Ensure(ctx, id, source)
	if err != nil {
		return Workspace{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ValidateCommitted(ctx, source, revision); err != nil {
		return ws, err
	}
	if ws.SourceCommit != revision {
		return ws, errors.New("source revision changed while copying checkout")
	}
	sourceTree, err := gitOutput(ctx, source, "rev-parse", revision+"^{tree}")
	if err != nil {
		return ws, err
	}
	tree, err := gitOutput(ctx, ws.Path, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return ws, err
	}
	if tree != sourceTree {
		return ws, errors.New("source tree changed while copying checkout; workspace retained")
	}
	if ws.Created {
		if err = runGit(ctx, ws.Path, "checkout", "--quiet", "--detach", revision); err != nil {
			return ws, err
		}
		common, err := gitOutput(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return ws, err
		}
		common, err = filepath.EvalSymlinks(common)
		if err != nil {
			return ws, err
		}
		if err = os.WriteFile(ws.Path+".ready", []byte(common+"\n"+revision+"\n"+revision+"\n"), 0600); err != nil {
			return ws, err
		}
		ws.BaselineCommit = revision
	}
	return ws, nil
}

// KeepCommitted applies a fixed submitted commit under the same manager lock
// used for Keep/Discard. --index makes Git validate all source/index preimages
// before applying; conflict errors never use --reject or partial patch mode.
// Parent HEAD is never moved; the result is staged for lead verification.
func (m *Manager) KeepCommitted(ctx context.Context, id, parent, base, result string, guards ...func() error) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, guard := range guards {
		if err := guard(); err != nil {
			return nil, err
		}
	}
	ws, err := m.lookup(ctx, id, parent)
	if err != nil {
		return nil, err
	}
	if err = ValidateCommitted(ctx, parent, base); err != nil {
		return nil, err
	}
	if err = ValidateCommitted(ctx, ws.Path, result); err != nil {
		return nil, err
	}
	if ws.SourceCommit != base {
		return nil, errors.New("owned checkout base revision changed")
	}
	patch, err := gitBytes(ctx, ws.Path, "diff", "--binary", base, result, "--")
	if err != nil {
		return nil, err
	}
	paths, err := gitNULList(ctx, ws.Path, "diff", "--name-only", "-z", base, result, "--")
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 {
		return paths, nil
	}
	// Cancellation is observed before mutation. Once Git starts applying, finish
	// the bounded operation so cancellation cannot kill it halfway through writes.
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", parent, "apply", "--check", "--index", "--binary", "-")
	cmd.Stdin = bytes.NewReader(patch)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("integration conflict: %s", commandError(err, string(b)))
	}
	if err = ValidateCommitted(ctx, parent, base); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, guard := range guards {
		if err := guard(); err != nil {
			return nil, err
		}
	}
	cmd = exec.Command("git", "-C", parent, "apply", "--index", "--binary", "-")
	cmd.Stdin = bytes.NewReader(patch)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("integration could not apply; workspace and intent retained: %s", commandError(err, string(b)))
	}
	return paths, nil
}

// DiscardCommitted refuses later edits even after lead verification. A missing
// checkout is an idempotent retry only for callers with durable cleanup intent.
func (m *Manager) DiscardCommitted(ctx context.Context, id, source, revision string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(m.Path(id)); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		ws, err := m.lookup(ctx, id, source)
		if err != nil {
			return err
		}
		if err = ValidateCommitted(ctx, ws.Path, revision); err != nil {
			return err
		}
		ignored, err := gitNULList(ctx, ws.Path, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
		if err != nil {
			return err
		}
		if len(ignored) > 0 {
			return errors.New("cleanup blocked by ignored workspace contents; inspect and preserve or explicitly remove them before retrying; workspace retained")
		}
	}
	return m.discard(ctx, id, source)
}

// WithCommittedBase runs a storage-only recovery mutation only while the
// admitted parent still matches its clean base under the integration lock.
// Callbacks must not reenter Manager or mutate checkout files.
func (m *Manager) WithCommittedBase(ctx context.Context, parent, base string, guard, save func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := guard(); err != nil {
		return err
	}
	if err := ValidateCommitted(ctx, parent, base); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return save()
}

// ParentStateForRuntime accepts the project checkout as the source of a new
// runtime agent workspace when HEAD == base, the index has no conflict stages
// and its tree is either the base tree or exactly headTree (the recorded
// integration head after accepted revisions; "" when there is none), nothing
// is unstaged or untracked, and every path is a contained symlink or regular
// file. Anything else is refused with ValidateCommitted's refusal text and
// the offending paths. It only reads, always through the hardened runner
// (no hooks or fsmonitor, no inherited GIT_* environment, optional index
// locks disabled): no command used can refresh or rewrite the index file,
// take index.lock or write an object (diff-index --cached instead of
// write-tree, status instead of porcelain diff).
func ParentStateForRuntime(ctx context.Context, parent, base, headTree string) error {
	return parentStateForRuntime(ctx, parent, base, headTree, false, cleanCheckoutRefusal, "delegation parent revision changed")
}

// amendRefusal prefixes StagedTree's refusals.
const amendRefusal = "amend requires the fix staged in the project checkout with nothing else changed (HEAD at the task base, no unstaged edits, no untracked files, no conflict stages)"

// parentStateForRuntime is ParentStateForRuntime's check. With anyStaged the
// staged content is not compared with any tree (StagedTree's caller decides
// what to do with it), but a staged gitlink is refused. refusal prefixes the
// dirty-checkout refusals and moved the moved-HEAD refusal.
func parentStateForRuntime(ctx context.Context, parent, base, headTree string, anyStaged bool, refusal, moved string) error {
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	top, err := hardenedOutput(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, canonical) {
		return errors.New("delegation requires the Git repository root, not a non-Git folder or project subdirectory")
	}
	head, err := hardenedOutput(ctx, canonical, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return errors.New("delegation requires an initial committed Git revision")
	}
	if head != base {
		return fmt.Errorf("%s (%w: HEAD moved from %s to %s)", moved, ErrParentChanged, base, head)
	}
	if err = ValidateIndexFlags(ctx, canonical); err != nil {
		return err
	}
	unmerged, err := hardenedNULList(ctx, canonical, "ls-files", "--unmerged", "-z")
	if err != nil {
		return err
	}
	if len(unmerged) > 0 {
		var paths []string
		seen := map[string]bool{}
		for _, entry := range unmerged {
			if _, p, ok := strings.Cut(entry, "\t"); ok && !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
		return fmt.Errorf("%s (%w: unresolved conflict stages at %s)", refusal, ErrParentChanged, strings.Join(paths, ", "))
	}
	entries, err := hardenedNULList(ctx, canonical, "ls-tree", "-r", "-z", "HEAD")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry, "160000 ") {
			return errors.New("delegation submodules are unsupported")
		}
	}
	if anyStaged {
		if err = rejectStagedGitlinks(ctx, canonical); err != nil {
			return err
		}
	}
	baseTree, err := hardenedOutput(ctx, canonical, "rev-parse", "--verify", base+"^{tree}")
	if err != nil {
		return err
	}
	// diff-index --cached compares index entries (path, mode, object) with a
	// tree without writing anything (write-tree may rewrite the index file).
	stagedAgainst := func(tree string) ([]string, error) {
		return hardenedNULList(ctx, canonical, "diff-index", "--cached", "--name-only", "--no-renames", "-z", tree, "--")
	}
	var staged []string
	if !anyStaged {
		if staged, err = stagedAgainst(baseTree); err != nil {
			return err
		}
	}
	if len(staged) > 0 && headTree != "" && headTree != baseTree {
		if staged, err = stagedAgainst(headTree); err != nil {
			return err
		}
	}
	if len(staged) > 0 {
		return fmt.Errorf("%s (%w: staged content differs from the integrated state at %s)", refusal, ErrParentChanged, strings.Join(staged, ", "))
	}
	// Unstaged and untracked changes come from one status call: unlike
	// porcelain diff, which refreshes stat-dirty entries and rewrites the
	// index under index.lock, status honours GIT_OPTIONAL_LOCKS=0 (set by the
	// hardened runner), so the project's index file is never written even
	// when a tracked file was only touched. Staged entries (the integrated
	// head) were checked above; only the worktree column and untracked
	// entries matter here.
	// The user's own global excludes file is honoured (userExcludesArgs);
	// no other global configuration is loaded.
	status, err := hardenedNULList(ctx, canonical, append(userExcludesArgs(ctx), "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=none")...)
	if err != nil {
		return err
	}
	var unstaged, untracked []string
	for _, entry := range status {
		if len(entry) < 4 || entry[2] != ' ' {
			return fmt.Errorf("invalid Git status record %q", entry)
		}
		switch xy, p := entry[:2], entry[3:]; {
		case xy == "??":
			untracked = append(untracked, p)
		case xy[1] != ' ':
			unstaged = append(unstaged, p)
		}
	}
	if len(unstaged) > 0 {
		return fmt.Errorf("%s (%w: unstaged edits at %s)", refusal, ErrParentChanged, strings.Join(unstaged, ", "))
	}
	if len(untracked) > 0 {
		return fmt.Errorf("%s (%w: untracked files at %s)", refusal, ErrParentChanged, strings.Join(untracked, ", "))
	}
	return ValidatePaths(ctx, canonical)
}

// rejectStagedGitlinks refuses an index holding a gitlink (submodule entry):
// a head tree with one could not be viewed or integrated.
func rejectStagedGitlinks(ctx context.Context, canonical string) error {
	entries, err := hardenedNULList(ctx, canonical, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry, "160000 ") {
			_, p, _ := strings.Cut(entry, "\t")
			return fmt.Errorf("staged submodule (gitlink) at %s is unsupported", p)
		}
	}
	return nil
}

// EnsureRuntime returns the owned checkout id of revision base, cloning it
// when it does not exist yet. The project checkout must pass
// ParentStateForRuntime (base or exactly the integrated head headTree). The
// clone is always the committed base revision, never the parent's staged or
// working tree state: only the base commit's objects are fetched, and they
// are immutable, so an apply running in the parent cannot be observed half
// way. Under the manager lock, which ApplyOnIntegrated holds for its whole
// run, so the parent check cannot interleave with an apply either. The
// parent is never written.
func (m *Manager) EnsureRuntime(ctx context.Context, id, parent, base, headTree string) (Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ParentStateForRuntime(ctx, parent, base, headTree); err != nil {
		return Workspace{}, err
	}
	// Never recover or delete an occupied (possibly partial) owned path.
	if _, err := os.Lstat(m.Path(id)); err == nil {
		ws, err := m.lookupWith(ctx, id, parent, hardenedOutput)
		if err != nil {
			return Workspace{}, err
		}
		if ws.SourceCommit != base {
			return ws, errors.New("workspace source revision changed")
		}
		return ws, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Workspace{}, err
	}
	return m.cloneRevision(ctx, id, parent, base)
}

// cloneRevision creates the owned self-contained checkout id holding exactly
// the committed revision of the source repository (shallow, detached, clean)
// and writes its ownership marker. The caller holds m.mu.
func (m *Manager) cloneRevision(ctx context.Context, id, source, revision string) (Workspace, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Workspace{}, errors.New("Git is required to create an isolated coding workspace; install Git and retry")
	}
	root, err := filepath.EvalSymlinks(source)
	if err != nil {
		return Workspace{}, err
	}
	common, err := hardenedOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Workspace{}, err
	}
	if common, err = filepath.EvalSymlinks(common); err != nil {
		return Workspace{}, err
	}
	objectFormat, err := hardenedOutput(ctx, root, "rev-parse", "--show-object-format")
	if err != nil || (objectFormat != "sha1" && objectFormat != "sha256") {
		return Workspace{}, fmt.Errorf("unsupported Git object format %q", objectFormat)
	}
	tree, err := hardenedOutput(ctx, root, "rev-parse", "--verify", revision+"^{tree}")
	if err != nil {
		return Workspace{}, err
	}
	if err = os.MkdirAll(m.root, 0o700); err != nil {
		return Workspace{}, fmt.Errorf("create UMCode worktree folder: %w", err)
	}
	path := m.Path(id)
	marker := path + ".ready"
	_ = os.Remove(marker)
	cleanup := func(cause error) (Workspace, error) {
		_ = os.RemoveAll(path)
		_ = os.Remove(marker)
		return Workspace{}, cause
	}
	if _, err := cloneGit(ctx, "", "init", "--quiet", "--template=", "--object-format="+objectFormat, path); err != nil {
		return cleanup(fmt.Errorf("initialize workspace Git metadata: %w", err))
	}
	if _, err = cloneGit(ctx, path, "fetch", "--depth=1", "--no-tags", root, revision); err != nil {
		return cleanup(fmt.Errorf("copy the task base revision into the workspace: %w", err))
	}
	if _, err = cloneGit(ctx, path, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return cleanup(fmt.Errorf("check out the task base revision in the workspace: %w", err))
	}
	if err = os.Remove(filepath.Join(path, ".git", "FETCH_HEAD")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cleanup(fmt.Errorf("remove temporary Git source reference: %w", err))
	}
	if head, err := hardenedOutput(ctx, path, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || head != revision {
		return cleanup(errors.New("workspace checkout is not the task base revision"))
	}
	if got, err := hardenedOutput(ctx, path, "rev-parse", "--verify", "HEAD^{tree}"); err != nil || got != tree {
		return cleanup(errors.New("workspace checkout tree is not the task base tree"))
	}
	if err = os.WriteFile(marker, []byte(common+"\n"+revision+"\n"+revision+"\n"), 0o600); err != nil {
		return cleanup(fmt.Errorf("mark workspace ready: %w", err))
	}
	return Workspace{Path: path, ProjectRoot: root, SourceCommit: revision, BaselineCommit: revision, Created: true}, nil
}

// cloneGit runs Git for cloneRevision: like the legacy Ensure (global
// configuration still applies), but with every inherited GIT_* variable
// removed (a GIT_DIR or GIT_WORK_TREE in the engine's environment must not
// redirect the clone, for example into the parent), hooks disabled
// (core.hooksPath=/dev/null, so no global post-checkout or
// reference-transaction hook runs in the new workspace) and only the local
// file transport the fetch from the parent needs. dir "" runs without -C.
func cloneGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never", "-c", "protocol.file.allow=always"}
	if dir != "" {
		prefix = append(prefix, "-C", dir)
	}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), commandError(err, string(b)))
	}
	return b, nil
}

// viewCommitBody is the fixed commit object of a runtime view: the
// integrated head tree on top of the task base, with a fixed identity and
// time, so its id depends only on (base, headTree).
func viewCommitBody(base, headTree string) []byte {
	return []byte("tree " + headTree + "\nparent " + base + "\nauthor UMCode <umcode@localhost> 0 +0000\ncommitter UMCode <umcode@localhost> 0 +0000\n\nUMCode integrated view\n")
}

// isObjectID reports whether s is a full hexadecimal Git object id.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// RuntimeViewCommit returns the id of the commit a runtime view of headTree
// on top of base checks out. It is computed in the parent without writing
// anything (hash-object without -w).
func RuntimeViewCommit(ctx context.Context, parent, base, headTree string) (string, error) {
	if !isObjectID(base) || !isObjectID(headTree) {
		return "", errors.New("runtime view needs full base and head object ids")
	}
	out, err := hardenedBytes(ctx, parent, viewCommitBody(base, headTree), "hash-object", "-t", "commit", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ApplyRuntimeView turns the owned checkout id, freshly cloned at base by
// EnsureRuntime, into a read-only view of the integrated head headTree:
//   - the parent is trusted but only read: the revision base→headTree is
//     scanned there with Plan 3's rules (no .git component, gitlink or
//     unsupported mode; every symlink of headTree contained), and its patch is
//     computed there with the hardened runner;
//   - the patch is applied with --index only inside the private checkout,
//     whose metadata is validated before and after (ValidateOwnedMetadata);
//   - the checkout's index tree must then equal headTree, its paths must pass
//     ValidatePaths, and it is committed as viewCommit (RuntimeViewCommit),
//     so it is a clean committed checkout of exactly the head.
//
// Any failure leaves the view unusable (HEAD is moved only at the end). An
// already finished view is checked and accepted again.
func (m *Manager) ApplyRuntimeView(ctx context.Context, id, parent, base, headTree, viewCommit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	expected, err := RuntimeViewCommit(ctx, parent, base, headTree)
	if err != nil {
		return err
	}
	if expected != viewCommit {
		return fmt.Errorf("runtime view commit %s is not the view of head %s (%s)", viewCommit, headTree, expected)
	}
	ws, err := m.lookupWith(ctx, id, parent, hardenedOutput)
	if err != nil {
		return err
	}
	if ws.SourceCommit != base {
		return errors.New("owned checkout base revision changed")
	}
	if err = ValidateOwnedMetadata(ctx, ws.Path); err != nil {
		return err
	}
	head, err := hardenedOutput(ctx, ws.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	switch head {
	case viewCommit:
		return checkView(ctx, ws.Path, viewCommit, headTree)
	case base:
	default:
		return fmt.Errorf("view checkout HEAD %s is neither the base nor the view; workspace retained", head)
	}
	if err = cleanAt(ctx, ws.Path, base); err != nil {
		return fmt.Errorf("view checkout is not the clean base revision; workspace retained: %w", err)
	}
	entries, err := scanRevision(ctx, parent, base, headTree)
	if err != nil {
		return err
	}
	deleted, changed := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		changed[e.path] = true
		if e.status == "D" {
			deleted[e.path] = true
		}
	}
	if err = checkSymlinks(ctx, ws.Path, parent, headTree, deleted, changed); err != nil {
		return err
	}
	patch, err := hardenedBytes(ctx, parent, nil, "diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--no-color", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/", base, headTree, "--")
	if err != nil {
		return err
	}
	if len(patch) > 0 {
		if _, stderr, err := HardenedGit(ctx, ws.Path, patch, "apply", "--index", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return fmt.Errorf("integrated view could not be applied in the workspace; workspace retained: %s", commandError(err, stderr))
		}
	}
	tree, err := hardenedOutput(ctx, ws.Path, "write-tree")
	if err != nil {
		return err
	}
	if tree != headTree {
		return fmt.Errorf("integrated view tree %s differs from the head %s; workspace retained", tree, headTree)
	}
	if err = ValidatePaths(ctx, ws.Path); err != nil {
		return err
	}
	out, err := hardenedBytes(ctx, ws.Path, viewCommitBody(base, headTree), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(out)); got != viewCommit {
		return fmt.Errorf("integrated view commit %s differs from %s", got, viewCommit)
	}
	if _, err = hardenedBytes(ctx, ws.Path, nil, "update-ref", "--no-deref", "HEAD", viewCommit, base); err != nil {
		return err
	}
	if err = checkView(ctx, ws.Path, viewCommit, headTree); err != nil {
		return err
	}
	return ValidateOwnedMetadata(ctx, ws.Path)
}

// checkView verifies a finished view: HEAD is viewCommit whose tree is
// headTree and the checkout is clean at it.
func checkView(ctx context.Context, root, viewCommit, headTree string) error {
	tree, err := hardenedOutput(ctx, root, "rev-parse", "--verify", viewCommit+"^{tree}")
	if err != nil {
		return err
	}
	if tree != headTree {
		return fmt.Errorf("view commit tree %s is not the head %s", tree, headTree)
	}
	if err = cleanAt(ctx, root, viewCommit); err != nil {
		return fmt.Errorf("view checkout changed; workspace retained: %w", err)
	}
	return nil
}

// cleanAt requires the checkout's index and working tree to equal revision
// with no other file at all (untracked or ignored). Read-only plumbing.
func cleanAt(ctx context.Context, root, revision string) error {
	staged, err := hardenedNULList(ctx, root, "diff-index", "--cached", "--name-only", "--no-renames", "-z", revision, "--")
	if err != nil {
		return err
	}
	unstaged, err := hardenedNULList(ctx, root, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "--no-renames", "-z", "--")
	if err != nil {
		return err
	}
	others, err := hardenedNULList(ctx, root, "ls-files", "--others", "-z")
	if err != nil {
		return err
	}
	if paths := append(append(staged, unstaged...), others...); len(paths) > 0 {
		return fmt.Errorf("changed at %s", strings.Join(paths, ", "))
	}
	return nil
}
