package protocol

// Training RPCs are pinned to a model incarnation, matching Data and Inference.
type WorkbenchTrainingParams struct {
	ProjectID           string `json:"projectId"`
	ModelID             string `json:"modelId"`
	ModelCreatedAt      string `json:"modelCreatedAt"`
	RunID               string `json:"runId"`
	CheckSnapshot       bool   `json:"-"`
	ExpectedGraphHash   string `json:"-"`
	ExpectedDataVersion string `json:"-"`
	Config              struct {
		Epochs       int     `json:"epochs"`
		BatchSize    int     `json:"batchSize"`
		LearningRate float64 `json:"learningRate"`
		Seed         int64   `json:"seed"`
	} `json:"config"`
}
