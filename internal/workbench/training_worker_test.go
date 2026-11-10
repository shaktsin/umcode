package workbench

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestTrainingRealRegressionCheckpointAndCancellation(t *testing.T) {
	python := os.Getenv("UMCODE_WORKBENCH_TEST_PYTHON")
	if python == "" {
		t.Skip("set UMCODE_WORKBENCH_TEST_PYTHON for training integration")
	}
	t.Setenv("UMCODE_WORKBENCH_PYTHON", python)
	root := t.TempDir()
	model, err := Create(root, "Learn linear", "custom")
	if err != nil {
		t.Fatal(err)
	}
	version := model.CreatedAt.Format(time.RFC3339Nano)
	graph := json.RawMessage(`{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"n0","kind":"input","position":{"x":0,"y":0},"config":{"mode":"features","features":1}},{"id":"n1","kind":"linear","position":{"x":0,"y":100},"config":{"outFeatures":1,"bias":true}},{"id":"n2","kind":"output","position":{"x":0,"y":200},"config":{}}],"edges":[{"id":"e1","source":"n0","target":"n1"},{"id":"e2","source":"n1","target":"n2"}]}`)
	if err = SaveGraph(root, model.ID, graph); err != nil {
		t.Fatal(err)
	}
	data := "x,y\n-5,-9\n-4,-7\n-3,-5\n-2,-3\n-1,-1\n0,1\n1,3\n2,5\n3,7\n4,9\n5,11\n6,13\n7,15\n8,17\n9,19\n10,21\n"
	ds, err := ImportData(root, model.ID, DataSource{Kind: "local", Name: "Linear"}, []DataFile{{Name: "linear.csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte(data))}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrepareData(root, model.ID, DataConfig{DatasetID: ds.ID, Mode: "features", Purpose: "regression", InputColumns: []string{"x"}, TargetColumn: "y", Scale: "standard", TrainPercent: 60, ValidationPercent: 20, TestPercent: 20, Seed: 17})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewTrainingManager(t.TempDir())
	defer manager.Close()
	inference := NewInferenceManager(t.TempDir())
	defer inference.Close()
	publish := func(ctx context.Context, snap TrainingSnapshot, weights []byte, name string) (Checkpoint, error) {
		validated, err := inference.Validate(ctx, snap.Graph, snap.Selection, weights, "pt")
		if err != nil {
			return Checkpoint{}, err
		}
		return SaveTrainingCheckpoint(root, model.ID, version, name, snap, weights, validated)
	}
	run, err := manager.Start(root, model.ID, version, TrainingConfig{Epochs: 600, BatchSize: 16, LearningRate: .05, Seed: 17}, publish)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		runs, e := manager.List(root, model.ID, version)
		if e != nil {
			t.Fatal(e)
		}
		run = runs[0]
		if run.Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if run.Status != "completed" || run.CheckpointID == "" || run.Test == nil || run.Validation == nil || run.Test.RMSE == nil || *run.Test.RMSE > .5 {
		t.Fatalf("status=%s error=%s checkpoint=%s test=%+v train=%+v", run.Status, run.Error, run.CheckpointID, run.Test, run.Train)
	}
	snap, err := ReadCheckpoint(root, model.ID, version, run.CheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := inference.Predict(t.Context(), "trained", snap, map[string]any{"features": map[string]float64{"x": 3}})
	if err != nil || result.Value == nil || *result.Value < 6.5 || *result.Value > 7.5 {
		t.Fatalf("prediction=%+v err=%v", result, err)
	}
	cancelled, err := manager.Start(root, model.ID, version, TrainingConfig{Epochs: 1000, BatchSize: 1, LearningRate: .001, Seed: 17}, publish)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Start(root, model.ID, version, cancelled.Config, publish); err == nil {
		t.Fatal("concurrent job accepted")
	}
	cancelled, err = manager.Stop(root, model.ID, version, cancelled.ID)
	if err != nil || cancelled.Status != "cancelled" || cancelled.CheckpointID != "" {
		t.Fatalf("stop=%+v err=%v", cancelled, err)
	}
	manager.Close()
	runs, err := manager.List(root, model.ID, version)
	if err != nil || runs[0].Status != "cancelled" {
		t.Fatalf("cancel persisted=%+v %v", runs, err)
	}
	cps, err := ListCheckpoints(root, model.ID, version)
	if err != nil || len(cps) != 1 {
		t.Fatalf("cancel published weights %+v %v", cps, err)
	}
}

func TestTrainingRealQuestionAnswerUsesPreparedPromptAtInference(t *testing.T) {
	python := os.Getenv("UMCODE_WORKBENCH_TEST_PYTHON")
	if python == "" {
		t.Skip("set test Python")
	}
	t.Setenv("UMCODE_WORKBENCH_PYTHON", python)
	root := t.TempDir()
	m, err := Create(root, "Raw questions", "text-generation")
	if err != nil {
		t.Fatal(err)
	}
	version := m.CreatedAt.Format(time.RFC3339Nano)
	graph := json.RawMessage(`{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"n0","kind":"input","position":{"x":0,"y":0},"config":{"mode":"tokens","sequence":32}},{"id":"n1","kind":"embedding","position":{"x":0,"y":1},"config":{"vocab":259,"width":16}},{"id":"n2","kind":"linear","position":{"x":0,"y":2},"config":{"outFeatures":259}},{"id":"n3","kind":"output","position":{"x":0,"y":3},"config":{}}],"edges":[{"id":"e1","source":"n0","target":"n1"},{"id":"e2","source":"n1","target":"n2"},{"id":"e3","source":"n2","target":"n3"}]}`)
	if err = SaveGraph(root, m.ID, graph); err != nil {
		t.Fatal(err)
	}
	ds, err := ImportData(root, m.ID, DataSource{Kind: "local", Name: "QA"}, []DataFile{{Name: "qa.csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte("question,answer\nQ?,Yes.\nQ?,Yes.\n"))}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrepareData(root, m.ID, DataConfig{DatasetID: ds.ID, Mode: "text", Purpose: "text-generation", InputColumns: []string{"question"}, TargetColumn: "answer", SequenceLength: 32, TrainPercent: 100, Scale: "none", Seed: 17})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewTrainingManager(t.TempDir())
	defer manager.Close()
	inference := NewInferenceManager(t.TempDir())
	defer inference.Close()
	run, err := manager.Start(root, m.ID, version, TrainingConfig{Epochs: 80, BatchSize: 2, LearningRate: .05, Seed: 17}, func(ctx context.Context, snap TrainingSnapshot, w []byte, name string) (Checkpoint, error) {
		validated, e := inference.Validate(ctx, snap.Graph, snap.Selection, w, "pt")
		if e != nil {
			return Checkpoint{}, e
		}
		return SaveTrainingCheckpoint(root, m.ID, version, name, snap, w, validated)
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		runs, e := manager.List(root, m.ID, version)
		if e != nil {
			t.Fatal(e)
		}
		run = runs[0]
		if run.Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if run.Status != "completed" {
		t.Fatalf("training %s: %s", run.Status, run.Error)
	}
	snap, err := ReadCheckpoint(root, m.ID, version, run.CheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Checkpoint.PromptFormat != "prompt-response-v1" {
		t.Fatalf("lost prompt recipe: %+v", snap.Checkpoint)
	}
	result, err := inference.Predict(t.Context(), "raw-question", snap, map[string]any{"prompt": "Q?\n", "temperature": 0, "maxNewTokens": 16})
	if err != nil || result.Text != "Yes." {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
