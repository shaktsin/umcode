package protocol

import "time"

// FileAccess is the file capability granted to a runtime agent.
type FileAccess string

const (
	FilesNone      FileAccess = "none"
	FilesRead      FileAccess = "read"
	FilesReadWrite FileAccess = "read-write"
)

// ShellAccess is the shell capability granted to a runtime agent.
type ShellAccess string

const (
	ShellNone      ShellAccess = "none"
	ShellReadOnly  ShellAccess = "read-only"
	ShellReadWrite ShellAccess = "read-write"
)

// AgentCapabilities bounds what a runtime agent may do.
type AgentCapabilities struct {
	Files    FileAccess  `json:"files"`
	Shell    ShellAccess `json:"shell"`
	Network  bool        `json:"network"`
	SpawnMax int         `json:"spawnMax"`
}

// PlanAgent is one proposed agent inside an AgentPlan.
type PlanAgent struct {
	Name         string            `json:"name"`
	Purpose      string            `json:"purpose"`
	Summary      string            `json:"summary"`
	Capabilities AgentCapabilities `json:"capabilities"`
	Model        string            `json:"model,omitempty"`
}

// PlanLimits bounds a plan's agent tree.
type PlanLimits struct {
	MaxConcurrent int     `json:"maxConcurrent"`
	MaxDepth      int     `json:"maxDepth"`
	MaxTotal      int     `json:"maxTotal"`
	SliceShare    float64 `json:"sliceShare"`
	Network       bool    `json:"network"`
}

// DefaultPlanLimits returns the limits used when a plan does not set its own.
func DefaultPlanLimits() PlanLimits {
	return PlanLimits{MaxConcurrent: 8, MaxDepth: 3, MaxTotal: 40, SliceShare: 0.25, Network: false}
}

// PlanState is the approval lifecycle of an AgentPlan.
type PlanState string

const (
	PlanProposed   PlanState = "proposed"
	PlanApproved   PlanState = "approved"
	PlanSuperseded PlanState = "superseded"
)

// AgentPlan is a proposed or approved set of runtime agents for a task.
type AgentPlan struct {
	ID         string      `json:"id"`
	ThreadID   string      `json:"threadId"`
	TaskID     string      `json:"taskId"`
	Agents     []PlanAgent `json:"agents"`
	Limits     PlanLimits  `json:"limits"`
	State      PlanState   `json:"state"`
	CreatedAt  time.Time   `json:"createdAt"`
	ApprovedAt *time.Time  `json:"approvedAt,omitempty"`
}

// RuntimeAgent is a persisted agent of the agent runtime.
type RuntimeAgent struct {
	ID             string            `json:"id"`
	TaskID         string            `json:"taskId"`
	ParentAgentID  string            `json:"parentAgentId,omitempty"`
	Name           string            `json:"name"`
	Purpose        string            `json:"purpose"`
	PlanID         string            `json:"planId,omitempty"`
	Assignment     AgentAssignment   `json:"assignment"`
	Capabilities   AgentCapabilities `json:"capabilities"`
	ModelRequested string            `json:"modelRequested,omitempty"`
	ModelActual    string            `json:"modelActual,omitempty"`
	RouteTrail     []string          `json:"routeTrail,omitempty"`
	BudgetShare    float64           `json:"budgetShare"`
	Depth          int               `json:"depth"`
	ContextMode    string            `json:"contextMode,omitempty"`
	WorkspaceID    string            `json:"workspaceId,omitempty"`
	Revision       string            `json:"revision,omitempty"`
	State          AgentState        `json:"state"`
	Result         AgentResult       `json:"result"`
	Usage          UsageTotals       `json:"usage"`
	Version        int64             `json:"version"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// AgentMessage is a message between runtime agents.
type AgentMessage struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"taskId"`
	FromAgentID string     `json:"fromAgentId"`
	ToAgentID   string     `json:"toAgentId"`
	Body        string     `json:"body"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
}

// CapabilityRejection explains why a requested capability was refused.
type CapabilityRejection struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
