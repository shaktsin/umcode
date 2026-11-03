//go:build !darwin && !linux

package workbench

import (
	"errors"
	"os"
	"path/filepath"
)

func renameModelToTrash(models, _ *os.Root, id, name string) error {
	// Root.Rename was added after the minimum supported Go version. Fail closed
	// when descriptor-rooted renames are unavailable on this platform.
	if root, ok := any(models).(interface{ Rename(string, string) error }); ok {
		return root.Rename(id, filepath.Join(".trash", name))
	}
	return errors.New("moving models to Trash requires a newer Go runtime on this platform")
}
