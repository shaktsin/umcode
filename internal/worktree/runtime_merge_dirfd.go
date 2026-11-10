package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func rootedReadFile(root *os.Root, rel string) ([]byte, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func rootedMkdirAll(root *os.Root, rel string, mode os.FileMode) error {
	if rel == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := root.Mkdir(current, mode); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr := root.Stat(current)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return fmt.Errorf("merge parent %q is not a directory", current)
			}
		}
	}
	return nil
}

// mergeParentFD resolves a path beneath the owned root and pins its parent
// directory. This blocks symlink replacement along the resolved path. A
// concurrent move of the pinned directory itself still changes its location;
// callers recheck the selected root before each write.
func mergeParentFD(root *os.Root, rel string) (*os.Root, *os.File, string, error) {
	parent, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return nil, nil, "", err
	}
	dir, err := parent.Open(".")
	if err != nil {
		_ = parent.Close()
		return nil, nil, "", err
	}
	return parent, dir, filepath.Base(rel), nil
}

func rootedReadlink(root *os.Root, rel string) (string, error) {
	parent, dir, name, err := mergeParentFD(root, rel)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	defer dir.Close()
	buf := make([]byte, 64<<10)
	n, err := unix.Readlinkat(int(dir.Fd()), name, buf)
	if err != nil {
		return "", err
	}
	if n == len(buf) {
		return "", fmt.Errorf("merge symlink %q is too long", rel)
	}
	return string(buf[:n]), nil
}

func rootedSymlink(root *os.Root, target, rel string) error {
	parent, dir, name, err := mergeParentFD(root, rel)
	if err != nil {
		return err
	}
	defer parent.Close()
	defer dir.Close()
	return unix.Symlinkat(target, int(dir.Fd()), name)
}

func rootedRename(root *os.Root, from, to string) error {
	fromParent, fromDir, fromName, err := mergeParentFD(root, from)
	if err != nil {
		return err
	}
	defer fromParent.Close()
	defer fromDir.Close()
	toParent, toDir, toName, err := mergeParentFD(root, to)
	if err != nil {
		return err
	}
	defer toParent.Close()
	defer toDir.Close()
	return unix.Renameat(int(fromDir.Fd()), fromName, int(toDir.Fd()), toName)
}
