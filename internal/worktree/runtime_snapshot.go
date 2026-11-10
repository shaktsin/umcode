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
	"sort"
	"strings"
)

// SnapshotPath describes one visible source path at capture time. Absent
// entries preserve the identity of tracked files deleted in the checkout.
type SnapshotPath struct {
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// SnapshotManifest is the source state against which approval and merge must
// check. The index digest is of its raw bytes; capture never refreshes it.
type SnapshotManifest struct {
	Head        string                  `json:"head"`
	IndexSHA256 string                  `json:"indexSha256"`
	IndexPaths  map[string]string       `json:"indexPaths"`
	Paths       map[string]SnapshotPath `json:"paths"`
	Untracked   []string                `json:"untracked"`
}

type TaskSnapshot struct {
	SourceRoot     string           `json:"sourceRoot"`
	Destination    string           `json:"destination"`
	SourceHead     string           `json:"sourceHead"`
	BaseCommit     string           `json:"baseCommit"`
	Manifest       SnapshotManifest `json:"manifest"`
	UntrackedPaths []string         `json:"untrackedPaths"`
}

// CaptureSourceManifest reads the selected checkout without refreshing its
// index. Call it again immediately before approval or merge to detect drift.
func CaptureSourceManifest(ctx context.Context, source string) (SnapshotManifest, error) {
	root, err := filepath.EvalSymlinks(source)
	if err != nil {
		return SnapshotManifest{}, err
	}
	top, err := hardenedOutput(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, root) {
		return SnapshotManifest{}, errors.New("task snapshot requires a Git repository root")
	}
	head, err := hardenedOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return SnapshotManifest{}, errors.New("task snapshot requires an initial commit")
	}
	if err := ValidateIndexFlags(ctx, root); err != nil {
		return SnapshotManifest{}, err
	}
	if conflicts, err := hardenedNULList(ctx, root, "ls-files", "-u", "-z"); err != nil {
		return SnapshotManifest{}, err
	} else if len(conflicts) != 0 {
		return SnapshotManifest{}, errors.New("task snapshot refuses unresolved index conflicts")
	}
	if err := ValidatePaths(ctx, root); err != nil {
		return SnapshotManifest{}, err
	}
	tracked, err := hardenedNULList(ctx, root, "ls-files", "--cached", "-z")
	if err != nil {
		return SnapshotManifest{}, err
	}
	untracked, err := hardenedNULList(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return SnapshotManifest{}, err
	}
	if _, err := snapshotSize(root, untracked); err != nil {
		return SnapshotManifest{}, err
	}
	indexPaths := make(map[string]string)
	if staged, err := hardenedNULList(ctx, root, "ls-files", "--stage", "-z"); err != nil {
		return SnapshotManifest{}, err
	} else {
		for _, entry := range staged {
			if strings.HasPrefix(entry, "160000 ") {
				return SnapshotManifest{}, errors.New("task snapshot refuses submodules")
			}
			identity, rel, ok := strings.Cut(entry, "\t")
			if !ok {
				return SnapshotManifest{}, errors.New("invalid Git index entry")
			}
			indexPaths[rel] = identity
		}
	}
	headEntries := make([]string, 0)
	if tree, err := hardenedNULList(ctx, root, "ls-tree", "-r", "-z", "HEAD"); err != nil {
		return SnapshotManifest{}, err
	} else {
		for _, entry := range tree {
			if strings.HasPrefix(entry, "160000 ") {
				return SnapshotManifest{}, errors.New("task snapshot refuses submodules")
			}
			_, rel, ok := strings.Cut(entry, "\t")
			if !ok {
				return SnapshotManifest{}, errors.New("invalid HEAD tree entry")
			}
			headEntries = append(headEntries, rel)
		}
	}
	idxPath, err := hardenedOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return SnapshotManifest{}, err
	}
	idx, err := os.ReadFile(idxPath)
	if err != nil {
		return SnapshotManifest{}, err
	}
	h := sha256.Sum256(idx)
	m := SnapshotManifest{Head: head, IndexSHA256: hex.EncodeToString(h[:]), IndexPaths: indexPaths, Paths: map[string]SnapshotPath{}, Untracked: untracked}
	paths := append(headEntries, tracked...)
	paths = append(paths, untracked...)
	for _, rel := range paths {
		if err := validateRel(rel); err != nil {
			return SnapshotManifest{}, err
		}
		if _, seen := m.Paths[rel]; seen {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			m.Paths[rel] = SnapshotPath{Kind: "absent"}
			continue
		}
		if err != nil {
			return SnapshotManifest{}, err
		}
		var content []byte
		p := SnapshotPath{}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return SnapshotManifest{}, err
			}
			content = []byte(link)
			p.Kind, p.Mode = "symlink", 0120000
		case info.Mode().IsRegular():
			content, err = os.ReadFile(path)
			if err != nil {
				return SnapshotManifest{}, err
			}
			p.Kind, p.Mode = "file", 0100644
			if info.Mode()&0111 != 0 {
				p.Mode = 0100755
			}
		default:
			return SnapshotManifest{}, fmt.Errorf("task snapshot refuses special file %q", rel)
		}
		digest := sha256.Sum256(content)
		p.SHA256 = hex.EncodeToString(digest[:])
		m.Paths[rel] = p
	}
	sort.Strings(m.Untracked)
	return m, nil
}

