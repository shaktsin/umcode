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
	if err = m.validateExisting(ctx, path); err != nil {
		return Workspace{}, err
	}
	common, err := gitOutput(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
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
		return errors.New("delegation requires a clean committed checkout; commit or remove changed and untracked required inputs (including UMCODE.md)")
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
func ValidateIndexFlags(ctx context.Context, root string) error {
	entries, err := gitNULList(ctx, root, "ls-files", "-v", "-z")
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
