package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func (e *Engine) InspectWorkbenchImport(ctx context.Context, p protocol.WorkbenchImportParams) (any, error) {
	if _, err := e.project(ctx, p.ProjectID); err != nil {
		return nil, err
	}
	if err := workbench.RequireImportPair(p.Files); err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	result, err := workbench.InspectImport(p.Files)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "inspect model: %v", err)
	}
	return result, nil
}
func (e *Engine) CreateWorkbenchImport(ctx context.Context, p protocol.WorkbenchImportParams) (any, error) {
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	var graph workbench.ImportedGraph
	if err := json.Unmarshal([]byte(p.GraphJSON), &graph); err != nil {
		return nil, err
	}
	if !graph.Source.WeightsVerified {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "Missing safetensors weights. Select config.json and all weight files together.")
	}
	opened, err := workbench.CreateImported(project.Root, p.Name, json.RawMessage(p.GraphJSON))
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "create imported model: %v", err)
	}
	return struct {
		workbench.OpenResult
		GraphJSON string `json:"graphJson"`
	}{opened, string(opened.Graph)}, nil
}
