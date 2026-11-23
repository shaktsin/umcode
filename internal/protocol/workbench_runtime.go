package protocol

type WorkbenchRuntimeParams struct {
	Imported  bool   `json:"imported,omitempty"`
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`
}
