// Package worktree creates persistent, task-specific Git workspaces.
package worktree

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const maxUntrackedSnapshot = 1 << 30 // Never duplicate more than 1 GiB of untracked data.

// Workspace describes a managed checkout associated with one chat/task.
type Workspace struct {
	Path           string
	ProjectRoot    string
	SourceCommit   string
	BaselineCommit string
	Created        bool
}

// Manager stores task workspaces beneath the app's private data directory.
// Each workspace has self-contained, shallow Git metadata: a native `git
// worktree`'s .git file points back into the source repository, which would
// break Git commands inside a VM that mounts only the task workspace.
type Manager struct {
	root string
	mu   sync.Mutex // git worktree metadata is shared and must be mutated serially.
}

// New creates a manager rooted under appDataDir/worktrees.
func New(appDataDir string) *Manager {
	return &Manager{root: filepath.Join(appDataDir, "worktrees")}
}

// HasWorkspace reports whether this task already has a completed managed
// workspace. It is used when upgrading chats that historically defaulted to
// worktrees, so saved work stays isolated while failed first attempts can fall
// back to the project folder.
func (m *Manager) HasWorkspace(taskID string) bool {
	if strings.TrimSpace(taskID) == "" {
		return false
	}
	h := sha256.Sum256([]byte(taskID))
	path := filepath.Join(m.root, hex.EncodeToString(h[:]))
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return false
	}
	if info, err := os.Stat(path + ".ready"); err != nil || !info.Mode().IsRegular() {
		return false
	}
	return true
}

// Ensure returns a stable task workspace for taskID. The starting snapshot includes
// HEAD, staged/unstaged tracked changes, and non-ignored untracked files from
// the user's project. Ignored files (which commonly include secrets and build
// products) are intentionally excluded.
func (m *Manager) Ensure(ctx context.Context, taskID, projectRoot string) (Workspace, error) {
	if strings.TrimSpace(taskID) == "" {
		return Workspace{}, errors.New("cannot create a coding workspace without a task id")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return Workspace{}, errors.New("Git is required to create an isolated coding workspace; install Git and retry")
	}
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve project folder: %w", err)
	}
	top, err := gitOutput(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return Workspace{}, errors.New("coding tasks need a Git repository; initialize Git in this project or choose a Git-backed project")
	}
	top = filepath.Clean(top)
	if !samePath(root, top) {
		return Workspace{}, errors.New("coding worktrees currently require the selected project folder to be the Git repository root")
	}
	commit, err := gitOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Workspace{}, errors.New("this Git project has no commit yet; create an initial commit before starting a coding task")
	}
	commonDir, err := gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve Git metadata: %w", err)
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve Git metadata: %w", err)
	}
	h := sha256.Sum256([]byte(taskID))
	path := filepath.Join(m.root, hex.EncodeToString(h[:]))
	marker := path + ".ready"
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return Workspace{}, fmt.Errorf("create UMCode worktree folder: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		ready, readErr := os.ReadFile(marker)
		if readErr == nil {
			fields := strings.Split(strings.TrimSpace(string(ready)), "\n")
			if len(fields) != 3 || !samePath(fields[0], commonDir) {
				return Workspace{}, errors.New("a saved task workspace is associated with a different project; preserve it and inspect it before retrying")
			}
			if err := m.validateExisting(ctx, path); err != nil {
				return Workspace{}, err
			}
			return Workspace{Path: path, ProjectRoot: root, SourceCommit: fields[1], BaselineCommit: fields[2]}, nil
		}
		// A crash may have happened between `git init` and snapshot finish.
		// Only remove it if Git confirms this exact path is a repository root.
		partialRoot, gitErr := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
		if gitErr != nil || !samePath(partialRoot, path) {
			return Workspace{}, errors.New("an incomplete path occupies this task's workspace; preserve it and inspect it before retrying")
		}
		if err := os.RemoveAll(path); err != nil {
			return Workspace{}, fmt.Errorf("recover incomplete task workspace: %w", err)
		}
		_ = os.Remove(marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Workspace{}, err
	} else {
		_ = os.Remove(marker)
	}

	commit, err = gitOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Workspace{}, errors.New("this Git project has no commit yet; create an initial commit before starting a coding task")
	}
	diff, err := gitBytes(ctx, root, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return Workspace{}, fmt.Errorf("snapshot tracked changes: %w", err)
	}
	untracked, err := gitNULList(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Workspace{}, fmt.Errorf("list untracked project files: %w", err)
	}
	if _, err := snapshotSize(root, untracked); err != nil {
		return Workspace{}, err
	}

	objectFormat, err := gitOutput(ctx, root, "rev-parse", "--show-object-format")
	if err != nil || (objectFormat != "sha1" && objectFormat != "sha256") {
		return Workspace{}, fmt.Errorf("unsupported Git object format %q", objectFormat)
	}
	if err := exec.CommandContext(ctx, "git", "init", "--quiet", "--object-format="+objectFormat, path).Run(); err != nil {
		_ = os.RemoveAll(path)
		return Workspace{}, fmt.Errorf("initialize task Git metadata: %w", err)
	}
	if _, err := gitBytes(ctx, path, "fetch", "--depth=1", "--no-tags", root, commit); err != nil {
		_ = os.RemoveAll(path)
		return Workspace{}, fmt.Errorf("copy the project baseline into the task workspace: %w", err)
	}
	if _, err := gitBytes(ctx, path, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		_ = os.RemoveAll(path)
		return Workspace{}, fmt.Errorf("check out the project baseline in the task workspace: %w", err)
	}
	if err := os.Remove(filepath.Join(path, ".git", "FETCH_HEAD")); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(path)
		return Workspace{}, fmt.Errorf("remove temporary Git source reference: %w", err)
	}
	cleanup := func(cause error) (Workspace, error) {
		_ = os.RemoveAll(path)
		_ = os.Remove(marker)
		return Workspace{}, cause
	}
	if len(diff) > 0 {
		cmd := exec.CommandContext(ctx, "git", "-C", path, "apply", "--binary", "--whitespace=nowarn", "-")
		cmd.Stdin = bytes.NewReader(diff)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			return cleanup(fmt.Errorf("copy the project's tracked edits into the task worktree: %s", commandError(err, output.String())))
		}
	}
	for _, rel := range untracked {
		if err := copyPath(root, path, rel); err != nil {
			return cleanup(fmt.Errorf("copy untracked project file %q into task worktree: %w", rel, err))
		}
	}
	if _, err := gitBytes(ctx, path, "add", "--all"); err != nil {
		return cleanup(fmt.Errorf("stage task baseline: %w", err))
	}
	if _, err := gitBytes(ctx, path, "-c", "user.name=UMCode", "-c", "user.email=umcode@localhost",
		"commit", "--quiet", "--allow-empty", "-m", "UMCode task baseline"); err != nil {
		return cleanup(fmt.Errorf("record task baseline: %w", err))
	}
	baseline, err := gitOutput(ctx, path, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return cleanup(fmt.Errorf("resolve task baseline: %w", err))
	}
	if err := os.WriteFile(marker, []byte(commonDir+"\n"+commit+"\n"+baseline+"\n"), 0o600); err != nil {
		return cleanup(fmt.Errorf("mark task worktree ready: %w", err))
	}
	return Workspace{Path: path, ProjectRoot: root, SourceCommit: commit, BaselineCommit: baseline, Created: true}, nil
}

