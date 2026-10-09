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

// Workflow depths.
const (
	DepthDirect   = "direct"
	DepthGuided   = "guided"
	DepthDesigned = "designed"
)

// Work node kinds and edge relations recorded by the engine.
const (
	NodeGoal            = "goal"
	NodeCriterion       = "criterion"
	NodeArtifact        = "artifact"
	NodeFact            = "fact"
	NodeRequirement     = "requirement"
	NodeNonGoal         = "non_goal"
	NodeOption          = "option"
	NodeDecision        = "decision"
	NodeTask            = "task"
	NodeUnknown         = "unknown"
	NodeMemoryCandidate = "memory_candidate"

	RelRequires     = "requires"
	RelServes       = "serves"
	RelDependsOn    = "depends_on"
	RelSupports     = "supports"
	RelContradicts  = "contradicts"
	RelSelects      = "selects"
	RelImplements   = "implements"
	RelVerifies     = "verifies"
	RelCandidateFor = "candidate_for"
)

// Semantic node lifecycle statuses. Stale and superseded are defined below.
const (
	StatusProposed     = "proposed"
	StatusApproved     = "approved"
	StatusRejected     = "rejected"
	StatusPending      = "pending"
	StatusReady        = "ready"
	StatusInProgress   = "in_progress"
	StatusCompleted    = "completed"
	StatusFailed       = "failed"
	StatusBlocked      = "blocked"
	StatusOpen         = "open"
	StatusResolved     = "resolved"
	StatusAcceptedRisk = "accepted_risk"
	StatusPromoted     = "promoted"
	StatusConflicted   = "conflicted"
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
	EvidenceDiscovery          = "discovery"
	EvidenceWorkflowApproval   = "workflow_approval"
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
	Revision      int        `json:"revision"`
	CreatedAt     time.Time  `json:"createdAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
}

// WorkNode is a semantic node in a work's graph.
type WorkNode struct {
	ID           string          `json:"id"`
	WorkID       string          `json:"workId"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Content      json.RawMessage `json:"content,omitempty"`
	Status       string          `json:"status"`
	Confidence   float64         `json:"confidence"`
	Revision     int             `json:"revision"`
	EvidenceIDs  []string        `json:"evidenceIds,omitempty"`
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

// WorkUpdateRequest is one bounded semantic batch; graph validation enforces
// node, edge, text and rationale limits before persistence.
type WorkUpdateRequest struct {
	WorkID           string           `json:"work_id"`
	ExpectedRevision int              `json:"expected_revision"`
	WorkflowDepth    string           `json:"workflow_depth,omitempty"`
	Nodes            []WorkNodeChange `json:"nodes,omitempty"`
	Edges            []WorkEdgeChange `json:"edges,omitempty"`
	Rationale        string           `json:"rationale,omitempty"`
}

// WorkNodeChange either creates a node using Ref, Kind, Title and Content, or
// transitions an existing ID using a revision and from/to status predicates.
type WorkNodeChange struct {
	Ref              string          `json:"ref,omitempty"`
	ID               string          `json:"id,omitempty"`
	Kind             string          `json:"kind,omitempty"`
	Title            string          `json:"title,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	ExpectedRevision int             `json:"expected_revision,omitempty"`
	FromStatus       string          `json:"from_status,omitempty"`
	ToStatus         string          `json:"to_status,omitempty"`
	EvidenceIDs      []string        `json:"evidence_ids,omitempty"`
	SupersededBy     string          `json:"superseded_by,omitempty"`
}

// WorkEdgeChange permits persisted IDs or batch-local refs at either endpoint.
type WorkEdgeChange struct {
	From     string `json:"from"`
	Relation string `json:"relation"`
	To       string `json:"to"`
}

// WorkUpdateResult contains only counts and the new work revision.
type WorkUpdateResult struct {
	Revision     int `json:"revision"`
	Created      int `json:"created"`
	Transitioned int `json:"transitioned"`
	Linked       int `json:"linked"`
}

// WorkflowGate is engine-only approval metadata, never tool output.
type WorkflowGate struct {
	WorkID       string `json:"-"`
	NodeID       string `json:"-"`
	NodeRevision int    `json:"-"`
	Kind         string `json:"-"`
	Reason       string `json:"-"`
	Summary      string `json:"-"`
}

// PreparedWorkUpdate is a data-only persistence contract. internal/work
// resolves and validates changes; internal/store consumes them transactionally.
type PreparedWorkUpdate struct {
	WorkID           string                 `json:"-"`
	ExpectedRevision int                    `json:"-"`
	WorkflowDepth    string                 `json:"-"`
	Creates          []WorkNode             `json:"-"`
	NodeChecks       []WorkNodeCheck        `json:"-"`
	Transitions      []WorkNodeTransition   `json:"-"`
	Edges            []WorkEdge             `json:"-"`
	EvidenceLinks    []WorkNodeEvidenceLink `json:"-"`
	Gates            []WorkflowGate         `json:"-"`
}

// WorkNodeCheck preserves optimistic predicates even for evidence-only updates.
type WorkNodeCheck struct {
	ID               string `json:"-"`
	ExpectedRevision int    `json:"-"`
	ExpectedStatus   string `json:"-"`
}

// WorkNodeTransition carries the predicates needed for a conditional update.
type WorkNodeTransition struct {
	ID               string `json:"-"`
	ExpectedRevision int    `json:"-"`
	FromStatus       string `json:"-"`
	ToStatus         string `json:"-"`
	SupersededBy     string `json:"-"`
}

// WorkNodeEvidenceLink references evidence without copying its contents.
type WorkNodeEvidenceLink struct {
	NodeID     string `json:"-"`
	EvidenceID string `json:"-"`
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
	// VaultHash links the full output stored in the vault ("" when none was retained).
	VaultHash      string `json:"vaultHash,omitempty"`
	EnvFingerprint string `json:"envFingerprint,omitempty"`
	// Availability is none (summary only), available, or unavailable (the vault
	// object is missing or corrupt).
	Availability string `json:"availability"`
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
	FingerprintID   string          `json:"fingerprintId,omitempty"`
}

// WorkDetail is a work with everything recorded for it.
type WorkDetail struct {
	Work         Work                  `json:"work"`
	Nodes        []WorkNode            `json:"nodes"`
	Edges        []WorkEdge            `json:"edges"`
	Evidence     []Evidence            `json:"evidence"`
	Attempts     []VerificationAttempt `json:"attempts"`
	Fingerprints []Fingerprint         `json:"fingerprints"`
}

// Evidence availability.
const (
	AvailNone        = "none"
	AvailAvailable   = "available"
	AvailUnavailable = "unavailable"
)

// Criterion node statuses added by the evidence lifecycle.
const (
	StatusStale      = "stale"
	StatusSuperseded = "superseded"
)

// Fingerprint kinds.
const (
	FingerprintVerification = "verification"
	FingerprintTurnEnd      = "turn_end"
)

// Fingerprint is the workspace state observed when an attempt was recorded or a turn ended.
type Fingerprint struct {
	ID      string    `json:"id"`
	WorkID  string    `json:"workId"`
	TurnID  string    `json:"turnId,omitempty"`
	Kind    string    `json:"kind"`
	Value   string    `json:"value"`
	Paths   []string  `json:"paths"`
	TakenAt time.Time `json:"takenAt"`
}

// VaultObjectRow is the index entry for a vault object.
type VaultObjectRow struct {
	Hash             string    `json:"hash"`
	Class            string    `json:"class"`
	Status           string    `json:"status"`
	Size             int64     `json:"size"`
	OriginalSize     int64     `json:"originalSize"`
	Truncated        bool      `json:"truncated"`
	CreatedAt        time.Time `json:"createdAt"`
	LastReferencedAt time.Time `json:"lastReferencedAt"`
}

// VaultStats summarizes the vault for inspection.
type VaultStats struct {
	Objects       int   `json:"objects"`
	Bytes         int64 `json:"bytes"`
	EligibleBytes int64 `json:"eligibleBytes"`
}
