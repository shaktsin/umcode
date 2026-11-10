package worktree

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TaskMergeChange describes one accepted path in the private integrated tree.
// Before and After are hashes of visible checkout content and file mode.
type TaskMergeChange struct {
	Path   string       `json:"path"`
	Before SnapshotPath `json:"before"`
	After  SnapshotPath `json:"after"`
	data   []byte
}

type TaskMergePlan struct {
	SourceRoot string            `json:"sourceRoot"`
	HeadTree   string            `json:"headTree"`
	Snapshot   TaskSnapshot      `json:"snapshot"`
	Changes    []TaskMergeChange `json:"changes"`
}

type MergeReceipt struct {
	HeadTree  string                  `json:"headTree"`
	Paths     []string                `json:"paths"`
	Before    map[string]SnapshotPath `json:"before"`
	After     map[string]SnapshotPath `json:"after"`
	AppliedAt time.Time               `json:"appliedAt"`
}

type mergeBackup struct {
	path string
	kind string
	mode os.FileMode
	data []byte
}

// PrepareTaskMerge reads only the accepted private tree and checks the selected
// checkout. It makes no source mutation; callers can persist intent first.
func PrepareTaskMerge(ctx context.Context, source string, snapshot TaskSnapshot, headTree string) (TaskMergePlan, error) {
	root, err := filepath.EvalSymlinks(source)
	if err != nil {
		return TaskMergePlan{}, err
	}
	if root != snapshot.SourceRoot || root == "" || snapshot.Destination == "" || snapshot.BaseCommit == "" || headTree == "" {
		return TaskMergePlan{}, errors.New("task merge workspace identity does not match the selected checkout")
	}
	if _, err := hardenedOutput(ctx, snapshot.Destination, "cat-file", "-e", snapshot.BaseCommit+"^{commit}"); err != nil {
		return TaskMergePlan{}, err
	}
	kind, err := hardenedOutput(ctx, snapshot.Destination, "cat-file", "-t", headTree)
	if err != nil || kind != "tree" {
		return TaskMergePlan{}, errors.New("task merge head must be an available tree")
	}
	baseTree, err := hardenedOutput(ctx, snapshot.Destination, "rev-parse", snapshot.BaseCommit+"^{tree}")
	if err != nil {
		return TaskMergePlan{}, err
	}
	paths, err := hardenedNULList(ctx, snapshot.Destination, "diff", "--name-only", "--no-renames", "-z", baseTree, headTree, "--")
	if err != nil {
		return TaskMergePlan{}, err
	}
	sort.Strings(paths)
	changed := map[string]bool{}
	for _, rel := range paths {
		if err := validateMergePath(rel); err != nil {
			return TaskMergePlan{}, err
		}
		changed[rel] = true
	}
	if err := checkSymlinks(ctx, root, snapshot.Destination, headTree, map[string]bool{}, changed); err != nil {
		return TaskMergePlan{}, err
	}
	entries, err := hardenedNULList(ctx, snapshot.Destination, "ls-tree", "-r", "-z", "--full-tree", headTree)
	if err != nil {
		return TaskMergePlan{}, err
	}
	type treeEntry struct{ mode, id string }
	tree := map[string]treeEntry{}
	for _, line := range entries {
		meta, rel, ok := strings.Cut(line, "\t")
		parts := strings.Fields(meta)
		if !ok || len(parts) != 3 {
			return TaskMergePlan{}, errors.New("invalid private task tree entry")
		}
		tree[rel] = treeEntry{parts[0], parts[2]}
	}
	plan := TaskMergePlan{SourceRoot: root, HeadTree: headTree, Snapshot: snapshot}
	for _, rel := range paths {
		before := snapshot.Manifest.Paths[rel]
		if before.Kind == "" {
			before.Kind = "absent"
		}
		change := TaskMergeChange{Path: rel, Before: before, After: SnapshotPath{Kind: "absent"}}
		if entry, ok := tree[rel]; ok {
			if entry.mode != "100644" && entry.mode != "100755" && entry.mode != "120000" {
				return TaskMergePlan{}, fmt.Errorf("unsupported merge mode %s at %q", entry.mode, rel)
			}
			size, err := hardenedOutput(ctx, snapshot.Destination, "cat-file", "-s", entry.id)
			if err != nil {
				return TaskMergePlan{}, err
			}
			var n int64
			if _, err := fmt.Sscan(size, &n); err != nil || n < 0 || n > 64<<20 {
				return TaskMergePlan{}, fmt.Errorf("task merge file %q is too large", rel)
			}
			change.data, err = hardenedBytes(ctx, snapshot.Destination, nil, "cat-file", "blob", entry.id)
			if err != nil {
				return TaskMergePlan{}, err
			}
			if int64(len(change.data)) != n {
				return TaskMergePlan{}, fmt.Errorf("task merge blob %q changed", rel)
			}
			digest := sha256.Sum256(change.data)
			change.After.SHA256 = hex.EncodeToString(digest[:])
			switch entry.mode {
			case "120000":
				change.After.Kind, change.After.Mode = "symlink", 0120000
			case "100755":
				change.After.Kind, change.After.Mode = "file", 0100755
			default:
				change.After.Kind, change.After.Mode = "file", 0100644
			}
		}
		plan.Changes = append(plan.Changes, change)
	}
	if err := checkTaskMergeTargets(ctx, plan); err != nil {
		return TaskMergePlan{}, err
	}
	return plan, nil
}

