package workbench

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxCheckpointBytes = 64 << 20

var checkpointMu sync.Mutex

type CheckpointValidation struct {
	Input          CheckpointInput `json:"input"`
	ChatCompatible bool            `json:"chatCompatible"`
	Device         string          `json:"device"`
}
type CheckpointSnapshot struct {
	Checkpoint Checkpoint
	Graph      json.RawMessage
	Selection  DataSelection
	Weights    []byte
}
type CheckpointValidator func(json.RawMessage, DataSelection, []byte, string) (CheckpointValidation, error)

func openCheckpointModel(projectRoot, modelID, expected string) (*os.Root, Model, error) {
	if !validID.MatchString(modelID) {
		return nil, Model{}, errors.New("invalid model id")
	}
	if _, err := modelRoot(projectRoot, false); err != nil {
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
	root, err := openDataDirectory(models, modelID, false)
	if err != nil {
		return nil, Model{}, err
	}
	var model Model
	if err = readDataJSON(root, "model.json", 16<<10, &model); err != nil {
		root.Close()
		return nil, Model{}, err
	}
	if model.ID != modelID || model.SchemaVersion != 1 || !validateTask(model.Task) {
		root.Close()
		return nil, Model{}, errors.New("invalid model metadata")
	}
	if expected != "" {
		created, e := time.Parse(time.RFC3339Nano, expected)
		if e != nil || !created.Equal(model.CreatedAt) {
			root.Close()
			return nil, Model{}, errors.New("this model was replaced; refresh before using inference")
		}
	}
	return root, model, nil
}

func ImportCheckpoint(projectRoot, modelID, expected, name, filename, encoded string, validate CheckpointValidator) (Checkpoint, error) {
	checkpointMu.Lock()
	defer checkpointMu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Checkpoint{}, errors.New("checkpoint name must be 1 to 100 characters")
	}
	if strings.ContainsAny(filename, "/\\") || filename == "" {
		return Checkpoint{}, errors.New("choose a weights file, not a path")
	}
	format := "pt"
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pt", ".pth":
	case ".safetensors":
		format = "safetensors"
	default:
		return Checkpoint{}, errors.New("choose .pt, .pth, or .safetensors tensor weights")
	}
	if len(encoded) == 0 || len(encoded) > base64.StdEncoding.EncodedLen(MaxCheckpointBytes) {
		return Checkpoint{}, errors.New("checkpoint must be at most 64 MiB")
	}
	weights, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(weights) == 0 || len(weights) > MaxCheckpointBytes {
		return Checkpoint{}, errors.New("invalid checkpoint bytes or 64 MiB limit exceeded")
	}
	root, model, err := openCheckpointModel(projectRoot, modelID, expected)
	if err != nil {
		return Checkpoint{}, err
	}
	defer root.Close()
	graph, err := readDataFile(root, "graph.json", maxGraphBytes)
	if err != nil {
		return Checkpoint{}, err
	}
	if err = validateGraph(graph); err != nil {
		return Checkpoint{}, err
	}
	data, err := ReadData(projectRoot, modelID, "")
	if err != nil {
		return Checkpoint{}, err
	}
	if data.Selection == nil {
		return Checkpoint{}, errors.New("save Data preparation before importing weights so inputs, tokenizer and normalization are known")
	}
	selection := *data.Selection
	// Check the destination before running any expensive validation.
	checkpoints, err := openDataDirectory(root, "checkpoints", true)
	if err != nil {
		return Checkpoint{}, err
	}
	defer checkpoints.Close()
	if validate == nil {
		return Checkpoint{}, errors.New("local checkpoint validator is unavailable")
	}
	validated, err := validate(graph, selection, weights, format)
	if err != nil {
		return Checkpoint{}, err
	}
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		return Checkpoint{}, err
	}
	if len(selectionJSON) > 2<<20 {
		return Checkpoint{}, errors.New("saved preparation exceeds checkpoint metadata limit")
	}
	revision := sha256.Sum256(graph)
	h := sha256.New()
	h.Write(graph)
	h.Write(selectionJSON)
	h.Write(weights)
	h.Write([]byte(format))
	id := hex.EncodeToString(h.Sum(nil))
	cp := Checkpoint{ID: id, Name: name, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), GraphRevision: hex.EncodeToString(revision[:]), ModelCreatedAt: model.CreatedAt.Format(time.RFC3339Nano), Task: model.Task, Format: format, ByteCount: int64(len(weights)), ChatCompatible: validated.ChatCompatible && model.Task == "text-generation", Input: validated.Input}
	if _, e := checkpoints.Lstat(id); e == nil {
		existing, e := readCheckpointRoot(checkpoints, id, cp.ModelCreatedAt)
		return existing.Checkpoint, e
	} else if !errors.Is(e, os.ErrNotExist) {
		return Checkpoint{}, e
	}
	stageName, stage, err := createDataStage(checkpoints, ".checkpoint-")
	if err != nil {
		return Checkpoint{}, err
	}
	defer stage.Close()
	published := false
	defer func() {
		if !published {
			discardDataStage(checkpoints, stageName, stage)
		}
	}()
	for _, file := range []struct {
		name  string
		bytes []byte
	}{{"graph.json", graph}, {"selection.json", selectionJSON}, {"weights.bin", weights}} {
		if err = writeNewDataFile(stage, file.name, file.bytes); err != nil {
			return Checkpoint{}, err
		}
	}
	if err = writeNewDataJSON(stage, "metadata.json", cp); err != nil {
		return Checkpoint{}, err
	}
	if err = dataAtomicRename(checkpoints, stageName, id); err != nil {
		return Checkpoint{}, err
	}
	published = true
	return cp, nil
}

