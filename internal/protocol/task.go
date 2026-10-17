package protocol

import (
	"encoding/json"
	"time"
)

// ExecutionTask is durable user work, distinct from scheduled Task jobs.
type ExecutionTask struct {
	ObjectiveTruncated bool            `json:"objectiveTruncated,omitempty"`
	ID                 string          `json:"id"`
	ThreadID           string          `json:"threadId"`
	WorkID             string          `json:"workId,omitempty"`
	ProjectID          string          `json:"projectId,omitempty"`
	OriginItemID       string          `json:"originItemId"`
	Revision           int             `json:"revision"`
	Objective          string          `json:"objective"`
	Scope              json.RawMessage `json:"scope,omitempty"`
	Phase              string          `json:"phase,omitempty"`
	Status             string          `json:"status"`
	ContinuationItemID string          `json:"continuationItemId,omitempty"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

type ActionFailure struct {
	AuthoritySource string          `json:"authoritySource,omitempty"`
	Advisory        bool            `json:"advisory,omitempty"`
	Category        string          `json:"category"`
	OperationID     string          `json:"operationId,omitempty"`
	BlockerID       string          `json:"blockerId,omitempty"`
	Message         string          `json:"message"`
	Retryable       bool            `json:"retryable"`
	NextStep        string          `json:"nextStep,omitempty"`
	State           json.RawMessage `json:"state,omitempty"`
}

type TaskOperation struct {
	ArgsRef            string          `json:"argsRef,omitempty"`
	ArgsSHA256         string          `json:"argsSHA256,omitempty"`
	ResolvedByID       string          `json:"resolvedById,omitempty"`
	Resolution         string          `json:"resolution,omitempty"`
	ID                 string          `json:"id"`
	TaskID             string          `json:"taskId"`
	TurnID             string          `json:"turnId"`
	ItemID             string          `json:"itemId,omitempty"`
	Revision           int             `json:"revision"`
	Tool               string          `json:"tool"`
	Args               json.RawMessage `json:"args"`
	ImplementationID   string          `json:"implementationId,omitempty"`
	Resources          json.RawMessage `json:"resources,omitempty"`
	Status             string          `json:"status"`
	VerificationStatus string          `json:"verificationStatus"`
	ResultItemID       string          `json:"resultItemId,omitempty"`
	EvidenceIDs        []string        `json:"evidenceIds,omitempty"`
	SupersedesID       string          `json:"supersedesId,omitempty"`
	Failure            *ActionFailure  `json:"failure,omitempty"`
}

type TaskRequirement struct {
	ResourceBindings map[string]string `json:"resourceBindings,omitempty"`
	ID               string            `json:"id"`
	TaskID           string            `json:"taskId"`
	Revision         int               `json:"revision"`
	Origin           string            `json:"origin"`
	OriginRef        string            `json:"originRef"`
	Scope            json.RawMessage   `json:"scope"`
	Condition        json.RawMessage   `json:"condition"`
	Status           string            `json:"status"`
	ApprovalID       string            `json:"approvalId,omitempty"`
}

type TaskSnapshot struct {
	Task         ExecutionTask     `json:"task"`
	Operations   []TaskOperation   `json:"operations"`
	Requirements []TaskRequirement `json:"requirements"`
}

type TaskTransition struct {
	ExpectedRevision   int
	Status             string
	Phase              string
	ContinuationItemID string
}
type OperationTransition struct {
	ResolvedByID       string
	Resolution         string
	ExpectedRevision   int
	Status             string
	VerificationStatus string
	ResultItemID       string
	EvidenceIDs        []string
	SupersedesID       string
	Failure            *ActionFailure
}
type TaskResolution struct{ Relation, TaskID, UserItemID, Question string }
type RequirementProposal struct {
	ResourceBindings          map[string]string
	TaskID, Origin, OriginRef string
	Scope, Condition          json.RawMessage
	ExpectedRevision          int
}
type AuthorityDecision struct {
	Outcome                     string
	Failure                     *ActionFailure
	RequirementIDs, ApprovalIDs []string
}
