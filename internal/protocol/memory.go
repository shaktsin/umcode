package protocol

import "time"

// Curated project memory categories.
const (
	MemoryCategoryCapability       = "capability"
	MemoryCategoryCommand          = "command"
	MemoryCategoryBoundary         = "boundary"
	MemoryCategoryInvariant        = "invariant"
	MemoryCategoryConvention       = "convention"
	MemoryCategoryPath             = "path"
	MemoryCategoryApprovedDecision = "approved_decision"
)

// Candidate promotion outcomes.
const (
	MemoryOutcomePromoted   = "promoted"
	MemoryOutcomeRejected   = "rejected"
	MemoryOutcomeStale      = "stale"
	MemoryOutcomeConflicted = "conflicted"
	MemoryOutcomePending    = "pending"
)

// Historical project memory row statuses.
const (
	MemoryStatusActive        = "active"
	MemoryStatusSuperseded    = "superseded"
	MemoryStatusConflicted    = "conflicted"
	MemoryStatusPendingRepair = "pending_repair"
)

// Recoverable promotion operation states.
const (
	MemoryOpPrepared      = "prepared"
	MemoryOpFileWritten   = "file_written"
	MemoryOpCommitted     = "committed"
	MemoryOpConflicted    = "conflicted"
	MemoryOpPendingRepair = "pending_repair"
)

// ProjectMemory is durable project knowledge with immutable source IDs.
// Provenance survives deletion of the originating Work or chat.
type ProjectMemory struct {
	ID, ProjectID, WorkID, CandidateNodeID            string
	SemanticKey, Category, TargetPath, Text, TextHash string
	Status, SourceRevision, EvidenceJSON              string
	FileHashBefore, FileHashAfter, SupersededBy       string
	CreatedAt                                         time.Time
	PromotedAt                                        *time.Time
}

// MemoryPromotionOp is the persisted audit and recovery record for one write.
// Byte snapshots are storage data and must not enter model context.
type MemoryPromotionOp struct {
	ID, ProjectID, WorkID, CandidateNodeID, MemoryID string
	ThreadID, TurnID, TargetPath, State              string
	FileHashBefore, FileHashAfter                    string
	BeforeBytes, AfterBytes                          []byte
	ErrorClass                                       string
	CreatedAt, UpdatedAt                             time.Time
}
