package protocol

import "github.com/shaktsin/umcode/internal/workbench"

const (
	MethodWorkbenchDataGet         = "workbench/data/get"
	MethodWorkbenchDataImport      = "workbench/data/import"
	MethodWorkbenchDataPrepare     = "workbench/data/prepare"
	MethodWorkbenchDataHuggingFace = "workbench/data/huggingface"
)

type WorkbenchDataGetParams struct {
	ProjectID string `json:"projectId"`
	ModelID   string `json:"modelId"`
	DatasetID string `json:"datasetId,omitempty"`
}

type WorkbenchDataImportParams struct {
	ProjectID      string               `json:"projectId"`
	ModelID        string               `json:"modelId"`
	ModelCreatedAt string               `json:"modelCreatedAt,omitempty"`
	Source         workbench.DataSource `json:"source"`
	Files          []workbench.DataFile `json:"files,omitempty"`
}

type WorkbenchDataPrepareParams struct {
	ProjectID      string               `json:"projectId"`
	ModelID        string               `json:"modelId"`
	ModelCreatedAt string               `json:"modelCreatedAt,omitempty"`
	Config         workbench.DataConfig `json:"config"`
}

type WorkbenchDataHuggingFaceParams struct {
	ProjectID string `json:"projectId"`
	ModelID   string `json:"modelId"`
	Action    string `json:"action"`
	Query     string `json:"query,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Secret    string `json:"secret,omitempty"`
}
