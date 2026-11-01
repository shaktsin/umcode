// Package workbench persists local-first ML model bundles in a UMCode project.
package workbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxGraphBytes = 1 << 20

var validID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

var ErrExists = errors.New("model already exists")

type Model struct {
	SchemaVersion int       `json:"schemaVersion"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Task          string    `json:"task"`
	CreatedAt     time.Time `json:"createdAt"`
}

type OpenResult struct {
	Model Model           `json:"model"`
	Graph json.RawMessage `json:"graph"`
}

func slug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func validateTask(task string) bool {
	return task == "text-generation" || task == "classification" || task == "custom"
}

// modelRoot refuses symlinked models directories, preventing model writes
// outside of the registered project folder.
func modelRoot(projectRoot string, create bool) (string, error) {
	if projectRoot == "" {
		return "", errors.New("project root is required")
	}
	base := filepath.Join(projectRoot, "models")
	info, err := os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return base, nil
		}
		if err := os.Mkdir(base, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err = os.Lstat(base)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("models path must be a real directory")
	}
	return base, nil
}

func bundleDir(projectRoot, id string) (string, error) {
	if !validID.MatchString(id) {
		return "", errors.New("invalid model id")
	}
	base, err := modelRoot(projectRoot, false)
	if err != nil {
		return "", err
	}
	p := filepath.Join(base, id)
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("model bundle must be a real directory")
	}
	return p, nil
}

func writeJSON(path string, value any) error {
	buf, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(buf, '\n'), 0644)
}

// Create allocates a unique model directory and initial P0 scaffolding.
// Starter graphs and trainable Python are intentionally deferred to P1.
func Create(projectRoot, name, task string) (model Model, err error) {
	name = strings.TrimSpace(name)
	id := slug(name)
	if len(name) == 0 || len(name) > 100 || !validID.MatchString(id) || !validateTask(task) {
		return Model{}, errors.New("name or task is invalid")
	}
	base, err := modelRoot(projectRoot, true)
	if err != nil {
		return Model{}, err
	}
	dir := filepath.Join(base, id)
	if err = os.Mkdir(dir, 0755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Model{}, ErrExists
		}
		return Model{}, err
	}
	// Only remove a directory created by this call. Never overwrite bundles.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	model = Model{SchemaVersion: 1, ID: id, Name: name, Task: task, CreatedAt: time.Now().UTC()}
	if err = writeJSON(filepath.Join(dir, "model.json"), model); err != nil {
		return Model{}, err
	}
	if err = writeJSON(filepath.Join(dir, "graph.json"), map[string]any{"version": 1, "nodes": []any{}, "edges": []any{}}); err != nil {
		return Model{}, err
	}
	adapter := fmt.Sprintf("# ML Workbench adapter skeleton; implement in P3.\nTASK = %q\nINPUT_TYPE = \"text\"\nOUTPUT_TYPE = \"text\"\n", task)
	if task == "classification" {
		adapter = fmt.Sprintf("# ML Workbench adapter skeleton; implement in P3.\nTASK = %q\nINPUT_TYPE = \"text\"\nOUTPUT_TYPE = \"label\"\n", task)
	} else if task == "custom" {
		adapter = "# Custom adapter skeleton. Declare input/output types before implementing inference.\nTASK = \"custom\"\nINPUT_TYPE = None\nOUTPUT_TYPE = None\n"
	}
	if err = os.WriteFile(filepath.Join(dir, "adapter.py"), []byte(adapter), 0644); err != nil {
		return Model{}, err
	}
	if err = os.WriteFile(filepath.Join(dir, "model.py"), []byte("# Model code will be generated from graph.json in P1.\n"), 0644); err != nil {
		return Model{}, err
	}
	if err = os.Mkdir(filepath.Join(dir, "runs"), 0755); err != nil {
		return Model{}, err
	}
	return model, nil
}

// List returns only well-formed bundles; corrupt or unexpected entries fail
// explicitly instead of being silently omitted from the user's workspace.
func List(projectRoot string) ([]Model, error) {
	base, err := modelRoot(projectRoot, false)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return []Model{}, nil
	}
	if err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validID.MatchString(entry.Name()) {
			continue
		}
		open, err := Open(projectRoot, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read model %q: %w", entry.Name(), err)
		}
		models = append(models, open.Model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].CreatedAt.After(models[j].CreatedAt) })
	return models, nil
}

func Open(projectRoot, id string) (OpenResult, error) {
	dir, err := bundleDir(projectRoot, id)
	if err != nil {
		return OpenResult{}, err
	}
	meta, err := readBundleFile(projectRoot, dir, "model.json", 16<<10)
	if err != nil {
		return OpenResult{}, err
	}
	var model Model
	if err := json.Unmarshal(meta, &model); err != nil {
		return OpenResult{}, err
	}
	if model.ID != id || model.SchemaVersion != 1 || !validateTask(model.Task) {
		return OpenResult{}, errors.New("invalid model metadata")
	}
	graph, err := readBundleFile(projectRoot, dir, "graph.json", maxGraphBytes)
	if err != nil {
		return OpenResult{}, err
	}
	if err := validateGraph(graph); err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Model: model, Graph: graph}, nil
}

// Resolve through the project root so a concurrent directory replacement cannot
// redirect a read outside the project. Bound reads before allocating large files.
func readBundleFile(projectRoot, dir, name string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(projectRoot, filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", name)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	return data, nil
}

func validateGraph(data []byte) error {
	if len(data) == 0 || len(data) > maxGraphBytes {
		return errors.New("graph must be 1 byte to 1 MiB")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return errors.New("graph must be a JSON object")
	}
	for _, key := range []string{"version", "nodes", "edges"} {
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("graph missing %s", key)
		}
	}
	var version int
	var nodes, edges []json.RawMessage
	if json.Unmarshal(obj["version"], &version) != nil || version != 1 || json.Unmarshal(obj["nodes"], &nodes) != nil || json.Unmarshal(obj["edges"], &edges) != nil || nodes == nil || edges == nil {
		return errors.New("invalid graph version or node/edge arrays")
	}
	return nil
}

// SaveGraph makes one atomic replacement; the model task cannot change.
func SaveGraph(projectRoot, id string, graph json.RawMessage) error {
	if err := validateGraph(graph); err != nil {
		return err
	}
	dir, err := bundleDir(projectRoot, id)
	if err != nil {
		return err
	}
	if _, err := Open(projectRoot, id); err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, graph, "", "  "); err != nil {
		return err
	}
	pretty.WriteByte('\n')
	if pretty.Len() > maxGraphBytes {
		return errors.New("formatted graph exceeds 1 MiB")
	}
	f, err := os.CreateTemp(dir, ".graph-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(pretty.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "graph.json"))
}
