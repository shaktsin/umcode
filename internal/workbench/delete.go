package workbench

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Delete moves a complete model bundle to project-local Trash. It never follows
// model contents or removes data, and returns a project-relative recovery path.
func Delete(projectRoot, id string) (string, error) {
	if !validID.MatchString(id) {
		return "", errors.New("invalid model id")
	}
	if _, err := modelRoot(projectRoot, false); err != nil {
		return "", err
	}
	project, err := os.OpenRoot(projectRoot)
	if err != nil {
		return "", err
	}
	defer project.Close()
	models, err := openModelDirectory(project, "models")
	if err != nil {
		return "", err
	}
	defer models.Close()
	bundle, err := openModelDirectory(models, id)
	if err != nil {
		return "", err
	}
	defer bundle.Close()
	if err := models.Mkdir(".trash", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	trash, err := openModelDirectory(models, ".trash")
	if err != nil {
		return "", err
	}
	defer trash.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	name := id + "-" + hex.EncodeToString(nonce[:])
	if err := renameModelToTrash(models, trash, id, name); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("models", ".trash", name)), nil
}

// Pin and verify every directory used for the move rather than resolving an
// absolute path again after validating it.
func openModelDirectory(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a real directory", name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	actual, statErr := f.Stat()
	f.Close()
	if statErr != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	return root, nil
}
