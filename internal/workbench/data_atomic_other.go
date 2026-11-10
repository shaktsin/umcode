//go:build !darwin && !linux

package workbench

import (
	"errors"
	"os"
	"strings"
)

func dataAtomicRename(root *os.Root, from, to string) error {
	if strings.ContainsAny(from+to, "/\\") {
		return errors.New("atomic data names must be basenames")
	}
	info, err := root.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return errors.New("atomic data source must be a regular file or real directory")
	}
	source, err := root.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("atomic data source changed while opening")
	}
	current, err := root.Lstat(from)
	if err != nil || !os.SameFile(info, current) || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("atomic data source changed before publication")
	}
	// Newer Go versions provide this operation on other supported platforms.
	if renamer, ok := any(root).(interface{ Rename(string, string) error }); ok {
		return renamer.Rename(from, to)
	}
	return errors.New("safe dataset publication requires macOS, Linux, or Go 1.25 or later")
}
