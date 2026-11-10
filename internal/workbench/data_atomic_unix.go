//go:build darwin || linux

package workbench

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

// Go 1.24 has no os.Root.Rename. Pin the parent descriptor for renameat.
func dataAtomicRename(root *os.Root, from, to string) error {
	if strings.ContainsAny(from+to, "/\\") {
		return errors.New("atomic data names must be basenames")
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := root.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return errors.New("atomic data source must be a regular file or real directory")
	}
	fd, err := unix.Openat(int(dir.Fd()), from, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	source := os.NewFile(uintptr(fd), from)
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("atomic data source changed while opening")
	}
	current, err := root.Lstat(from)
	if err != nil || !os.SameFile(info, current) || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("atomic data source changed before publication")
	}
	if err := unix.Renameat(int(dir.Fd()), from, int(dir.Fd()), to); err != nil {
		return err
	}
	// Once rename succeeds the published pointer is committed. Directory-sync
	// failures cannot be reported as a failed operation that preserved old data.
	_ = dir.Sync()
	return nil
}
