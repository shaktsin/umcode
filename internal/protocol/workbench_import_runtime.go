package protocol

import "github.com/shaktsin/umcode/internal/workbench"

type ImportRuntimeParams struct {
	ProjectID      string                       `json:"projectId"`
	ModelID        string                       `json:"modelId"`
	ModelCreatedAt string                       `json:"modelCreatedAt"`
	Action         string                       `json:"action"`
	TestID         string                       `json:"testId,omitempty"`
	Files          []workbench.ImportSourceFile `json:"files,omitempty"`
	UploadID       string                       `json:"uploadId,omitempty"`
	Name           string                       `json:"name,omitempty"`
	Offset         int64                        `json:"offset,omitempty"`
	ContentBase64  string                       `json:"contentBase64,omitempty"`
	Prompt         string                       `json:"prompt,omitempty"`
	ImageBase64    string                       `json:"imageBase64,omitempty"`
	Checkpoint     string                       `json:"checkpoint,omitempty"`
	MaxTokens      int                          `json:"maxTokens,omitempty"`
	Epochs         int                          `json:"epochs,omitempty"`
	LearningRate   float64                      `json:"learningRate,omitempty"`
}
