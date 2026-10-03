// Package fingerprint summarizes the state of a project workspace and of the
// environment a check ran in, so evidence can be marked stale when either
// changes. Everything here is read-only, bounded in time and best-effort.
package fingerprint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// MaxManifestFiles caps the non-git manifest walk.
const MaxManifestFiles = 5000

// maxHashBytes is the largest file whose content is hashed; larger files are
// identified by size and modification time.
const maxHashBytes = 1 << 20

// budget bounds one fingerprint. Tests shrink it.
var budget = 2 * time.Second

var ignoredDirs = map[string]bool{".git": true, "node_modules": true}

// Workspace is the observed state of a project folder.
type Workspace struct {
	Value string   // hash of the whole state; empty when unavailable
	Paths []string // git projects: paths that differ from HEAD; otherwise empty

	files map[string]string // path -> identity, used by Diff
}

// TakeWorkspace fingerprints root. The second result is false when root is
// empty, the budget or context expired, or an error occurred; callers then
// invalidate nothing.
func TakeWorkspace(ctx context.Context, root, vaultDir string) (Workspace, bool) {
	if root == "" {
		return Workspace{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if ctx.Err() != nil {
		return Workspace{}, false
	}
	var w Workspace
	var err error
	if head, ok := gitHead(ctx, root); ok {
		w, err = gitWorkspace(ctx, root, vaultDir, head)
	} else {
		w, err = manifestWorkspace(ctx, root, vaultDir)
	}
	if err != nil || ctx.Err() != nil {
		return Workspace{}, false
	}
	return w, true
}

// Diff lists the paths whose identity differs between two fingerprints.
func Diff(before, after Workspace) []string {
	seen := map[string]bool{}
	var out []string
	for p, id := range after.files {
		if before.files[p] != id {
			seen[p] = true
			out = append(out, p)
		}
	}
	for p := range before.files {
		if _, ok := after.files[p]; !ok && !seen[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root, "-c", "core.quotepath=off"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd.Output()
}

// gitHead returns HEAD when root is the top level of a git work tree.
func gitHead(ctx context.Context, root string) (string, bool) {
	top, err := git(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	a, _ := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	b, _ := filepath.EvalSymlinks(root)
	if a == "" || a != b {
		return "", false
	}
	head, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "no-head", true // a repository without commits
	}
	return strings.TrimSpace(string(head)), true
}

func gitWorkspace(ctx context.Context, root, vaultDir, head string) (Workspace, error) {
	out, err := git(ctx, root, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return Workspace{}, err
	}
	files := map[string]string{}
	toks := bytes.Split(out, []byte{0})
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 {
			continue
		}
		xy, path := string(t[:2]), string(t[3:])
		if strings.ContainsAny(xy, "RC") {
			i++ // the original path of a rename or copy follows
		}
		if inside(root, vaultDir, path) {
			continue
		}
		files[path] = fileIdentity(filepath.Join(root, path))
	}
	return finish(head, files, true), ctx.Err()
}

func manifestWorkspace(ctx context.Context, root, vaultDir string) (Workspace, error) {
	files := map[string]string{}
	vault := ""
	if vaultDir != "" {
		vault, _ = filepath.Abs(vaultDir)
	}
	stop := fmt.Errorf("manifest cap reached")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if p != root && (ignoredDirs[d.Name()] || (vault != "" && p == vault)) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		files[filepath.ToSlash(rel)] = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
		if len(files) >= MaxManifestFiles {
			return stop
		}
		return nil
	})
	if err != nil && err != stop {
		return Workspace{}, err
	}
	return finish("", files, false), nil
}

func inside(root, vaultDir, rel string) bool {
	if vaultDir == "" {
		return false
	}
	v, err := filepath.Rel(root, vaultDir)
	if err != nil || strings.HasPrefix(v, "..") {
		return false
	}
	return rel == v || strings.HasPrefix(filepath.ToSlash(rel), filepath.ToSlash(v)+"/")
}

// fileIdentity identifies a file's content cheaply; a missing file is "deleted".
func fileIdentity(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "deleted"
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return "link:" + target
	}
	if info.IsDir() {
		return "dir"
	}
	if info.Size() > maxHashBytes {
		return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	f, err := os.Open(path)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))
}

func finish(head string, files map[string]string, listPaths bool) Workspace {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "head=%s\n", head)
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\n", k, files[k])
	}
	w := Workspace{Value: hex.EncodeToString(h.Sum(nil)), files: files}
	if listPaths {
		w.Paths = keys
	}
	return w
}

// Environment returns a short hash of what a check's result depends on besides
// the files: OS and architecture, the git HEAD of the project, and whatever
// tool identity the caller supplies (for example a compiler version).
func Environment(ctx context.Context, root string, tool func(ctx context.Context) string) string {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	h := sha256.New()
	fmt.Fprintf(h, "%s/%s\n", runtime.GOOS, runtime.GOARCH)
	if root != "" {
		if head, ok := gitHead(ctx, root); ok {
			fmt.Fprintf(h, "head=%s\n", head)
		}
	}
	if tool != nil {
		fmt.Fprintf(h, "tool=%s\n", tool(ctx))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
