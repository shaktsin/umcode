package workbench

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func publishImportedCheckpoint(b BundleTarget, output, id string) error {
	if !safeDataPath(id) || strings.Contains(id, "/") {
		return errors.New("invalid checkpoint identity")
	}
	// The worker never receives a writable path inside the user's model.
	model, err := b.model()
	if err != nil {
		return err
	}
	defer model.Close()
	checkpoints, err := openDataDirectory(model, "import-checkpoints", true)
	if err != nil {
		return err
	}
	defer checkpoints.Close()
	source, err := os.OpenRoot(output)
	if err != nil {
		return err
	}
	defer source.Close()
	entries, err := fs.ReadDir(source.FS(), ".")
	if err != nil {
		return err
	}
	manifest := bundleManifest{}
	for _, entry := range entries {
		if BundleFileKind(entry.Name()) == "" {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("checkpoint contains a nonregular file")
		}
		manifest.Files = append(manifest.Files, ImportSourceFile{Name: entry.Name(), Size: info.Size()})
	}
	if err = validateBundleManifest(manifest.Files); err != nil {
		return err
	}
	stageName, stage, err := createDataStage(checkpoints, "checkpoint-stage-")
	if err != nil {
		return err
	}
	defer stage.Close()
	published := false
	defer func() {
		if !published {
			discardDataStage(checkpoints, stageName, stage)
		}
	}()
	for _, file := range manifest.Files {
		input, e := openDataRegularFile(source, file.Name)
		if e != nil {
			return e
		}
		output, e := stage.OpenFile(file.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			input.Close()
			return e
		}
		n, e := io.Copy(output, io.LimitReader(input, file.Size+1))
		input.Close()
		syncErr := output.Sync()
		output.Close()
		if e != nil {
			return e
		}
		if syncErr != nil {
			return syncErr
		}
		if n != file.Size {
			return errors.New("checkpoint changed while saving")
		}
	}
	_, manifest.Digests, err = bundleMetadata(stage, manifest)
	if err != nil {
		return err
	}
	if err = writeNewDataJSON(stage, "manifest.json", manifest); err != nil {
		return err
	}
	// Recheck identity after the copy; a deleted/recreated model gets nothing.
	current, err := b.model()
	if err != nil {
		return err
	}
	current.Close()
	err = publishImportedDirectory(checkpoints, stageName, id)
	published = err == nil
	return err
}

func verifiedImportedCheckpoint(b BundleTarget, id string) (string, error) {
	if !safeDataPath(id) || strings.Contains(id, "/") {
		return "", errors.New("invalid checkpoint identity")
	}
	model, err := b.model()
	if err != nil {
		return "", err
	}
	defer model.Close()
	parent, err := openDataDirectory(model, "import-checkpoints", false)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	folder, err := openDataDirectory(parent, id, false)
	if err != nil {
		return "", err
	}
	defer folder.Close()
	var manifest bundleManifest
	if err = readDataJSON(folder, "manifest.json", 64<<10, &manifest); err != nil {
		return "", err
	}
	if err = validateBundleManifest(manifest.Files); err != nil {
		return "", err
	}
	_, digests, err := bundleMetadata(folder, manifest)
	if err != nil {
		return "", err
	}
	if !importEqual(digests, manifest.Digests) {
		return "", errors.New("Saved checkpoint files changed; train a new checkpoint.")
	}
	return filepath.Join(b.ProjectRoot, "models", b.ModelID, "import-checkpoints", id), nil
}