func validateMergePath(rel string) error {
	if err := validateRel(rel); err != nil {
		return err
	}
	if path.Clean(rel) != rel || strings.Contains(rel, "\\") {
		return fmt.Errorf("unsafe merge path %q", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || isDotGit(part) {
			return fmt.Errorf("unsafe merge path %q", rel)
		}
	}
	return nil
}

func checkMergeRootIdentity(sourceRoot string) error {
	resolved, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil || resolved != sourceRoot {
		return fmt.Errorf("%w: selected project root was replaced", ErrParentChanged)
	}
	return nil
}

func checkOpenedMergeRoot(sourceRoot string, root *os.Root) error {
	pathInfo, err := os.Lstat(sourceRoot)
	if err != nil || !pathInfo.IsDir() {
		return fmt.Errorf("%w: selected project root was replaced", ErrParentChanged)
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		return fmt.Errorf("%w: selected project root changed while opening", ErrParentChanged)
	}
	return nil
}

// checkTaskMergeTargets permits unrelated edits but refuses changes to any
// accepted path, its index entry, its parent directories or source HEAD.
func checkTaskMergeTargets(ctx context.Context, plan TaskMergePlan) error {
	return checkPendingMergeTargets(ctx, plan, 0)
}

func checkPendingMergeTargets(ctx context.Context, plan TaskMergePlan, start int) error {
	current, err := CaptureSourceManifest(ctx, plan.SourceRoot)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(plan.SourceRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	if current.Head != plan.Snapshot.Manifest.Head {
		return fmt.Errorf("%w: selected checkout HEAD moved", ErrParentChanged)
	}
	for _, c := range plan.Changes[start:] {
		if err := checkTargetPath(plan.SourceRoot, c.Path); err != nil {
			return fmt.Errorf("%w: %v", ErrParentChanged, err)
		}
		visible, err := readMergePathRoot(root, c.Path)
		if err != nil || visible != c.Before {
			return fmt.Errorf("%w: target %q changed or collides with an ignored file: %v", ErrParentChanged, c.Path, err)
		}
		got := current.Paths[c.Path]
		if got.Kind == "" {
			got.Kind = "absent"
		}
		if got != c.Before {
			return fmt.Errorf("%w: target %q changed since the agent snapshot", ErrParentChanged, c.Path)
		}
		if current.IndexPaths[c.Path] != plan.Snapshot.Manifest.IndexPaths[c.Path] {
			return fmt.Errorf("%w: index entry for %q changed", ErrParentChanged, c.Path)
		}
	}
	return nil
}

func checkTargetPath(root, rel string) error {
	parts := strings.Split(rel, "/")
	parent := root
	for i, part := range parts {
		entries, err := os.ReadDir(parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if aliasKey(entry.Name()) == aliasKey(part) && entry.Name() != part {
				return fmt.Errorf("target %q aliases existing %q", rel, entry.Name())
			}
		}
		next := filepath.Join(parent, part)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			if i < len(parts)-1 {
				break
			}
		} else if err != nil {
			return err
		} else if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("target %q has non-directory ancestor %q", rel, part)
		}
		parent = next
	}
	return nil
}

