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
	// Agent is a runtime agent's current state (runtime tasks only). Order
	// updates of one agent by Agent.Version.
	Agent *RuntimeAgent `json:"agent,omitempty"`
	// Integration is the task's runtime integration status after a change.
	Integration *AgentIntegrationStatus `json:"integration,omitempty"`
	Error       string                  `json:"error,omitempty"`
	Sequence    int64                   `json:"sequence,omitempty"`
}

// AgentIntegrationStatus is the read model of a runtime task's integration:
// what agent.integrate status reports. HeadTree is the integrated tree (empty
// before anything was submitted or applied), AppliedCount how many revisions
// moved it and Verified whether the lead's checks all passed for HeadTree.
type AgentIntegrationStatus struct {
	HeadTree     string              `json:"headTree"`
	AppliedCount int                 `json:"appliedCount"`
	Verified     bool                `json:"verified"`
	Revisions    []AgentRevisionView `json:"revisions,omitempty"`
	// Amendments counts the lead's amends of the integrated head; HeadTree
	// then also holds the lead's own staged fixes. AmendReason is the latest.
	Amendments  int    `json:"amendments,omitempty"`
	AmendReason string `json:"amendReason,omitempty"`
	// Blocked explains an integration that cannot be read or an interrupted
	// apply that was not settled.
	Blocked string `json:"blocked,omitempty"`
}

// AgentRevisionView is one runtime agent's revision: State is submitted,
// applying, applied, conflict or dropped; Conflict lists the conflicting
// paths (comma separated) of a conflicted revision.
type AgentRevisionView struct {
	AgentID  string `json:"agentId"`
	State    string `json:"state"`
	Conflict string `json:"conflict,omitempty"`
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
	// Integration is set for a task with runtime agent revisions: what the
	// cancel left in the project checkout.
	Integration *AgentIntegrationReport `json:"integration,omitempty"`
}

// AgentIntegrationReport describes a cancelled task's runtime integration.
// Applied agent work stays staged in the project checkout and is never
// reverted; agent workspaces are retained.
type AgentIntegrationReport struct {
	HeadTree     string `json:"headTree"`
	AppliedCount int    `json:"appliedCount"`
	// Verified is true only when the lead's checks all passed for HeadTree.
	Verified bool `json:"verified"`
	// Applied lists the agents whose revisions are applied (staged).
	Applied []string `json:"applied,omitempty"`
	// Unintegrated lists the agents whose revisions were never applied
	// (submitted or conflict); their work stays only in their workspaces.
	Unintegrated []string `json:"unintegrated,omitempty"`
	// BlockedAgents lists the agents whose apply was interrupted and could
	// not be settled (left applying): the checkout may hold part of their
	// work, mixed with other edits.
	BlockedAgents []string `json:"blockedAgents,omitempty"`
	// Blocked explains an interrupted apply that could not be settled.
	Blocked string `json:"blocked,omitempty"`
	// Amendments counts the lead's agent.integrate amend steps: HeadTree
	// then also holds the lead's own staged fixes, not only agent work.
	Amendments int `json:"amendments,omitempty"`
	// AmendReason is the reason given for the latest amendment.
	AmendReason string `json:"amendReason,omitempty"`
	Message     string `json:"message"`
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
	// Agents, Plan and Integration are set for tasks of the agent runtime:
	// its agents in creation order, the approved plan and the integration
	// status. Legacy tasks leave them empty.
	Agents      []RuntimeAgent          `json:"agents,omitempty"`
	Plan        *AgentPlan              `json:"plan,omitempty"`
	Integration *AgentIntegrationStatus `json:"integration,omitempty"`
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
