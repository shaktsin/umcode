package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
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
	envelope := workbenchActionEnvelope(c, "import_data")
	envelope["sources"] = results
	encoded, _ := json.Marshal(envelope)
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

// workbenchActionEnvelope is the identity every Workbench action result carries
// so the app can match it to the open model before acting on it.
func workbenchActionEnvelope(c *protocol.WorkbenchTurnContext, action string) map[string]any {
	return map[string]any{"action": action, "projectId": c.ProjectID, "modelId": c.ModelID, "modelCreatedAt": c.ModelCreatedAt}
}

func tailBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

type workbenchScriptRequest struct {
	Title           string   `json:"title"`
	InputDatasetIDs []string `json:"inputDatasetIds"`
	OutputName      string   `json:"outputName"`
	Script          string   `json:"script"`
	ExpectOutput    string   `json:"expectOutput"`
}

func (e *Engine) workbenchRunDataScriptAction(ctx context.Context, args json.RawMessage) (string, error) {
	c := workbenchContextFrom(ctx)
	if c == nil {
		return "", errors.New("Data script action requires a Workbench turn")
	}
	if len(args) > 65536 {
		return "", errors.New("Data script action exceeds 64 KiB")
	}
	var request workbenchScriptRequest
	if err := strictWorkbenchJSON(args, &request); err != nil {
		return "", err
	}
	project, err := e.project(ctx, c.ProjectID)
	if err != nil {
		return "", err
	}
	python := e.runtimeSetup().Selected(ctx)
	if python == "" {
		return "", errors.New("no Python runtime is selected; set one up under Train > Python runtime first")
	}
	e.workbenchMu.Lock()
	_, err = matchingWorkbenchModel(project.Root, c.ModelID, c.ModelCreatedAt)
	e.workbenchMu.Unlock()
	if err != nil {
		return "", err
	}
	e.dataScriptMu.Lock()
	defer e.dataScriptMu.Unlock()
	result, err := workbench.RunDataScript(ctx, workbench.DataScriptRequest{
		ProjectRoot: project.Root, ModelID: c.ModelID, Python: python, Title: request.Title, OutputName: request.OutputName,
		Script: request.Script, InputDatasetIDs: request.InputDatasetIDs, ExpectOutput: request.ExpectOutput,
	})
	if err != nil {
		return "", fmt.Errorf("%v\nstdout:\n%s\nstderr:\n%s", err, tailBytes(result.Stdout, 4096), tailBytes(result.Stderr, 4096))
	}
	response := workbenchActionEnvelope(c, "run_data_script")
	response["durationMs"], response["stdout"] = result.DurationMS, tailBytes(result.Stdout, 8192)
	audit := map[string]any{"projectId": c.ProjectID, "modelId": c.ModelID, "title": request.Title, "inputs": request.InputDatasetIDs, "scriptSha256": fmt.Sprintf("%x", sha256.Sum256([]byte(request.Script)))}
	if result.Dataset != nil {
		columns := make([]string, 0, len(result.Dataset.Columns))
		for _, column := range result.Dataset.Columns {
			columns = append(columns, column.Name)
		}
		response["datasetId"], response["name"], response["rows"], response["columns"] = result.Dataset.ID, result.Dataset.Name, result.Dataset.RowCount, columns
		audit["datasetId"] = result.Dataset.ID
		c.CreatedDatasetIDs = append(c.CreatedDatasetIDs, result.Dataset.ID)
	}
	_ = e.Store.Audit(ctx, "workbench.data.script", audit)
	encoded, _ := json.Marshal(response)
	return string(encoded), nil
}

// workbenchActionApproval describes the approval card for actions that read
// project files, download data, or run model-written code.
func (e *Engine) workbenchActionApproval(ctx context.Context, name string, args json.RawMessage) (risk tools.Risk, reason, summary string) {
	if name == "workbench.import_data" {
		var request struct {
			Sources []workbenchImportSource `json:"sources"`
		}
		labels := []string{}
		if json.Unmarshal(args, &request) == nil {
			for _, source := range request.Sources {
				label := source.Path
				if source.Type == "huggingface" {
					label = "huggingface:" + source.Repo
				}
				labels = append(labels, label)
			}
		}
		return tools.RiskYellow, "The assistant wants to import data into this Workbench model", fmt.Sprintf("Import %d dataset source(s): %s", len(labels), strings.Join(labels, ", "))
	}
	var request workbenchScriptRequest
	_ = json.Unmarshal(args, &request)
	names := []string{}
	if c := workbenchContextFrom(ctx); c != nil && len(request.InputDatasetIDs) > 0 {
		known := map[string]string{}
		if project, err := e.project(ctx, c.ProjectID); err == nil {
			if state, err := workbench.ReadData(project.Root, c.ModelID, ""); err == nil {
				for _, dataset := range state.Datasets {
					known[dataset.ID] = dataset.Name
				}
			}
		}
		for _, id := range request.InputDatasetIDs {
			if label := known[id]; label != "" {
				names = append(names, label)
			} else {
				names = append(names, id)
			}
		}
	}
	reads := "nothing"
	if len(names) > 0 {
		reads = strings.Join(names, ", ")
	}
	writes := "a report only"
	if request.ExpectOutput == "dataset" {
		writes = fmt.Sprintf("dataset %q", request.OutputName)
	}
	return tools.RiskRed, "The assistant wrote Python that will run sandboxed with no network access", fmt.Sprintf("Run data script %q: reads %s, writes %s", request.Title, reads, writes)
}
