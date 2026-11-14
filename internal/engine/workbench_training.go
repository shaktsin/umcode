package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func (e *Engine) trainingRunner() *workbench.TrainingManager {
	e.trainingMu.Lock()
	defer e.trainingMu.Unlock()
	if e.trainingManager == nil {
		e.trainingManager = workbench.NewTrainingManager(e.Cfg.Home)
		_ = e.trainingManager.SetPython(e.runtimeSetup().Selected(context.Background()))
	}
	return e.trainingManager
}
func (e *Engine) WorkbenchTraining(ctx context.Context, op string, p protocol.WorkbenchTrainingParams) (any, error) {
	result, err := e.workbenchTraining(ctx, op, p)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	return result, nil
}
func (e *Engine) workbenchTraining(ctx context.Context, op string, p protocol.WorkbenchTrainingParams) (any, error) {
	if p.ModelCreatedAt == "" {
		return nil, errors.New("model version is required; refresh the model")
	}
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	if _, err = matchingWorkbenchModel(project.Root, p.ModelID, p.ModelCreatedAt); err != nil {
		return nil, err
	}
	opened, err := workbench.Open(project.Root, p.ModelID)
	if err != nil {
		return nil, err
	}
	if op == "start" && p.CheckSnapshot {
		if fmt.Sprintf("%x", sha256.Sum256(opened.Graph)) != p.ExpectedGraphHash {
			return nil, errors.New("saved Build graph changed during this chat turn; ask again with the latest model")
		}
		data, readErr := workbench.ReadData(project.Root, p.ModelID, "")
		if readErr != nil {
			return nil, readErr
		}
		version := ""
		if data.Selection != nil {
			version = data.Selection.Version
		}
		if version != p.ExpectedDataVersion {
			return nil, errors.New("saved Data preparation changed during this chat turn; ask again with the latest model")
		}
	}
	if workbench.IsImportedGraph(opened.Graph) {
		if op == "get" {
			return workbench.TrainingState{Runs: []workbench.TrainingRun{}, Issues: []string{workbench.ImportedRuntimeMessage}}, nil
		}
		return nil, errors.New(workbench.ImportedRuntimeMessage)
	}
	manager := e.trainingRunner()
	switch op {
	case "get":
		runs, err := manager.List(project.Root, p.ModelID, p.ModelCreatedAt)
		if err != nil {
			return nil, err
		}
		state := workbench.TrainingState{Runs: runs, RuntimeAvailable: manager.Available(), Issues: []string{}}
		data, err := workbench.ReadData(project.Root, p.ModelID, "")
		if err != nil {
			return nil, err
		}
		if data.Selection == nil {
			state.Issues = append(state.Issues, "Save Data preparation before training so inputs, targets and evaluation splits are known.")
		} else {
			if maxBatch, known := workbench.TrainingMaxBatchSize(opened.Graph, *data.Selection, workbench.TrainingAvailableMemory()); known {
				state.MaxBatchSize = &maxBatch
			}
			state.Issues = append(state.Issues, workbench.TrainingCompatibilityIssues(opened.Graph, *data.Selection)...)
			if data.Selection.Config.Mode == "image" {
				state.Issues = append(state.Issues, "This first Train version supports numeric and text models. Vision training is coming later.")
			}
			if data.Selection.Counts.Train == 0 {
				state.Issues = append(state.Issues, "The saved training split is empty. Adjust splits in Data and save preparation.")
			}
		}
		if manager.ActiveElsewhere(project.Root, p.ModelID, p.ModelCreatedAt) {
			state.Issues = append(state.Issues, "Another model is training locally. Stop that run or wait for it to finish, then refresh.")
		}
		if !state.RuntimeAvailable {
			state.Issues = append(state.Issues, "Set up a Python runtime with PyTorch to train locally.")
		}
		return state, nil
	case "start":
		config := workbench.TrainingConfig{Epochs: p.Config.Epochs, BatchSize: p.Config.BatchSize, LearningRate: p.Config.LearningRate, Seed: p.Config.Seed}
		e.trainingMu.Lock()
		defer e.trainingMu.Unlock()
		return manager.Start(project.Root, p.ModelID, p.ModelCreatedAt, config, func(jobCtx context.Context, snap workbench.TrainingSnapshot, weights []byte, name string) (workbench.Checkpoint, error) {
			validated, err := e.inferenceRunner().Validate(jobCtx, snap.Graph, snap.Selection, weights, "pt")
			if err != nil {
				return workbench.Checkpoint{}, err
			}
			e.workbenchMu.Lock()
			defer e.workbenchMu.Unlock()
			if err = jobCtx.Err(); err != nil {
				return workbench.Checkpoint{}, err
			}
			if _, err = matchingWorkbenchModel(project.Root, p.ModelID, p.ModelCreatedAt); err != nil {
				return workbench.Checkpoint{}, err
			}
			return workbench.SaveTrainingCheckpoint(project.Root, p.ModelID, p.ModelCreatedAt, name, snap, weights, validated)
		})
	case "stop":
		return manager.Stop(project.Root, p.ModelID, p.ModelCreatedAt, p.RunID)
	default:
		return nil, errors.New("unknown training operation")
	}
}
