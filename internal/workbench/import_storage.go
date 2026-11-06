package workbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const maxImportedGraphBytes = 32 << 20
const ImportedRuntimeMessage = "This imported architecture supports layer inspection and editing in Build. Its pretrained weights are not loaded by the current Workbench training/inference runner."

func IsImportedGraph(raw []byte) bool {
	var head struct {
		Format string `json:"format"`
	}
	return json.Unmarshal(raw, &head) == nil && head.Format == "umcode.imported.v1"
}
func importedSourceJSON(raw []byte) ([]byte, error) {
	var graph map[string]json.RawMessage
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, err
	}
	source, ok := graph["source"]
	if !ok {
		return nil, errors.New("imported source is required")
	}
	d := json.NewDecoder(bytes.NewReader(source))
	d.UseNumber()
	var obj any
	if err := d.Decode(&obj); err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

// SameImportedSource compares normalized immutable provenance, including the
// baseline and tensor bindings, without accepting changes to the saved source.
func SameImportedSource(a, b []byte) bool {
	x, err := importedSourceJSON(a)
	if err != nil {
		return false
	}
	y, err := importedSourceJSON(b)
	return err == nil && bytes.Equal(x, y)
}
func verifyImportedSource(projectRoot, dir string, raw []byte) error {
	original, err := readBundleFile(projectRoot, dir, "import-source.json", maxImportedGraphBytes)
	if err != nil {
		return fmt.Errorf("read original imported source: %w", err)
	}
	current, err := importedSourceJSON(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(original), current) {
		return errors.New("the original imported model metadata and tensor bindings cannot be changed; import another model to replace the source")
	}
	return nil
}

// CreateImported atomically publishes a new metadata-only editable model.
// No source files are opened or modified and no weight payload is copied.
func CreateImported(projectRoot, name string, raw json.RawMessage) (OpenResult, error) {
	if err := ValidateImportedGraph(raw); err != nil {
		return OpenResult{}, err
	}
	var graph ImportedGraph
	if err := json.Unmarshal(raw, &graph); err != nil {
		return OpenResult{}, err
	}
	family, supported := importFamily(graph.Source.Config)
	if !supported {
		return OpenResult{}, errors.New("this architecture family is available for tensor inspection only")
	}
	name = strings.TrimSpace(name)
	id := slug(name)
	if name == "" || len(name) > 100 || !validID.MatchString(id) {
		return OpenResult{}, errors.New("model name must be 1 to 100 characters and produce a valid local model name")
	}
	if _, err := modelRoot(projectRoot, true); err != nil {
		return OpenResult{}, err
	}
	project, err := os.OpenRoot(projectRoot)
	if err != nil {
		return OpenResult{}, err
	}
	defer project.Close()
	models, err := openDataDirectory(project, "models", false)
	if err != nil {
		return OpenResult{}, err
	}
	defer models.Close()
	if _, err = models.Lstat(id); err == nil {
		return OpenResult{}, ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return OpenResult{}, err
	}
	task := "classification"
	switch family {
	case "gpt2", "llama", "mistral", "qwen2":
		task = "text-generation"
	}
	model := Model{SchemaVersion: 1, ID: id, Name: name, Task: task, CreatedAt: time.Now().UTC()}
	stageName, stage, err := createDataStage(models, ".import-")
	if err != nil {
		return OpenResult{}, err
	}
	defer stage.Close()
	published := false
	defer func() {
		if !published {
			discardDataStage(models, stageName, stage)
		}
	}()
	source, err := importedSourceJSON(raw)
	if err != nil {
		return OpenResult{}, err
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, raw); err != nil {
		return OpenResult{}, err
	}
	if err = writeNewDataJSON(stage, "model.json", model); err != nil {
		return OpenResult{}, err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"graph.json", compact.Bytes()}, {"import-source.json", source}, {"model.py", []byte("# Imported architecture metadata. Pretrained execution is not available in this runner.\n")}, {"adapter.py", []byte("# Imported source weights remain in the original model folder.\n")}} {
		if err = writeNewDataFile(stage, file.name, file.data); err != nil {
			return OpenResult{}, err
		}
	}
	if err = stage.Mkdir("runs", 0700); err != nil {
		return OpenResult{}, err
	}
	if err = publishImportedDirectory(models, stageName, id); err != nil {
		if errors.Is(err, os.ErrExist) {
			return OpenResult{}, ErrExists
		}
		return OpenResult{}, err
	}
	published = true
	return OpenResult{Model: model, Graph: append(json.RawMessage(nil), compact.Bytes()...)}, nil
}
