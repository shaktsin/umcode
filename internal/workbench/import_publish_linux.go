//go:build linux

package workbench

import (
	"golang.org/x/sys/unix"
	"os"
)

func publishImportedDirectory(root *os.Root, from, to string) error {
	pinned, err := openDataDirectory(root, from, false)
	if err != nil {
		return err
	}
	defer pinned.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.Renameat2(int(dir.Fd()), from, int(dir.Fd()), to, unix.RENAME_NOREPLACE)
}