func (m *Manager) validateExisting(ctx context.Context, path string) error {
	root, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(root, path) {
		return errors.New("the saved task workspace is not a valid Git worktree; move it aside and retry")
	}
	common, err := gitOutput(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !samePath(common, filepath.Join(path, ".git")) {
		return errors.New("the saved task workspace does not contain self-contained Git metadata; preserve it and inspect it before retrying")
	}
	if _, err := gitOutput(ctx, path, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return errors.New("the saved task workspace no longer has a valid commit; preserve its work and inspect it before retrying")
	}
	return nil
}

// Keep applies only changes made after the task's initial snapshot back to the
// project checkout. Git's three-way apply fails rather than clobbering edits
// that have diverged since the task started. It never commits or pushes.
func (m *Manager) Keep(ctx context.Context, taskID, projectRoot string) (int, error) {
	ws, err := m.Ensure(ctx, taskID, projectRoot)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := gitBytes(ctx, ws.Path, "add", "--all"); err != nil {
		return 0, fmt.Errorf("stage task changes for review: %w", err)
	}
	patch, err := gitBytes(ctx, ws.Path, "diff", "--binary", ws.BaselineCommit, "--")
	if err != nil {
		return 0, fmt.Errorf("prepare task changes: %w", err)
	}
	if len(patch) == 0 {
		return 0, nil
	}
	paths, err := gitNULList(ctx, ws.Path, "diff", "--name-only", "-z", ws.BaselineCommit, "--")
	if err != nil {
		return 0, fmt.Errorf("list task changes: %w", err)
	}
	for _, args := range [][]string{{"apply", "--check", "--binary", "-"}, {"apply", "--binary", "-"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", ws.ProjectRoot}, args...)...)
		cmd.Stdin = bytes.NewReader(patch)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("could not keep task changes because the source checkout has conflicting edits: %s",
				commandError(err, output.String()))
		}
	}
	if _, err := gitBytes(ctx, ws.Path, "add", "--all"); err != nil {
		return len(paths), fmt.Errorf("task changes were applied, but could not stage the new task baseline: %w", err)
	}
	if _, err := gitBytes(ctx, ws.Path, "-c", "user.name=UMCode", "-c", "user.email=umcode@localhost",
		"commit", "--quiet", "--allow-empty", "-m", "UMCode kept task baseline"); err != nil {
		return len(paths), fmt.Errorf("task changes were applied, but could not record the new task baseline: %w", err)
	}
	baseline, err := gitOutput(ctx, ws.Path, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return len(paths), fmt.Errorf("task changes were applied, but could not resolve the new baseline: %w", err)
	}
	marker := ws.Path + ".ready"
	ready, err := os.ReadFile(marker)
	if err != nil {
		return len(paths), fmt.Errorf("task changes were applied, but could not read the workspace marker: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(string(ready)), "\n")
	if len(fields) != 3 {
		return len(paths), errors.New("task changes were applied, but the workspace marker is invalid")
	}
	tmpMarker := marker + ".tmp"
	if err := os.WriteFile(tmpMarker, []byte(fields[0]+"\n"+fields[1]+"\n"+baseline+"\n"), 0o600); err != nil {
		return len(paths), fmt.Errorf("task changes were applied, but could not save the new baseline: %w", err)
	}
	if err := os.Rename(tmpMarker, marker); err != nil {
		_ = os.Remove(tmpMarker)
		return len(paths), fmt.Errorf("task changes were applied, but could not save the new baseline: %w", err)
	}
	return len(paths), nil
}

// Discard removes a stopped task workspace. Callers should require an explicit
// user confirmation because uncommitted task changes are permanently removed.
func (m *Manager) Discard(ctx context.Context, taskID, projectRoot string) error {
	if strings.TrimSpace(taskID) == "" {
		return errors.New("cannot discard a coding workspace without a task id")
	}
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve project folder: %w", err)
	}
	commonDir, err := gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return errors.New("coding workspace project is no longer a Git repository")
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return fmt.Errorf("resolve Git metadata: %w", err)
	}
	h := sha256.Sum256([]byte(taskID))
	path := filepath.Join(m.root, hex.EncodeToString(h[:]))
	marker := path + ".ready"
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to discard a task workspace path that is not a normal directory")
	}
	ready, err := os.ReadFile(marker)
	if err != nil {
		return errors.New("refusing to discard an unrecognized task workspace without its ownership marker")
	}
	fields := strings.Split(strings.TrimSpace(string(ready)), "\n")
	if len(fields) != 3 || !samePath(fields[0], commonDir) {
		return errors.New("refusing to discard a task workspace associated with a different project")
	}
	if err := m.validateExisting(ctx, path); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("discard task workspace: %w", err)
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove task workspace marker: %w", err)
	}
	return nil
}

