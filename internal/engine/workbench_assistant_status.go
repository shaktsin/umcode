package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

// workbenchStageSnapshot reads saved stage metadata for the currently admitted
// project/model. It excludes dataset rows, checkpoint weights and local paths.
func sameWorkbenchGraph(saved, draft []byte) bool {
	var a, b any
	return json.Unmarshal(saved, &a) == nil && json.Unmarshal(draft, &b) == nil && reflect.DeepEqual(a, b)
}

func (e *Engine) workbenchStageSnapshot(ctx context.Context, c *protocol.WorkbenchTurnContext) string {
	status := map[string]any{}
	project, err := e.project(ctx, c.ProjectID)
	if err != nil {
		return `{"error":"project is unavailable"}`
	}
	model, err := workbench.Open(project.Root, c.ModelID)
	if err != nil {
		return `{"error":"model is unavailable"}`
	}
	version := model.Model.CreatedAt.Format(time.RFC3339Nano)
	status["build"] = map[string]any{"task": model.Model.Task, "draftMatchesSaved": sameWorkbenchGraph(model.Graph, []byte(c.GraphJSON))}

	data, err := workbench.ReadData(project.Root, c.ModelID, "")
	if err != nil {
		status["data"] = map[string]any{"error": err.Error()}
	} else {
		datasets := make([]map[string]any, 0, min(len(data.Datasets), 5))
		for _, dataset := range data.Datasets[:min(len(data.Datasets), 5)] {
			columns := make([]map[string]string, 0, min(len(dataset.Columns), 32))
			for _, column := range dataset.Columns[:min(len(dataset.Columns), 32)] {
				columns = append(columns, map[string]string{"name": column.Name, "kind": column.Kind})
			}
			datasets = append(datasets, map[string]any{"id": dataset.ID, "name": dataset.Name, "rows": dataset.RowCount, "format": dataset.Format, "columns": columns})
		}
		stage := map[string]any{"datasetCount": len(data.Datasets), "datasets": datasets, "prepared": data.Selection != nil}
		if data.Selection != nil {
			stage["selection"] = map[string]any{"version": data.Selection.Version, "config": data.Selection.Config, "counts": data.Selection.Counts, "classCount": len(data.Selection.Classes), "warnings": data.Selection.Warnings}
			stage["trainingCompatibilityIssues"] = workbench.TrainingCompatibilityIssues(model.Graph, *data.Selection)
		}
		status["data"] = stage
	}

	runs, err := e.trainingRunner().List(project.Root, c.ModelID, version)
	if err != nil {
		status["train"] = map[string]any{"error": err.Error()}
	} else {
		recent := make([]map[string]any, 0, min(len(runs), 3))
		for _, run := range runs[:min(len(runs), 3)] {
			failure := run.Error
			if len(failure) > 2048 {
				failure = failure[:2048] + "…"
			}
			recent = append(recent, map[string]any{"id": run.ID, "name": run.Name, "status": run.Status, "error": failure, "epoch": run.Epoch, "config": run.Config, "preparationVersion": run.PreparationVersion, "checkpointId": run.CheckpointID, "recentEpochs": run.History[max(0, len(run.History)-3):], "trainMetrics": run.Train, "validationMetrics": run.Validation, "testMetrics": run.Test})
		}
		trainStatus := map[string]any{"runtimeAvailable": e.trainingRunner().Available(), "runCount": len(runs), "recentRuns": recent}
		if data, readErr := workbench.ReadData(project.Root, c.ModelID, ""); readErr == nil && data.Selection != nil {
			if maxBatch, known := workbench.TrainingMaxBatchSize(model.Graph, *data.Selection, workbench.TrainingAvailableMemory()); known {
				trainStatus["maxBatchSize"] = maxBatch
			}
		}
		status["train"] = trainStatus
	}

	inference, err := e.inferenceState(ctx, protocol.WorkbenchInferenceParams{ProjectID: c.ProjectID, ModelID: c.ModelID, ModelCreatedAt: version}, project.Root)
	if err != nil {
		status["inference"] = map[string]any{"error": err.Error()}
	} else {
		checkpoints := make([]map[string]any, 0, min(len(inference.Checkpoints), 5))
		for _, checkpoint := range inference.Checkpoints[:min(len(inference.Checkpoints), 5)] {
			checkpoints = append(checkpoints, map[string]any{"id": checkpoint.ID, "name": checkpoint.Name, "task": checkpoint.Task, "format": checkpoint.Format, "input": checkpoint.Input, "chatCompatible": checkpoint.ChatCompatible})
		}
		status["inference"] = map[string]any{"checkpointCount": len(inference.Checkpoints), "checkpoints": checkpoints, "loadedCheckpointId": inference.LoadedCheckpointID, "runtimes": inference.Runtimes, "issues": inference.Issues, "promoted": inference.Promotion != nil}
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return `{"error":"workbench status is unavailable"}`
	}
	if len(encoded) > 32<<10 {
		status["data"] = map[string]any{"note": "Data metadata exceeds the assistant context limit; inspect Data in the Workbench UI."}
		status["inference"] = map[string]any{"note": "Inference metadata exceeds the assistant context limit; inspect Inference in the Workbench UI."}
		encoded, err = json.Marshal(status)
		if err != nil || len(encoded) > 32<<10 {
			return `{"error":"workbench status exceeds the assistant context limit"}`
		}
	}
	return string(encoded)
}
