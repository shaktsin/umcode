package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrainingSnapshotPinsPreparedDataAndModelVersion(t *testing.T) {
	root, m := checkpointFixture(t)
	version := m.CreatedAt.Format(time.RFC3339Nano)
	snap, err := SnapshotTraining(root, m.ID, version)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Splits["train"]) == 0 || snap.Selection.Counts.Train != 4 {
		t.Fatalf("snapshot=%+v", snap.Selection)
	}
	if _, err = SnapshotTraining(root, m.ID, time.Now().Add(time.Hour).Format(time.RFC3339Nano)); err == nil {
		t.Fatal("replaced model accepted")
	}
	if err = SaveGraph(root, m.ID, json.RawMessage(`{"version":1,"nodes":[],"edges":[],"note":"new"}`)); err != nil {
		t.Fatal(err)
	}
	cp, err := SaveTrainingCheckpoint(root, m.ID, version, "Trained epoch", snap, []byte("weights"), CheckpointValidation{Input: CheckpointInput{Mode: "features"}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadCheckpoint(root, m.ID, version, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Graph) != string(snap.Graph) {
		t.Fatal("published changed graph instead of training snapshot")
	}
}

func TestTrainingConfigRejectsUnsafeBounds(t *testing.T) {
	good := TrainingConfig{Epochs: 20, BatchSize: 16, LearningRate: .003, Seed: 17}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []TrainingConfig{{Epochs: 0, BatchSize: 1, LearningRate: .01}, {Epochs: 1001, BatchSize: 1, LearningRate: .01}, {Epochs: 1, BatchSize: 257, LearningRate: .01}, {Epochs: 1, BatchSize: 1, LearningRate: 0}, {Epochs: 1, BatchSize: 1, LearningRate: .01, Seed: -1}} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestTrainingSnapshotRejectsSymlinkSplit(t *testing.T) {
	root, m := checkpointFixture(t)
	version := m.CreatedAt.Format(time.RFC3339Nano)
	ds, err := ReadData(root, m.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	split := filepath.Join(root, "models", m.ID, "data", "prepared", ds.Selection.Version, "train.jsonl")
	if err = os.Remove(split); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(t.TempDir(), "outside"), split); err != nil {
		t.Fatal(err)
	}
	if _, err = SnapshotTraining(root, m.ID, version); err == nil {
		t.Fatal("followed symlink")
	}
}

func TestTrainingHistoryPersistenceAndInterruptedRecovery(t *testing.T) {
	root, m := checkpointFixture(t)
	version := m.CreatedAt.Format(time.RFC3339Nano)
	run := TrainingRun{ID: "train-0123456789abcdef0123456789abcdef", ModelCreatedAt: version, Status: "running", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), History: []TrainingEpoch{}}
	if err := WriteTrainingRun(root, m.ID, version, run); err != nil {
		t.Fatal(err)
	}
	mgr := NewTrainingManager(t.TempDir())
	defer mgr.Close()
	runs, err := mgr.List(root, m.ID, version)
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if runs[0].Error == "" {
		t.Fatal("interruption unexplained")
	}
}

func TestLegacyTrainingCheckpointRecoversRawPromptRecipe(t *testing.T) {
	root, model := checkpointFixture(t)
	version := model.CreatedAt.Format(time.RFC3339Nano)
	snap := CheckpointSnapshot{Checkpoint: Checkpoint{ID: "legacy", ChatCompatible: true}, Selection: DataSelection{Config: DataConfig{Mode: "text", Purpose: "text-generation", TargetColumn: "response"}}}
	run := TrainingRun{ID: "train-0123456789abcdef0123456789abcdef", ModelCreatedAt: version, Status: "completed", CheckpointID: "legacy", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), History: []TrainingEpoch{}}
	if err := WriteTrainingRun(root, model.ID, version, run); err != nil {
		t.Fatal(err)
	}
	if err := resolveCheckpointPromptFormat(root, model.ID, version, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Checkpoint.PromptFormat != "prompt-response-v1" {
		t.Fatalf("raw QA got %q", snap.Checkpoint.PromptFormat)
	}
	snap.Checkpoint.ID = "imported"
	snap.Checkpoint.PromptFormat = ""
	if err := resolveCheckpointPromptFormat(root, model.ID, version, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Checkpoint.PromptFormat != "chat-v1" {
		t.Fatal("imported legacy weights changed format")
	}
}
