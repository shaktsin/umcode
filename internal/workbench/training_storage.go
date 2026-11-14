package workbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
)

var trainingID = regexp.MustCompile(`^train-[a-f0-9]{32}$`)

// SnapshotTraining copies bounded, prepared rows. Training never reads live project paths.
func SnapshotTraining(projectRoot, modelID, expected string) (TrainingSnapshot, error) {
	var snap TrainingSnapshot
	root, _, err := openCheckpointModel(projectRoot, modelID, expected)
	if err != nil {
		return snap, err
	}
	defer root.Close()
	snap.Graph, err = readDataFile(root, "graph.json", maxGraphBytes)
	if err != nil {
		return snap, err
	}
	if IsImportedGraph(snap.Graph) {
		return snap, errors.New(ImportedRuntimeMessage)
	}
	if err = validateGraph(snap.Graph); err != nil {
		return snap, err
	}
	data, err := openDataDirectory(root, "data", false)
	if errors.Is(err, os.ErrNotExist) {
		return snap, errors.New("save Data preparation before starting training")
	}
	if err != nil {
		return snap, err
	}
	defer data.Close()
	if err = readDataJSON(data, "selection.json", 2<<20, &snap.Selection); err != nil {
		return snap, errors.New("save Data preparation before starting training")
	}
	if !dataDigest.MatchString(snap.Selection.Version) || snap.Selection.Version != preparedDataVersion(snap.Selection.Config) {
		return snap, errors.New("saved preparation version is invalid; save preparation again")
	}
	if issues := TrainingCompatibilityIssues(snap.Graph, snap.Selection); len(issues) > 0 {
		return snap, errors.New(issues[0])
	}
	if snap.Selection.Config.Mode != "features" && snap.Selection.Config.Mode != "text" {
		return snap, errors.New("this first Train version supports numeric and text data; vision training is coming later")
	}
	if snap.Selection.Counts.Train < 1 {
		return snap, errors.New("training split is empty; adjust splits in Data and save preparation")
	}
	prepared, err := openDataDirectory(data, "prepared", false)
	if err != nil {
		return snap, err
	}
	defer prepared.Close()
	version, err := openDataDirectory(prepared, snap.Selection.Version, false)
	if err != nil {
		return snap, err
	}
	defer version.Close()
	var saved DataSelection
	if err = readDataJSON(version, "metadata.json", 2<<20, &saved); err != nil {
		return snap, err
	}
	a, _ := json.Marshal(saved)
	b, _ := json.Marshal(snap.Selection)
	if !bytes.Equal(a, b) {
		return snap, errors.New("saved preparation metadata changed; prepare the dataset again")
	}
	snap.Splits = map[string][]byte{}
	remaining := int64(maxPreparedDataBytes)
	for _, name := range []string{"train", "validation", "test"} {
		rows, e := readDataFile(version, name+".jsonl", remaining)
		if e != nil {
			return snap, e
		}
		remaining -= int64(len(rows))
		snap.Splits[name] = rows
	}
	return snap, nil
}

func WriteTrainingRun(projectRoot, modelID, expected string, run TrainingRun) error {
	if !trainingID.MatchString(run.ID) || run.ModelCreatedAt != expected {
		return errors.New("invalid training run identity")
	}
	root, _, err := openCheckpointModel(projectRoot, modelID, expected)
	if err != nil {
		return err
	}
	defer root.Close()
	runs, err := openDataDirectory(root, "runs", true)
	if err != nil {
		return err
	}
	defer runs.Close()
	return atomicDataJSON(runs, run.ID+".json", run)
}
func ReadTrainingRuns(projectRoot, modelID, expected string) ([]TrainingRun, error) {
	result := []TrainingRun{}
	root, _, err := openCheckpointModel(projectRoot, modelID, expected)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	runs, err := openDataDirectory(root, "runs", false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer runs.Close()
	dir, err := runs.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 6 || !trainingID.MatchString(name[:len(name)-5]) || name[len(name)-5:] != ".json" {
			continue
		}
		var run TrainingRun
		if err = readDataJSON(runs, name, 2<<20, &run); err != nil {
			return nil, fmt.Errorf("read training history: %w", err)
		}
		if run.ModelCreatedAt != expected || run.ID+".json" != name {
			return nil, errors.New("training history belongs to another model version")
		}
		result = append(result, run)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	return result, nil
}

// SaveTrainingCheckpoint publishes exactly the graph and preparation used to train.
// Caller verifies live model identity and validates the produced tensor state_dict first.
func SaveTrainingCheckpoint(projectRoot, modelID, expected, name string, snap TrainingSnapshot, weights []byte, validated CheckpointValidation) (Checkpoint, error) {
	checkpointMu.Lock()
	defer checkpointMu.Unlock()
	if len(weights) < 1 || len(weights) > MaxCheckpointBytes {
		return Checkpoint{}, errors.New("trained weights exceed 64 MiB")
	}
	if err := validateGraph(snap.Graph); err != nil {
		return Checkpoint{}, err
	}
	root, model, err := openCheckpointModel(projectRoot, modelID, expected)
	if err != nil {
		return Checkpoint{}, err
	}
	defer root.Close()
	cps, err := openDataDirectory(root, "checkpoints", true)
	if err != nil {
		return Checkpoint{}, err
	}
	defer cps.Close()
	return saveCheckpointFiles(cps, model, name, snap.Graph, snap.Selection, weights, "pt", trainingPromptFormat(snap.Selection), validated)
}

// Prepared QA rows use raw question + newline + response; corpus rows are plain text.
func trainingPromptFormat(selection DataSelection) string {
	if selection.Config.Mode != "text" || selection.Config.Purpose != "text-generation" {
		return ""
	}
	if selection.Config.TargetColumn != "" {
		return "prompt-response-v1"
	}
	return "text-v1"
}

// Early local training checkpoints predate explicit prompt-format metadata.
// Recover only from a completed run linked to this exact checkpoint and incarnation.
// Legacy imported weights retain their documented chat framing.
func resolveCheckpointPromptFormat(projectRoot, modelID, expected string, snap *CheckpointSnapshot) error {
	if snap.Checkpoint.PromptFormat != "" {
		return validatePromptFormat(snap.Checkpoint.PromptFormat)
	}
	snap.Checkpoint.PromptFormat = "chat-v1"
	if snap.Selection.Config.Purpose != "text-generation" {
		return nil
	}
	runs, err := ReadTrainingRuns(projectRoot, modelID, expected)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status == "completed" && run.CheckpointID == snap.Checkpoint.ID {
			snap.Checkpoint.PromptFormat = trainingPromptFormat(snap.Selection)
			if snap.Checkpoint.PromptFormat == "text-v1" {
				snap.Checkpoint.ChatCompatible = false
			}
			break
		}
	}
	return nil
}
func validatePromptFormat(format string) error {
	switch format {
	case "", "chat-v1", "prompt-response-v1", "text-v1":
		return nil
	default:
		return errors.New("checkpoint prompt format is unsupported")
	}
}
