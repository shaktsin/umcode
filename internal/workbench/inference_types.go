package workbench

type CheckpointInput struct {
	Mode           string   `json:"mode"`
	Features       []string `json:"features"`
	SequenceLength int      `json:"sequenceLength"`
	ImageWidth     int      `json:"imageWidth"`
	ImageHeight    int      `json:"imageHeight"`
	ImageChannels  int      `json:"imageChannels"`
	Classes        []string `json:"classes"`
}

type Checkpoint struct {
	PromptFormat   string          `json:"promptFormat,omitempty"`
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	CreatedAt      string          `json:"createdAt"`
	GraphRevision  string          `json:"graphRevision"`
	ModelCreatedAt string          `json:"modelCreatedAt"`
	Task           string          `json:"task"`
	Format         string          `json:"format"`
	ByteCount      int64           `json:"byteCount"`
	ChatCompatible bool            `json:"chatCompatible"`
	Input          CheckpointInput `json:"input"`
}

type RuntimeInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type Promotion struct {
	SourceFingerprint string `json:"sourceFingerprint,omitempty"`
	ContextWindow     int    `json:"contextWindow,omitempty"`
	PromptFormat      string `json:"promptFormat,omitempty"`
	ID                string `json:"id"`
	ProjectID         string `json:"projectId"`
	ModelID           string `json:"modelId"`
	ModelCreatedAt    string `json:"modelCreatedAt"`
	CheckpointID      string `json:"checkpointId"`
	Name              string `json:"name"`
	Runtime           string `json:"runtime"`
	CreatedAt         string `json:"createdAt"`
}

type InferenceState struct {
	Checkpoints        []Checkpoint  `json:"checkpoints"`
	Runtimes           []RuntimeInfo `json:"runtimes"`
	LoadedCheckpointID string        `json:"loadedCheckpointId"`
	Promotion          *Promotion    `json:"promotion,omitempty"`
	Issues             []string      `json:"issues"`
}

type PredictionScore struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}
type Prediction struct {
	CheckpointID string            `json:"checkpointId"`
	TestID       string            `json:"testId"`
	Text         string            `json:"text,omitempty"`
	Value        *float64          `json:"value,omitempty"`
	Label        string            `json:"label,omitempty"`
	Scores       []PredictionScore `json:"scores,omitempty"`
	Device       string            `json:"device"`
	DurationMs   int64             `json:"durationMs"`
	Truncated    bool              `json:"truncated,omitempty"`
}
