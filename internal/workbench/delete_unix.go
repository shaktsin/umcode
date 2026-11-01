//go:build darwin || linux

package workbench

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func renameModelToTrash(models, trash *os.Root, id, name string) error {
	from, err := models.Open(".")
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := trash.Open(".")
	if err != nil {
		return err
	}
	defer to.Close()
	fd, err := unix.Openat(int(from.Fd()), id, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	info, err := models.Lstat(id)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("model bundle must be a real directory")
	}
	return unix.Renameat(int(from.Fd()), id, int(to.Fd()), name)
}
