package engine

import (
	"context"
	"errors"
	"fmt"
	"os"

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
