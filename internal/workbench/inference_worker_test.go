package workbench

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedInferenceRealPyTorchAndCancellation(t *testing.T) {
	python := os.Getenv("UMCODE_WORKBENCH_TEST_PYTHON")
	if python == "" {
		t.Skip("set UMCODE_WORKBENCH_TEST_PYTHON for real CPU inference integration")
	}
	t.Setenv("UMCODE_WORKBENCH_PYTHON", python)
	manager := NewInferenceManager(t.TempDir())
	defer manager.Close()
	graph := json.RawMessage(`{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"n0","kind":"input","position":{"x":0,"y":0},"config":{"mode":"features","features":1}},{"id":"n1","kind":"linear","position":{"x":0,"y":100},"config":{"outFeatures":1}},{"id":"n2","kind":"output","position":{"x":0,"y":200},"config":{}}],"edges":[{"id":"e1","source":"n0","target":"n1"},{"id":"e2","source":"n1","target":"n2"}]}`)
	shape, err := manager.Shape(t.Context(), graph)
	if err != nil || !shape.Untrained || shape.Device != "cpu" || shape.ParameterCount != 2 {
		t.Fatalf("shape=%+v err=%v", shape, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "weights.pt")
	code := `import torch,sys;torch.save({"layers.layer_1.weight":torch.tensor([[3.]]),"layers.layer_1.bias":torch.tensor([2.])},sys.argv[1])`
	if out, err := exec.Command(python, "-I", "-c", code, path).CombinedOutput(); err != nil {
		t.Fatalf("fixture %s: %v", out, err)
	}
	weights, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	selection := DataSelection{Config: DataConfig{Mode: "features", Purpose: "regression", InputColumns: []string{"x"}, Scale: "none"}}
	validation, err := manager.Validate(t.Context(), graph, selection, weights, "pt")
	if err != nil || validation.Device != "cpu" || validation.Input.Mode != "features" {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
	snap := CheckpointSnapshot{Checkpoint: Checkpoint{ID: "real", Format: "pt"}, Graph: graph, Selection: selection, Weights: weights}
	result, err := manager.Predict(t.Context(), "key", snap, map[string]any{"features": map[string]float64{"x": 14}})
	if err != nil || result.Value == nil || *result.Value != 44 || result.CheckpointID != "real" || result.Device != "cpu" {
		t.Fatalf("prediction=%+v err=%v", result, err)
	}
	if !manager.Loaded("key") {
		t.Fatal("no resident worker")
	}
	manager.Unload("key")
	if manager.Loaded("key") {
		t.Fatal("worker not unloaded")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	if _, err = manager.Shape(ctx, graph); err == nil {
		t.Fatal("cancelled inference admitted")
	}
	manager.Close()
	if _, err = manager.Shape(t.Context(), graph); err == nil {
		t.Fatal("shutdown admitted a worker")
	}
}
