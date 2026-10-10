package protocol

import "time"

// ObservedExcerpt is engine-owned file provenance, never a client assertion.
type ObservedExcerpt struct {
	ID                string    `json:"id"`
	WorkID            string    `json:"workId"`
	EvidenceID        string    `json:"evidenceId"`
	Path              string    `json:"path"`
	WorkspaceRootHash string    `json:"workspaceRootHash"`
	ContentHash       string    `json:"contentHash"`
	StartLine         int       `json:"startLine"`
	EndLine           int       `json:"endLine"`
	Text              string    `json:"text"`
	ObservedAt        time.Time `json:"observedAt"`
}
