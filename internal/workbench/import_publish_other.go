//go:build !darwin && !linux

package workbench

import (
	"errors"
	"os"
)

func publishImportedDirectory(_ *os.Root, _, _ string) error {
	return errors.New("atomic model-folder import is currently supported on macOS and Linux")
}
