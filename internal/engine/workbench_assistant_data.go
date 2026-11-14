package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

type workbenchImportSource struct {
	Type     string   `json:"type"`
	Path     string   `json:"path"`
	Repo     string   `json:"repo"`
	Revision string   `json:"revision"`
	Files    []string `json:"files"`
}

type workbenchImportSourceResult struct {
	OK        bool     `json:"ok"`
	DatasetID string   `json:"datasetId,omitempty"`
	Name      string   `json:"name,omitempty"`
	Rows      int      `json:"rows,omitempty"`
	Columns   []string `json:"columns,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func (e *Engine) workbenchImportDataAction(ctx context.Context, args json.RawMessage) (string, error) {
	c := workbenchContextFrom(ctx)
	if c == nil {
		return "", errors.New("Import action requires a Workbench turn")
	}
	if len(args) > 16384 {
		return "", errors.New("Import action exceeds 16 KiB")
	}
	var request struct {
		Sources []workbenchImportSource `json:"sources"`
	}
	if err := strictWorkbenchJSON(args, &request); err != nil {
		return "", err
	}
	if len(request.Sources) == 0 || len(request.Sources) > 8 {
		return "", errors.New("imports take 1-8 sources")
	}
	project, err := e.project(ctx, c.ProjectID)
	if err != nil {
		return "", err
	}
	results := make([]workbenchImportSourceResult, 0, len(request.Sources))
	for _, source := range request.Sources {
		results = append(results, e.workbenchImportOneSource(ctx, project, c, source))
	}
	encoded, _ := json.Marshal(map[string]any{"action": "import_data", "sources": results})
	return string(encoded), nil
}

func (e *Engine) workbenchImportOneSource(ctx context.Context, project protocol.Project, c *protocol.WorkbenchTurnContext, source workbenchImportSource) workbenchImportSourceResult {
	params := protocol.WorkbenchDataImportParams{ProjectID: c.ProjectID, ModelID: c.ModelID, ModelCreatedAt: c.ModelCreatedAt}
	switch source.Type {
	case "local":
		files, err := workbench.ReadLocalDataFiles(project.Root, source.Path)
		if err != nil {
			return workbenchImportSourceResult{Error: err.Error()}
		}
		params.Source = workbench.DataSource{Kind: "local", Name: source.Path}
		params.Files = files
	case "huggingface":
		params.Source = workbench.DataSource{Kind: "huggingface", Name: source.Repo, Repo: source.Repo, Revision: source.Revision, Files: source.Files}
	default:
		return workbenchImportSourceResult{Error: fmt.Sprintf("source type %q must be local or huggingface", source.Type)}
	}
	imported, err := e.ImportWorkbenchData(ctx, params)
	if err != nil {
		return workbenchImportSourceResult{Error: err.Error()}
	}
	dataset, ok := imported.(workbench.Dataset)
	if !ok {
		return workbenchImportSourceResult{Error: "import returned an unexpected result"}
	}
	columns := make([]string, 0, len(dataset.Columns))
	for _, column := range dataset.Columns {
		columns = append(columns, column.Name)
	}
	return workbenchImportSourceResult{OK: true, DatasetID: dataset.ID, Name: dataset.Name, Rows: dataset.RowCount, Columns: columns}
}

func (e *Engine) workbenchInspectDataAction(ctx context.Context, args json.RawMessage) (string, error) {
	c := workbenchContextFrom(ctx)
	if c == nil {
		return "", errors.New("Inspect action requires a Workbench turn")
	}
	if len(args) > 8192 {
		return "", errors.New("Inspect action exceeds 8 KiB")
	}
	var request struct {
		DatasetIDs []string `json:"datasetIds"`
		Sample     int      `json:"sample"`
	}
	if err := strictWorkbenchJSON(args, &request); err != nil {
		return "", err
	}
	if request.Sample == 0 {
		request.Sample = 5
	}
	project, err := e.project(ctx, c.ProjectID)
	if err != nil {
		return "", err
	}
	report, err := workbench.InspectDatasets(project.Root, c.ModelID, request.DatasetIDs, request.Sample)
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(report)
	return string(encoded), nil
}
