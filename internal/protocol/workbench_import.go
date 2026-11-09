package protocol

import "github.com/shaktsin/umcode/internal/workbench"

type WorkbenchImportParams struct {
	ProjectID string                 `json:"projectId"`
	Files     []workbench.ImportFile `json:"files,omitempty"`
	Name      string                 `json:"name,omitempty"`
	GraphJSON string                 `json:"graphJson,omitempty"`
}
