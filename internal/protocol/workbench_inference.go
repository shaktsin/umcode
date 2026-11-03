package protocol

// Every inference operation is pinned to a model incarnation, never just a name.
type WorkbenchInferenceParams struct {
	ProjectID      string             `json:"projectId"`
	ModelID        string             `json:"modelId"`
	ModelCreatedAt string             `json:"modelCreatedAt"`
	CheckpointID   string             `json:"checkpointId"`
	Runtime        string             `json:"runtime"`
	Name           string             `json:"name"`
	Filename       string             `json:"filename"`
	ContentBase64  string             `json:"contentBase64"`
	Prompt         string             `json:"prompt"`
	Features       map[string]float64 `json:"features"`
	ImageBase64    string             `json:"imageBase64"`
	Temperature    float64            `json:"temperature"`
	MaxNewTokens   int                `json:"maxNewTokens"`
	TestID         string             `json:"testId"`
}