// ApplyTaskMerge applies a prepared plan to worktree files only. If a write
// fails, previously changed targets are restored from bounded in-memory copies.
// The original Git index is never opened for writing.
func ApplyTaskMerge(ctx context.Context, plan TaskMergePlan) (MergeReceipt, error) {
	if err := checkMergeRootIdentity(plan.SourceRoot); err != nil {
		return MergeReceipt{}, err
	}
	if err := checkTaskMergeTargets(ctx, plan); err != nil {
		return MergeReceipt{}, err
	}
	root, err := os.OpenRoot(plan.SourceRoot)
	if err != nil {
		return MergeReceipt{}, err
	}
	defer root.Close()
	if err := checkOpenedMergeRoot(plan.SourceRoot, root); err != nil {
		return MergeReceipt{}, err
	}
	backups := make([]mergeBackup, 0, len(plan.Changes))
	for _, c := range plan.Changes {
		target := filepath.Join(plan.SourceRoot, filepath.FromSlash(c.Path))
		b := mergeBackup{path: target, kind: "absent"}
		info, err := root.Lstat(c.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return MergeReceipt{}, err
		}
		if err == nil {
			b.mode = info.Mode()
			if info.Mode()&os.ModeSymlink != 0 {
				b.kind = "symlink"
				link, e := rootedReadlink(root, c.Path)
				if e != nil {
					return MergeReceipt{}, e
				}
				b.data = []byte(link)
			}
			if info.Mode().IsRegular() {
				b.kind = "file"
				b.data, err = rootedReadFile(root, c.Path)
				if err != nil {
					return MergeReceipt{}, err
				}
			}
			if b.kind == "absent" {
				return MergeReceipt{}, fmt.Errorf("unsupported target type %q", c.Path)
			}
		}
		backups = append(backups, b)
	}
	applied := 0
	rollback := func(cause error) (MergeReceipt, error) {
		var restoration []string
		for i := applied - 1; i >= 0; i-- {
			if err := restoreMergeBackupIfUnchanged(root, plan.SourceRoot, backups[i], plan.Changes[i].After); err != nil {
				restoration = append(restoration, err.Error())
			}
		}
		if len(restoration) > 0 {
			return MergeReceipt{}, fmt.Errorf("task merge failed: %v; rollback incomplete: %s", cause, strings.Join(restoration, "; "))
		}
		return MergeReceipt{}, cause
	}
	for i, c := range plan.Changes {
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		// Repeat checks for all remaining targets immediately before each write.
		// Already-written targets are checked against their expected result below.
		if err := checkPendingMergeTargets(ctx, plan, i); err != nil {
			return rollback(err)
		}
		if err := checkOpenedMergeRoot(plan.SourceRoot, root); err != nil {
			return rollback(err)
		}
		if err := writeMergeTarget(root, c.Path, c); err != nil {
			return rollback(err)
		}
		applied++
	}
	receipt := MergeReceipt{HeadTree: plan.HeadTree, Before: map[string]SnapshotPath{}, After: map[string]SnapshotPath{}, AppliedAt: time.Now().UTC()}
	for _, c := range plan.Changes {
		receipt.Paths = append(receipt.Paths, c.Path)
		receipt.Before[c.Path], receipt.After[c.Path] = c.Before, c.After
		got, err := readMergePathRoot(root, c.Path)
		if err != nil || got != c.After {
			return rollback(fmt.Errorf("task merge verification failed at %q: %v", c.Path, err))
		}
	}
	return receipt, nil
}

func readMergePathRoot(root *os.Root, rel string) (SnapshotPath, error) {
	info, err := root.Lstat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return SnapshotPath{Kind: "absent"}, nil
	}
	if err != nil {
		return SnapshotPath{}, err
	}
	p := SnapshotPath{}
	var data []byte
	if info.Mode()&os.ModeSymlink != 0 {
		p.Kind, p.Mode = "symlink", 0120000
		link, err := rootedReadlink(root, rel)
		if err != nil {
			return p, err
		}
		data = []byte(link)
	} else if info.Mode().IsRegular() {
		p.Kind, p.Mode = "file", 0100644
		if info.Mode()&0111 != 0 {
			p.Mode = 0100755
		}
		data, err = rootedReadFile(root, rel)
		if err != nil {
			return p, err
		}
	} else {
		return p, fmt.Errorf("unsupported target type %q", rel)
	}
	h := sha256.Sum256(data)
	p.SHA256 = hex.EncodeToString(h[:])
	return p, nil
}