func snapshotSize(root string, paths []string) (int64, error) {
	var total int64
	for _, rel := range paths {
		if err := validateRel(rel); err != nil {
			return 0, err
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return 0, err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
			if total > maxUntrackedSnapshot {
				return 0, errors.New("untracked project files exceed the 1 GiB task snapshot limit; ignore large generated files or commit them first")
			}
		}
	}
	return total, nil
}

func copyPath(sourceRoot, targetRoot, rel string) error {
	if err := validateRel(rel); err != nil {
		return err
	}
	src := filepath.Join(sourceRoot, filepath.FromSlash(rel))
	dst := filepath.Join(targetRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(link, dst)
	case info.Mode().IsRegular():
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	default:
		return fmt.Errorf("unsupported file type %s", info.Mode().Type())
	}
}

func validateRel(rel string) error {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe project-relative path %q", rel)
	}
	return nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.EvalSymlinks(a)
	if errA != nil {
		aa, errA = filepath.Abs(filepath.Clean(a))
	}
	bb, errB := filepath.EvalSymlinks(b)
	if errB != nil {
		bb, errB = filepath.Abs(filepath.Clean(b))
	}
	return errA == nil && errB == nil && aa == bb
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	b, err := gitBytes(ctx, dir, args...)
	return strings.TrimSpace(string(b)), err
}

func gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), commandError(err, string(b)))
	}
	return b, nil
}

func gitNULList(ctx context.Context, dir string, args ...string) ([]string, error) {
	b, err := gitBytes(ctx, dir, args...)
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

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := gitBytes(ctx, dir, args...)
	return err
}

func commandError(err error, output string) string {
	output = strings.TrimSpace(output)
	if output != "" {
		return output
	}
	return err.Error()
}
