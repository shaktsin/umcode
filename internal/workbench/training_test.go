package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestTrainingCompatibilityCatchesByteVocabularyMismatch(t *testing.T) {
	graph := json.RawMessage(`{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"input","kind":"input","config":{"mode":"tokens","sequence":128}},{"id":"embedding","kind":"embedding","config":{"vocab":32000,"width":128}}],"edges":[]}`)
	selection := DataSelection{Config: DataConfig{Mode: "text", SequenceLength: 128}}
	issues := TrainingCompatibilityIssues(graph, selection)
	if len(issues) != 1 || !strings.Contains(issues[0], "259") {
		t.Fatalf("issues=%v", issues)
	}
}

func TestTrainingBatchLimitForSavedTextGraph(t *testing.T) {
	graph := []byte(`{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"input","kind":"input","config":{"mode":"tokens","sequence":512}},{"id":"embedding","kind":"embedding","config":{"width":32}},{"id":"position","kind":"position","config":{"length":512}},{"id":"transformer","kind":"transformer","config":{"width":32,"heads":4,"repeat":1,"expansion":2}},{"id":"meanpool","kind":"meanpool","config":{}},{"id":"linear","kind":"linear","config":{"outFeatures":18}},{"id":"output","kind":"output","config":{}}],"edges":[{"id":"a","source":"input","target":"embedding"},{"id":"b","source":"embedding","target":"position"},{"id":"c","source":"position","target":"transformer"},{"id":"d","source":"transformer","target":"meanpool"},{"id":"e","source":"meanpool","target":"linear"},{"id":"f","source":"linear","target":"output"}]}`)
	selection := DataSelection{Config: DataConfig{Mode: "text", SequenceLength: 512}, Counts: DataCounts{Train: 1000, Validation: 100, Test: 100}}
	large, ok := TrainingMaxBatchSize(graph, selection, 16<<30)
	if !ok || large != 23 {
		t.Fatalf("large-memory max batch = %d, known = %v", large, ok)
	}
	small, ok := TrainingMaxBatchSize(graph, selection, 2<<30)
	if !ok || small < 1 || small >= large {
		t.Fatalf("small-memory max batch = %d, known = %v", small, ok)
	}
	fallback, ok := TrainingMaxBatchSize(graph, selection, 0)
	if !ok || fallback != 3 {
		t.Fatalf("unavailable-memory fallback = %d, known = %v", fallback, ok)
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
