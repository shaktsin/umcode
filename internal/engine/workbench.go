package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func (e *Engine) ListWorkbenchModels(ctx context.Context, p protocol.WorkbenchModelListParams) (any, error) {
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	models, err := workbench.List(project.Root)
	if err != nil {
		return nil, fmt.Errorf("list workbench models: %w", err)
	}
	return struct {
		Models []workbench.Model `json:"models"`
	}{Models: models}, nil
}

func (e *Engine) CreateWorkbenchModel(ctx context.Context, p protocol.WorkbenchModelCreateParams) (any, error) {
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	model, err := workbench.Create(project.Root, p.Name, p.Task)
	if err != nil {
		if errors.Is(err, workbench.ErrExists) {
			return nil, protocol.Errorf(protocol.CodeConflict, "model already exists")
		}
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	_ = e.Store.Audit(ctx, "workbench.model.create", map[string]any{"projectId": project.ID, "modelId": model.ID, "task": model.Task})
	return model, nil
}

func (e *Engine) OpenWorkbenchModel(ctx context.Context, p protocol.WorkbenchModelOpenParams) (any, error) {
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	model, err := workbench.Open(project.Root, p.ModelID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, protocol.Errorf(protocol.CodeNotFound, "model bundle %q is unavailable", p.ModelID)
		}
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	return struct {
		workbench.OpenResult
		GraphJSON string `json:"graphJson"`
	}{OpenResult: model, GraphJSON: string(model.Graph)}, nil
}

func (e *Engine) SaveWorkbenchGraph(ctx context.Context, p protocol.WorkbenchGraphSaveParams) (any, error) {
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	if p.ModelCreatedAt != "" {
		if _, err := matchingWorkbenchModel(project.Root, p.ModelID, p.ModelCreatedAt); err != nil {
			return nil, err
		}
	}
	graph := p.Graph
	if p.GraphJSON != "" {
		if len(graph) != 0 {
			return nil, protocol.Errorf(protocol.CodeInvalidParams, "supply exactly one of graph or graphJson")
		}
		graph = []byte(p.GraphJSON)
	}
	if err := workbench.SaveGraph(project.Root, p.ModelID, graph); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, protocol.Errorf(protocol.CodeNotFound, "model bundle %q is unavailable", p.ModelID)
		}
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	_ = e.Store.Audit(ctx, "workbench.graph.save", map[string]any{"projectId": project.ID, "modelId": p.ModelID})
	return struct {
		OK bool `json:"ok"`
	}{OK: true}, nil
}

func (e *Engine) DeleteWorkbenchModel(ctx context.Context, p protocol.WorkbenchModelDeleteParams) (any, error) {
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	if p.ModelCreatedAt == "" {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "model version is required; refresh the model list")
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	if _, err := matchingWorkbenchModel(project.Root, p.ModelID, p.ModelCreatedAt); err != nil {
		return nil, err
	}
	e.trainingRunner().CancelModel(project.Root, p.ModelID, p.ModelCreatedAt)
	e.importedRunner().CancelModel(project.Root, p.ModelID)
	trashPath, err := workbench.Delete(project.Root, p.ModelID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, protocol.Errorf(protocol.CodeNotFound, "model bundle %q is unavailable", p.ModelID)
		}
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "move model to project Trash: %v", err)
	}
	e.inferenceRunner().Unload(inferencePrefix(protocol.WorkbenchInferenceParams{ProjectID: p.ProjectID, ModelID: p.ModelID, ModelCreatedAt: p.ModelCreatedAt}))
	if err := e.removeWorkbenchPromotion(ctx, p.ProjectID, p.ModelID, p.ModelCreatedAt); err != nil {
		e.Log.Warn("remove deleted model from local chat catalog", "err", err)
	}
	_ = e.Store.Audit(ctx, "workbench.model.delete", map[string]any{"projectId": project.ID, "modelId": p.ModelID, "trashPath": trashPath})
	return struct {
		OK        bool   `json:"ok"`
		TrashPath string `json:"trashPath"`
	}{true, trashPath}, nil
}

// The persisted creation timestamp distinguishes a replacement bundle that
// reuses a deleted model's name. Call under workbenchMu before mutating files.
func matchingWorkbenchModel(root, id, expected string) (workbench.Model, error) {
	opened, err := workbench.Open(root, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workbench.Model{}, protocol.Errorf(protocol.CodeNotFound, "model bundle %q is unavailable", id)
		}
		return workbench.Model{}, protocol.Errorf(protocol.CodeInvalidParams, "model is unavailable: %v", err)
	}
	if expected != "" {
		created, err := time.Parse(time.RFC3339Nano, expected)
		if err != nil || !created.Equal(opened.Model.CreatedAt) {
			return workbench.Model{}, protocol.Errorf(protocol.CodeConflict, "this model was replaced; refresh before trying again")
		}
	}
	return opened.Model, nil
}