func writeMergeTarget(root *os.Root, rel string, c TaskMergeChange) error {
	if c.After.Kind == "absent" {
		before := c.Before
		if before.Kind == "" {
			before.Kind = "absent"
		}
		current, err := readMergePathRoot(root, rel)
		if err != nil || current != before {
			return fmt.Errorf("%w: target %q changed before merge deletion", ErrParentChanged, rel)
		}
		if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	parent := filepath.Dir(rel)
	if err := rootedMkdirAll(root, parent, 0755); err != nil {
		return err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := filepath.Join(parent, ".umcode-merge-"+hex.EncodeToString(nonce[:]))
	defer root.Remove(name)
	if c.After.Kind == "symlink" {
		if err := rootedSymlink(root, string(c.data), name); err != nil {
			return err
		}
	} else {
		mode := os.FileMode(0644)
		if c.After.Mode == 0100755 {
			mode = 0755
		}
		tmp, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := tmp.Write(c.data); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Chmod(mode); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
	}
	// Staging the new content may take time; recheck the visible target just
	// before replacing it. This catches edits made after the plan-wide check.
	before := c.Before
	if before.Kind == "" {
		before.Kind = "absent"
	}
	current, err := readMergePathRoot(root, rel)
	if err != nil || current != before {
		return fmt.Errorf("%w: target %q changed while staging merge content", ErrParentChanged, rel)
	}
	if err := rootedRename(root, name, rel); err != nil {
		return err
	}
	return nil
}

func restoreMergeBackupIfUnchanged(root *os.Root, sourceRoot string, b mergeBackup, after SnapshotPath) error {
	rel, err := filepath.Rel(sourceRoot, b.path)
	if err != nil {
		return err
	}
	current, err := readMergePathRoot(root, rel)
	if err != nil || current != after {
		return fmt.Errorf("%w: target %q changed after merge write; current content preserved", ErrParentChanged, rel)
	}
	if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if b.kind == "absent" {
		return nil
	}
	if err := rootedMkdirAll(root, filepath.Dir(rel), 0755); err != nil {
		return err
	}
	if b.kind == "symlink" {
		return rootedSymlink(root, string(b.data), rel)
	}
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, b.mode.Perm())
	if err != nil {
		return err
	}
	if _, err := f.Write(b.data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(b.mode.Perm()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// MergeTaskResult is the standalone convenience for callers that do not need
// a persistent journal. Engine completion uses Prepare and Apply separately.
func MergeTaskResult(ctx context.Context, source string, snapshot TaskSnapshot, headTree string) (MergeReceipt, error) {
	plan, err := PrepareTaskMerge(ctx, source, snapshot, headTree)
	if err != nil {
		return MergeReceipt{}, err
	}
	return ApplyTaskMerge(ctx, plan)
}

// InspectTaskMerge reads a journaled plan after a restart. It never writes.
// Mixed or changed states are ambiguous and require a human review.
func InspectTaskMerge(ctx context.Context, plan TaskMergePlan) string {
	if checkMergeRootIdentity(plan.SourceRoot) != nil {
		return "ambiguous"
	}
	current, err := CaptureSourceManifest(ctx, plan.SourceRoot)
	if err != nil || current.Head != plan.Snapshot.Manifest.Head {
		return "ambiguous"
	}
	root, err := os.OpenRoot(plan.SourceRoot)
	if err != nil {
		return "ambiguous"
	}
	defer root.Close()
	if checkOpenedMergeRoot(plan.SourceRoot, root) != nil {
		return "ambiguous"
	}
	before, after := true, true
	for _, c := range plan.Changes {
		if err := checkTargetPath(plan.SourceRoot, c.Path); err != nil {
			return "ambiguous"
		}
		got, err := readMergePathRoot(root, c.Path)
		if err != nil || current.IndexPaths[c.Path] != plan.Snapshot.Manifest.IndexPaths[c.Path] {
			return "ambiguous"
		}
		before = before && got == c.Before
		after = after && got == c.After
	}
	if after {
		return "after"
	}
	if before {
		return "before"
	}
	return "ambiguous"
}
