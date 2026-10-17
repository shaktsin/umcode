package protocol

import "time"

// AgentActivation records the admitted source of delegation permission.
// Only admission code may decide that a user request or installed skill qualifies.
type AgentActivation string

const (
	AgentActivationExplicitUser   AgentActivation = "explicit_user"
	AgentActivationInstalledSkill AgentActivation = "installed_skill"
)

// AgentState is the durable lifecycle of a delegation task or child.
type AgentState string

const (
	AgentStateQueued          AgentState = "queued"
	AgentStateRunning         AgentState = "running"
	AgentStateWaitingApproval AgentState = "waiting_approval"
	AgentStateCompleted       AgentState = "completed"
	AgentStateFailed          AgentState = "failed"
	AgentStateCancelled       AgentState = "cancelled"
	AgentStateInterrupted     AgentState = "interrupted"
)

// AgentRole determines the child's execution and workspace policy.
type AgentRole string

const (
	AgentRoleImplementer AgentRole = "implementer"
	AgentRoleReviewer    AgentRole = "reviewer"
)

// AgentTask owns delegation permission for one root turn in a parent chat.
type AgentTask struct {
	ID           string          `json:"id"`
	ThreadID     string          `json:"threadId"`
	RootTurnID   string          `json:"rootTurnId"`
	Activation   AgentActivation `json:"activation"`
	State        AgentState      `json:"state"`
	BaseRevision string          `json:"baseRevision"`
	BaseRoot     string          `json:"baseRoot"`
	ParentRoot   string          `json:"parentRoot"`
	ProjectID    string          `json:"projectId,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// AgentChild is one stable child identity, including recoverable work and usage.
type AgentChild struct {
	ID               string          `json:"id"`
	TaskID           string          `json:"taskId"`
	Role             AgentRole       `json:"role"`
	Model            ModelSelection  `json:"model,omitempty"`
	Assignment       AgentAssignment `json:"assignment"`
	State            AgentState      `json:"state"`
	Revision         string          `json:"revision,omitempty"`
	WorkspaceID      string          `json:"workspaceId,omitempty"`
	Result           AgentResult     `json:"result"`
	Usage            UsageTotals     `json:"usage"`
	CorrectionRounds int             `json:"correctionRounds"`
	Version          int64           `json:"version"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

// AgentAssignment records the prompt, acceptance criteria and supplied references.
type AgentAssignment struct {
	Prompt           string   `json:"prompt"`
	AcceptanceChecks []string `json:"acceptanceChecks,omitempty"`
	ContextRefs      []string `json:"contextRefs,omitempty"`
}

// AgentResult retains partial results and verification evidence after failure.
type AgentReviewReport struct {
	ReviewerID    string         `json:"reviewerId"`
	ImplementerID string         `json:"implementerId"`
	Revision      string         `json:"revision"`
	Verdict       string         `json:"verdict"`
	Findings      []AgentFinding `json:"findings"`
}

type AgentResult struct {
	Review       *AgentReviewReport   `json:"review,omitempty"`
	Summary      string               `json:"summary"`
	Findings     []AgentFinding       `json:"findings,omitempty"`
	Checks       []AgentCheckEvidence `json:"checks,omitempty"`
	ChangedPaths []string             `json:"changedPaths,omitempty"`
	Blocker      string               `json:"blocker,omitempty"`
}

type AgentFinding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type AgentCheckEvidence struct {
	Command string `json:"command"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// AgentFollowup is a durable correction, delivered only at a runner boundary.
type AgentFollowup struct {
	CallID  string `json:"callId"`
	Message string `json:"message"`
	Round   int    `json:"round"`
}

// DelegationUpdated updates the existing child row in its owning parent chat.
type DelegationUpdated struct {
	ThreadID string         `json:"threadId"`
	TaskID   string         `json:"taskId"`
	Child    AgentChild     `json:"child,omitzero"`
	Task     *AgentTask     `json:"task,omitempty"`
	Workflow *AgentWorkflow `json:"workflow,omitempty"`
	Error    string         `json:"error,omitempty"`
	Sequence int64          `json:"sequence,omitempty"`
}

const NotifyDelegationUpdated = "delegation/updated"

type DelegationCancelParams struct {
	ThreadID string `json:"threadId"`
	TaskID   string `json:"taskId"`
	ChildID  string `json:"childId,omitempty"`
}

type DelegationCancelResult struct {
	Task     AgentTask    `json:"task"`
	Children []AgentChild `json:"children"`
}

type AgentWorkflowCheck struct {
	Label           string `json:"label"`
	Command         string `json:"command"`
	Directory       string `json:"directory,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Reason          string `json:"reason,omitempty"`
	RequiredCommand string `json:"required_command,omitempty"`
	Status          string `json:"status"`
	Summary         string `json:"summary,omitempty"`
}
type AgentWorkflow struct {
	TaskID         string               `json:"taskId"`
	ChildID        string               `json:"childId"`
	Revision       string               `json:"revision"`
	Checks         []AgentWorkflowCheck `json:"checks"`
	CleanupWarning string               `json:"cleanupWarning,omitempty"`
}

// AgentSnapshot is a read-only, parent-scoped hydration view. Host workspace
// roots and internal execution identities are never exposed by this RPC.
type AgentSnapshot struct {
	Task            AgentTask         `json:"task"`
	Children        []AgentChild      `json:"children"`
	ChildErrors     map[string]string `json:"childErrors,omitempty"`
	ChildSequences  map[string]int64  `json:"childSequences,omitempty"`
	Workflow        *AgentWorkflow    `json:"workflow,omitempty"`
	Usage           UsageTotals       `json:"usage"`
	BudgetAvailable bool              `json:"budgetAvailable"`
	StopReason      string            `json:"stopReason,omitempty"`
}
type DelegationListParams struct {
	ThreadID string `json:"threadId"`
	TaskID   string `json:"taskId,omitempty"`
}
type DelegationListResult struct {
	Tasks []AgentSnapshot `json:"tasks"`
}
type DelegationFileParams struct {
	ThreadID string `json:"threadId"`
	TaskID   string `json:"taskId"`
	ChildID  string `json:"childId"`
	Revision string `json:"revision"`
	Path     string `json:"path"`
}
