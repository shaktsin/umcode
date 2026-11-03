package protocol

import "encoding/json"

const (
	MethodWorkbenchModelList   = "workbench/model/list"
	MethodWorkbenchModelCreate = "workbench/model/create"
	MethodWorkbenchModelOpen   = "workbench/model/open"
	MethodWorkbenchModelDelete = "workbench/model/delete"
	MethodWorkbenchGraphSave   = "workbench/graph/save"
)

type WorkbenchModelListParams struct {
	ProjectID string `json:"projectId"`
}

type WorkbenchModelCreateParams struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Task      string `json:"task"`
}

type WorkbenchModelOpenParams struct {
	ProjectID string `json:"projectId"`
	ModelID   string `json:"modelId"`
}

type WorkbenchModelDeleteParams struct {
	ProjectID      string `json:"projectId"`
	ModelID        string `json:"modelId"`
	ModelCreatedAt string `json:"modelCreatedAt"`
}

type WorkbenchGraphSaveParams struct {
	ProjectID      string          `json:"projectId"`
	ModelID        string          `json:"modelId"`
	ModelCreatedAt string          `json:"modelCreatedAt,omitempty"`
	Graph          json.RawMessage `json:"graph"`
	// GraphJSON preserves numeric literals through JavaScript clients. Exactly
	// one of graph or graphJson must be supplied; graph remains supported.
	GraphJSON string `json:"graphJson,omitempty"`
}
