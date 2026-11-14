package protocol

import "encoding/json"

// WorkbenchTurnContext is an immutable editor snapshot, not filesystem authority.
type WorkbenchTurnContext struct {
	ProjectID      string `json:"projectId"`
	ModelID        string `json:"modelId"`
	ModelName      string `json:"modelName"`
	GraphJSON      string `json:"graphJson"`
	Revision       string `json:"revision"`
	SelectedNodeID string `json:"selectedNodeId,omitempty"`
	Summary        string `json:"summary"`
	// Captured by the engine at admission; never trusted from the client or sent to the model.
	ModelCreatedAt string   `json:"-"`
	SavedGraphHash string   `json:"-"`
	DataVersion    string   `json:"-"`
	BuildSaved     bool     `json:"-"`
	StartedRunIDs  []string `json:"-"`
}

type WorkbenchProposal struct {
	ProjectID   string            `json:"projectId"`
	ModelID     string            `json:"modelId"`
	ModelName   string            `json:"modelName"`
	Revision    string            `json:"revision"`
	Explanation string            `json:"explanation"`
	Operations  []json.RawMessage `json:"operations"`
}
