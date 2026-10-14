package protocol

import "time"

type MemoryListParams struct {
	ProjectID      string `json:"projectId"`
	Cursor         string `json:"cursor,omitempty"`
	IncludeHistory bool   `json:"includeHistory,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}
type MemoryReviewEntry struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	SemanticKey    string     `json:"semanticKey"`
	Category       string     `json:"category"`
	TargetPath     string     `json:"targetPath"`
	Text           string     `json:"text"`
	TextHash       string     `json:"textHash"`
	TargetHash     string     `json:"targetHash"`
	Status         string     `json:"status"`
	SourceRevision string     `json:"sourceRevision"`
	EvidenceIDs    []string   `json:"evidenceIds"`
	UserOwned      bool       `json:"userOwned"`
	CreatedAt      time.Time  `json:"createdAt"`
	PromotedAt     *time.Time `json:"promotedAt,omitempty"`
	Revision       int        `json:"revision"`
	LineageID      string     `json:"lineageId,omitempty"`
}
type MemoryListResult struct {
	Entries    []MemoryReviewEntry `json:"entries"`
	NextCursor string              `json:"nextCursor,omitempty"`
}
type MemoryChangeParams struct {
	ProjectID          string `json:"projectId"`
	MemoryID           string `json:"memoryId"`
	ExpectedTextHash   string `json:"expectedTextHash"`
	ExpectedTargetHash string `json:"expectedTargetHash"`
	Text               string `json:"text,omitempty"`
}
type MemoryChangeResult struct {
	Entry              MemoryReviewEntry `json:"entry"`
	InstructionRemains bool              `json:"instructionRemains"`
	Warning            string            `json:"warning,omitempty"`
}
