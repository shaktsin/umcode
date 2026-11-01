package workbench

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Serialize publication so one process never exposes half of a new import.
// Filesystem writes are rooted at pinned directory descriptors as well.
var dataMu sync.Mutex

func openDataDirectory(parent *os.Root, name string, create bool) (*os.Root, error) {
	if !safeDataPath(name) || strings.Contains(name, "/") {
		return nil, errors.New("invalid storage directory")
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		if e := parent.Mkdir(name, 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a real directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	f, err := child.Open(".")
	if err != nil {
		child.Close()
		return nil, err
	}
	opened, err := f.Stat()
	f.Close()
	if err != nil || !os.SameFile(info, opened) {
		child.Close()
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	return child, nil
}

func openDataStorage(projectRoot, modelID string, create bool) (*os.Root, Model, error) {
	if _, err := Open(projectRoot, modelID); err != nil {
		return nil, Model{}, err
	}
	project, err := os.OpenRoot(projectRoot)
	if err != nil {
		return nil, Model{}, err
	}
	defer project.Close()
	models, err := openDataDirectory(project, "models", false)
	if err != nil {
		return nil, Model{}, err
	}
	defer models.Close()
	modelRoot, err := openDataDirectory(models, modelID, false)
	if err != nil {
		return nil, Model{}, err
	}
	defer modelRoot.Close()
	var model Model
	if err := readDataJSON(modelRoot, "model.json", 16<<10, &model); err != nil {
		return nil, Model{}, err
	}
	if model.ID != modelID || model.SchemaVersion != 1 || !validateTask(model.Task) {
		return nil, Model{}, errors.New("invalid model metadata")
	}
	root, err := openDataDirectory(modelRoot, "data", create)
	return root, model, err
}

func readDataFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := openDataRegularFile(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%s exceeds size limit", name)
	}
	return content, nil
}

func openDataRegularFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		f.Close()
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	return f, nil
}

func readDataJSON(root *os.Root, name string, limit int64, value any) error {
	content, err := readDataFile(root, name, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, value); err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	return nil
}

func dataRandomName(prefix string) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(id[:]), nil
}

func writeNewDataFile(root *os.Root, name string, content []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeNewDataJSON(root *os.Root, name string, value any) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeNewDataFile(root, name, append(content, '\n'))
}

func atomicDataJSON(root *os.Root, name string, value any) error {
	// Reject an existing link even though rename itself would replace the link.
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", name)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := dataRandomName(".pointer-")
	if err != nil {
		return err
	}
	if err = writeNewDataJSON(root, tmp, value); err != nil {
		return err
	}
	defer root.Remove(tmp)
	if err = dataAtomicRename(root, tmp, name); err != nil {
		return err
	}
	return nil
}

func createDataStage(parent *os.Root, prefix string) (string, *os.Root, error) {
	name, err := dataRandomName(prefix)
	if err != nil {
		return "", nil, err
	}
	if err := parent.Mkdir(name, 0700); err != nil {
		return "", nil, err
	}
	root, err := openDataDirectory(parent, name, false)
	return name, root, err
}

// Cleanup traverses only pinned, nonsymlinked directories created by this call.
func discardDataStage(parent *os.Root, name string, stage *os.Root) {
	if stage == nil {
		return
	}
	clearDataDirectory(stage)
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	f, err := stage.Open(".")
	if err != nil {
		return
	}
	opened, err := f.Stat()
	f.Close()
	if err == nil && os.SameFile(info, opened) {
		_ = parent.Remove(name)
	}
}

func clearDataDirectory(root *os.Root) {
	f, err := root.Open(".")
	if err != nil {
		return
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			child, err := openDataDirectory(root, entry.Name(), false)
			if err == nil {
				clearDataDirectory(child)
				child.Close()
				_ = root.Remove(entry.Name())
			}
		} else {
			_ = root.Remove(entry.Name())
		}
	}
}

func writeDataRawFiles(root *os.Root, files []decodedDataFile) error {
	raw, err := openDataDirectory(root, "raw", true)
	if err != nil {
		return err
	}
	defer raw.Close()
	for _, file := range files {
		parent := raw
		opened := []*os.Root{}
		parts := strings.Split(file.name, "/")
		for _, part := range parts[:len(parts)-1] {
			next, e := openDataDirectory(parent, part, true)
			if e != nil {
				for _, dir := range opened {
					dir.Close()
				}
				return e
			}
			opened = append(opened, next)
			parent = next
		}
		err = writeNewDataFile(parent, parts[len(parts)-1], file.content)
		for _, dir := range opened {
			dir.Close()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeDataRows(root *os.Root, names []string, rows []dataRecord) error {
	f, err := root.OpenFile("rows.jsonl", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	values := make([]string, len(names))
	for _, row := range rows {
		for i, name := range names {
			values[i] = row[name]
		}
		if err := enc.Encode(values); err != nil {
			return err
		}
	}
	return f.Sync()
}

func readDataRows(root *os.Root, ds Dataset) ([]dataRecord, error) {
	f, err := openDataRegularFile(root, "rows.jsonl")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	limited := &io.LimitedReader{R: f, N: maxDataRowsJSONBytes + 1}
	dec := json.NewDecoder(limited)
	rows := make([]dataRecord, 0, ds.RowCount)
	for {
		var values []string
		err := dec.Decode(&values)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(values) != len(ds.Columns) || len(rows) >= maxDataRows {
			return nil, errors.New("invalid stored dataset rows")
		}
		row := dataRecord{}
		for i, col := range ds.Columns {
			row[col.Name] = values[i]
		}
		rows = append(rows, row)
	}
	if limited.N <= 0 || len(rows) != ds.RowCount {
		return nil, errors.New("stored dataset rows do not match metadata")
	}
	return rows, nil
}

func readDataset(parent *os.Root, id string) (Dataset, error) {
	if !dataDigest.MatchString(id) {
		return Dataset{}, errors.New("invalid dataset id")
	}
	root, err := openDataDirectory(parent, id, false)
	if err != nil {
		return Dataset{}, err
	}
	defer root.Close()
	var ds Dataset
	if err := readDataJSON(root, "metadata.json", maxDataMetadataBytes, &ds); err != nil {
		return Dataset{}, err
	}
	if ds.ID != id || ds.RowCount < 1 || ds.RowCount > maxDataRows || len(ds.Columns) < 1 || len(ds.Columns) > maxDataColumns || ds.ByteCount < 1 || ds.ByteCount > maxDataBytes || len(ds.Preview) > 20 {
		return Dataset{}, errors.New("invalid dataset metadata")
	}
	return ds, nil
}
