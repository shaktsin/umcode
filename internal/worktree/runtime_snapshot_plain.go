package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// capturePlainManifest records an ordinary project without introducing Git
// metadata into the user's folder. The private task workspace owns its Git.
func capturePlainManifest(root string) (SnapshotManifest, error) {
	m := SnapshotManifest{IndexPaths: map[string]string{}, Paths: map[string]SnapshotPath{}, Untracked: []string{}}
	err := filepath.WalkDir(root, func(abs string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if abs == root {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isDotGit(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := validateMergePath(rel); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		p := SnapshotPath{}
		var data []byte
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			resolved, err := filepath.EvalSymlinks(abs)
			if err != nil {
				return fmt.Errorf("project symlink %q: %w", rel, err)
			}
			inside, err := filepath.Rel(root, resolved)
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("project symlink %q escapes the selected folder", rel)
			}
			link, err := os.Readlink(abs)
			if err != nil {
				return err
			}
			data = []byte(link)
			p.Kind, p.Mode = "symlink", 0120000
		case info.Mode().IsRegular():
			data, err = os.ReadFile(abs)
			if err != nil {
				return err
			}
			p.Kind, p.Mode = "file", 0100644
			if info.Mode()&0111 != 0 {
				p.Mode = 0100755
			}
		default:
			return fmt.Errorf("task snapshot refuses special file %q", rel)
		}
		if len(data) > 64<<20 {
			return fmt.Errorf("task snapshot file %q exceeds 64 MiB", rel)
		}
		digest := sha256.Sum256(data)
		p.SHA256 = hex.EncodeToString(digest[:])
		m.Paths[rel] = p
		m.Untracked = append(m.Untracked, rel)
		return nil
	})
	if err != nil {
		return SnapshotManifest{}, err
	}
	sort.Strings(m.Untracked)
	if _, err := snapshotSize(root, m.Untracked); err != nil {
		return SnapshotManifest{}, err
	}
	return m, nil
}