// CaptureTaskSnapshot creates a self-contained private Git checkout whose
// commit represents the visible staged, unstaged and non-ignored untracked
// state. It does not invoke user hooks or filters and never writes the source.
func CaptureTaskSnapshot(ctx context.Context, source, destination string) (TaskSnapshot, error) {
	root, err := filepath.EvalSymlinks(source)
	if err != nil {
		return TaskSnapshot{}, err
	}
	manifest, err := CaptureSourceManifest(ctx, root)
	if err != nil {
		return TaskSnapshot{}, err
	}
	common, err := hardenedOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return TaskSnapshot{}, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return TaskSnapshot{}, errors.New("task snapshot destination already exists")
		}
		return TaskSnapshot{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return TaskSnapshot{}, err
	}
	format, err := hardenedOutput(ctx, root, "rev-parse", "--show-object-format")
	if err != nil || (format != "sha1" && format != "sha256") {
		return TaskSnapshot{}, fmt.Errorf("unsupported Git object format %q", format)
	}
	if _, err := cloneGit(ctx, "", "init", "--quiet", "--template=", "--object-format="+format, destination); err != nil {
		return TaskSnapshot{}, err
	}
	cleanup := func(err error) (TaskSnapshot, error) {
		_ = os.RemoveAll(destination)
		_ = os.Remove(destination + ".ready")
		return TaskSnapshot{}, err
	}
	if _, err := cloneGit(ctx, destination, "fetch", "--no-tags", root, manifest.Head); err != nil {
		return cleanup(err)
	}
	if _, err := cloneGit(ctx, destination, "read-tree", manifest.Head); err != nil {
		return cleanup(err)
	}
	paths := make([]string, 0, len(manifest.Paths))
	for rel := range manifest.Paths {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		p := manifest.Paths[rel]
		if p.Kind == "absent" {
			if _, err := cloneGit(ctx, destination, "update-index", "--force-remove", "--", rel); err != nil {
				return cleanup(err)
			}
			continue
		}
		if err := copyPath(root, destination, rel); err != nil {
			return cleanup(fmt.Errorf("copy %q: %w", rel, err))
		}
		var data []byte
		if p.Kind == "symlink" {
			link, err := os.Readlink(filepath.Join(root, rel))
			if err != nil {
				return cleanup(err)
			}
			data = []byte(link)
		} else {
			data, err = os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				return cleanup(err)
			}
		}
		blob, err := snapshotGitStdin(ctx, destination, data, "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return cleanup(err)
		}
		if _, err := cloneGit(ctx, destination, "update-index", "--add", "--cacheinfo", fmt.Sprintf("%o,%s,%s", p.Mode, blob, rel)); err != nil {
			return cleanup(err)
		}
	}
	post, err := CaptureSourceManifest(ctx, root)
	if err != nil {
		return cleanup(err)
	}
	if !EqualTaskManifest(manifest, post) {
		return cleanup(errors.New("source changed during task snapshot capture"))
	}
	tree, err := hardenedOutput(ctx, destination, "write-tree")
	if err != nil {
		return cleanup(err)
	}
	commit, err := snapshotGitStdin(ctx, destination, []byte("UMCode task snapshot\n"), "-c", "user.name=UMCode", "-c", "user.email=umcode@localhost", "commit-tree", tree, "-p", manifest.Head)
	if err != nil {
		return cleanup(err)
	}
	if _, err := cloneGit(ctx, destination, "update-ref", "HEAD", commit); err != nil {
		return cleanup(err)
	}
	status, err := hardenedOutput(ctx, destination, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return cleanup(err)
	}
	if status != "" {
		return cleanup(fmt.Errorf("task snapshot is not clean: %s", status))
	}
	if err := os.WriteFile(destination+".ready", []byte(common+"\n"+manifest.Head+"\n"+commit+"\n"), 0600); err != nil {
		return cleanup(err)
	}
	return TaskSnapshot{SourceRoot: root, Destination: destination, SourceHead: manifest.Head, BaseCommit: commit, Manifest: manifest, UntrackedPaths: manifest.Untracked}, nil
}

// EqualTaskManifest compares all captured source authority, including the
// original HEAD, raw index digest, visible paths and untracked path set.
func EqualTaskManifest(a, b SnapshotManifest) bool {
	if a.Head != b.Head || a.IndexSHA256 != b.IndexSHA256 || len(a.IndexPaths) != len(b.IndexPaths) || len(a.Paths) != len(b.Paths) || len(a.Untracked) != len(b.Untracked) {
		return false
	}
	for path, p := range a.IndexPaths {
		if b.IndexPaths[path] != p {
			return false
		}
	}
	for path, p := range a.Paths {
		if b.Paths[path] != p {
			return false
		}
	}
	for i, p := range a.Untracked {
		if b.Untracked[i] != p {
			return false
		}
	}
	return true
}

func snapshotGitStdin(ctx context.Context, dir string, data []byte, args ...string) (string, error) {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", dir}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), commandError(err, string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