func readCheckpointRoot(parent *os.Root, id, expected string) (CheckpointSnapshot, error) {
	var snap CheckpointSnapshot
	if !dataDigest.MatchString(id) {
		return snap, errors.New("invalid checkpoint id")
	}
	root, err := openDataDirectory(parent, id, false)
	if err != nil {
		return snap, err
	}
	defer root.Close()
	if err = readDataJSON(root, "metadata.json", 2<<20, &snap.Checkpoint); err != nil {
		return snap, err
	}
	cp := snap.Checkpoint
	if cp.ID != id || cp.ModelCreatedAt != expected || cp.ByteCount < 1 || cp.ByteCount > MaxCheckpointBytes || (cp.Format != "pt" && cp.Format != "safetensors") {
		return snap, errors.New("checkpoint metadata does not match this model version")
	}
	snap.Graph, err = readDataFile(root, "graph.json", maxGraphBytes)
	if err != nil {
		return snap, err
	}
	if err = readDataJSON(root, "selection.json", 2<<20, &snap.Selection); err != nil {
		return snap, err
	}
	snap.Weights, err = readDataFile(root, "weights.bin", MaxCheckpointBytes)
	if err != nil {
		return snap, err
	}
	revision := sha256.Sum256(snap.Graph)
	selectionJSON, _ := json.Marshal(snap.Selection)
	h := sha256.New()
	h.Write(snap.Graph)
	h.Write(selectionJSON)
	h.Write(snap.Weights)
	h.Write([]byte(cp.Format))
	if int64(len(snap.Weights)) != cp.ByteCount || hex.EncodeToString(revision[:]) != cp.GraphRevision || hex.EncodeToString(h.Sum(nil)) != id {
		return snap, errors.New("checkpoint files changed; import the original weights again")
	}
	return snap, nil
}

func ReadCheckpoint(projectRoot, modelID, expected, id string) (CheckpointSnapshot, error) {
	root, model, err := openCheckpointModel(projectRoot, modelID, expected)
	if err != nil {
		return CheckpointSnapshot{}, err
	}
	defer root.Close()
	parent, err := openDataDirectory(root, "checkpoints", false)
	if err != nil {
		return CheckpointSnapshot{}, err
	}
	defer parent.Close()
	return readCheckpointRoot(parent, id, model.CreatedAt.Format(time.RFC3339Nano))
}

func ListCheckpoints(projectRoot, modelID, expected string) ([]Checkpoint, error) {
	result := []Checkpoint{}
	root, model, err := openCheckpointModel(projectRoot, modelID, expected)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	parent, err := openDataDirectory(root, "checkpoints", false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	f, err := parent.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !dataDigest.MatchString(entry.Name()) {
			continue
		}
		child, e := openDataDirectory(parent, entry.Name(), false)
		if e != nil {
			return nil, e
		}
		var cp Checkpoint
		e = readDataJSON(child, "metadata.json", 2<<20, &cp)
		child.Close()
		if e != nil {
			return nil, e
		}
		if cp.ID != entry.Name() || cp.ModelCreatedAt != model.CreatedAt.Format(time.RFC3339Nano) {
			return nil, fmt.Errorf("checkpoint %s belongs to another model version", entry.Name())
		}
		result = append(result, cp)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	return result, nil
}
