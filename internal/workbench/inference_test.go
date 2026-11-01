package workbench

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkpointFixture(t *testing.T) (string, Model) {
	t.Helper()
	root := t.TempDir()
	m, err := Create(root, "Weights", "classification")
	if err != nil {
		t.Fatal(err)
	}
	ds, err := ImportData(root, m.ID, DataSource{Kind: "local", Name: "Rows"}, []DataFile{{Name: "rows.csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte("x,label\n1,a\n2,b\n3,a\n4,b\n"))}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrepareData(root, m.ID, DataConfig{DatasetID: ds.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x"}, TargetColumn: "label", TrainPercent: 100, Seed: 42, Scale: "none"})
	if err != nil {
		t.Fatal(err)
	}
	return root, m
}

func TestCheckpointSnapshotsAndRecovery(t *testing.T) {
	root, m := checkpointFixture(t)
	validated := 0
	validate := func(graph json.RawMessage, selection DataSelection, weights []byte, format string) (CheckpointValidation, error) {
		validated++
		if string(weights) != "trained weights" || format != "pt" || selection.Config.InputColumns[0] != "x" {
			t.Fatal("validator did not receive actual source bytes and saved preparation")
		}
		return CheckpointValidation{Input: CheckpointInput{Mode: "features", Features: []string{"x"}, Classes: []string{"a", "b"}}, Device: "cpu"}, nil
	}
	cp, err := ImportCheckpoint(root, m.ID, m.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), "Epoch one", "weights.pt", base64.StdEncoding.EncodeToString([]byte("trained weights")), validate)
	if err != nil {
		t.Fatal(err)
	}
	if validated != 1 || cp.ID == "" || cp.Input.Mode != "features" {
		t.Fatalf("checkpoint = %+v", cp)
	}
	if err := SaveGraph(root, m.ID, json.RawMessage(`{"version":1,"nodes":[],"edges":[],"note":"newer draft"}`)); err != nil {
		t.Fatal(err)
	}
	read, err := ReadCheckpoint(root, m.ID, cp.ModelCreatedAt, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Weights) != "trained weights" || strings.Contains(string(read.Graph), "newer draft") {
		t.Fatal("checkpoint did not preserve immutable trained version")
	}
	list, err := ListCheckpoints(root, m.ID, cp.ModelCreatedAt)
	if err != nil || len(list) != 1 {
		t.Fatalf("checkpoint list = %+v,%v", list, err)
	}
	if _, err := ReadCheckpoint(root, m.ID, "different-version", cp.ID); err == nil {
		t.Fatal("replacement model reused an old checkpoint")
	}
	if _, err := ReadCheckpoint(root, m.ID, cp.ModelCreatedAt, "../escape"); err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestCheckpointRejectsSymlinksAndUnpreparedModels(t *testing.T) {
	root := t.TempDir()
	m, err := Create(root, "Unprepared", "custom")
	if err != nil {
		t.Fatal(err)
	}
	validate := func(json.RawMessage, DataSelection, []byte, string) (CheckpointValidation, error) {
		t.Fatal("must reject unsafe checkpoint before validation")
		return CheckpointValidation{}, nil
	}
	if _, err := ImportCheckpoint(root, m.ID, "", "one", "weights.pt", "eA==", validate); err == nil {
		t.Fatal("unprepared checkpoint accepted")
	}
	root, m = checkpointFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "models", m.ID, "checkpoints")); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportCheckpoint(root, m.ID, "", "one", "weights.pt", "eA==", validate); err == nil {
		t.Fatal("symlinked checkpoint folder accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("wrote outside model")
	}
}
