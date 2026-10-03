package protocol

import (
	"encoding/json"
	"time"
)

// Work statuses.
const (
	WorkOpen      = "open"
	WorkCompleted = "completed"
	WorkAbandoned = "abandoned"
)

// Workflow depths. "designed" is reserved for a later phase.
const (
	DepthDirect = "direct"
	DepthGuided = "guided"
)

// Work node kinds and edge relations recorded by the engine.
const (
	NodeGoal      = "goal"
	NodeCriterion = "criterion"
	NodeArtifact  = "artifact"
	NodeFact      = "fact"

	RelRequires = "requires"
	RelServes   = "serves"
)

// Verification attempt statuses.
const (
	AttemptPassed  = "passed"
	AttemptFailed  = "failed"
	AttemptBlocked = "blocked"
	AttemptNotRun  = "not_run"
)

// Evidence kinds.
const (
	EvidenceFileChange         = "file_change"
	EvidenceVerificationOutput = "verification_output"
	EvidenceToolError          = "tool_error"
)

// Work is one objective in a thread, spanning turns until it is resolved.
type Work struct {
	ID            string     `json:"id"`
	ThreadID      string     `json:"threadId"`
	ProjectID     string     `json:"projectId,omitempty"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	WorkflowDepth string     `json:"workflowDepth"`
	Goal          string     `json:"goal"`
	CreatedAt     time.Time  `json:"createdAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
}

// WorkNode is a goal, criterion, artifact or fact in a work's graph.
type WorkNode struct {
	ID           string          `json:"id"`
	WorkID       string          `json:"workId"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Content      json.RawMessage `json:"content,omitempty"`
	Status       string          `json:"status"`
	Confidence   float64         `json:"confidence"`
	Revision     int             `json:"revision"`
	ValidFrom    time.Time       `json:"validFrom"`
	ValidUntil   *time.Time      `json:"validUntil,omitempty"`
	SupersededBy string          `json:"supersededBy,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// WorkEdge links two nodes of one work.
type WorkEdge struct {
	WorkID     string `json:"workId"`
	FromNodeID string `json:"fromNodeId"`
	Relation   string `json:"relation"`
	ToNodeID   string `json:"toNodeId"`
}

// Evidence is an immutable observation. It holds a capped excerpt (the first
// 2 KB) of the output, a hash and provenance; the full output is not stored.
type Evidence struct {
	ID             string     `json:"id"`
	WorkID         string     `json:"workId"`
	NodeID         string     `json:"nodeId,omitempty"`
	Kind           string     `json:"kind"`
	SourceURI      string     `json:"sourceUri,omitempty"`
	SourceRevision string     `json:"sourceRevision,omitempty"`
	ContentHash    string     `json:"contentHash,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	Confidence     float64    `json:"confidence"`
	ObservedAt     time.Time  `json:"observedAt"`
	StaleAt        *time.Time `json:"staleAt,omitempty"`
}

// VerificationAttempt is one append-only run of a check.
type VerificationAttempt struct {
	ID              string          `json:"id"`
	WorkID          string          `json:"workId"`
	CriterionNodeID string          `json:"criterionNodeId,omitempty"`
	CheckType       string          `json:"checkType"`
	Command         string          `json:"command"`
	Environment     json.RawMessage `json:"environment,omitempty"`
	Status          string          `json:"status"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	EvidenceID      string          `json:"evidenceId,omitempty"`
	StartedAt       time.Time       `json:"startedAt"`
	FinishedAt      time.Time       `json:"finishedAt"`
}

// WorkDetail is a work with everything recorded for it.
type WorkDetail struct {
	Work     Work                  `json:"work"`
	Nodes    []WorkNode            `json:"nodes"`
	Edges    []WorkEdge            `json:"edges"`
	Evidence []Evidence            `json:"evidence"`
	Attempts []VerificationAttempt `json:"attempts"`
}
