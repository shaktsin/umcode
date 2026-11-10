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
	// Requested is what the lead asked for when Capabilities (the grant shown
	// on the plan card) was clamped by agent.propose_plan; it lets the card's
	// rejections be rebuilt from the stored plan. Nil means Capabilities is
	// the request.
	Requested *AgentCapabilities `json:"requested,omitempty"`
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

// Normalized fills zero or negative numeric limits from DefaultPlanLimits, so a
// plan that sets only some limits (propose_plan carries maxConcurrent,
// sliceShare and network) never disables a rail. Network is kept as given.
func (l PlanLimits) Normalized() PlanLimits {
	def := DefaultPlanLimits()
	if l.MaxConcurrent <= 0 {
		l.MaxConcurrent = def.MaxConcurrent
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = def.MaxDepth
	}
	if l.MaxTotal <= 0 {
		l.MaxTotal = def.MaxTotal
	}
	if !(l.SliceShare > 0) {
		l.SliceShare = def.SliceShare
	}
	return l
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

// PlanCard is the payload of an agent_plan approval (agent.propose_plan): the
// stored proposed plan with each entry's clamped grant, the task's model pool
// and every notice to show. It is rebuilt from the stored plan and the pool.
type PlanCard struct {
	PlanID   string               `json:"planId"`
	Agents   []PlanCardAgent      `json:"agents"`
	Limits   PlanLimits           `json:"limits"`
	Models   []PlanCardModel      `json:"models"`
	Notices  []string             `json:"notices"`
	Previous []PreviousAssignment `json:"previous"`
}

// PlanCardAgent is one plan entry on the card: the proposal, what it is
// granted after the capability clamp and what was rejected.
type PlanCardAgent struct {
	PlanAgent
	Granted  AgentCapabilities     `json:"granted"`
	Rejected []CapabilityRejection `json:"rejected"`
}

// PlanCardModel is one model of the task's approved pool offered on the card.
// Name is the model reference entries use (provider/model).
type PlanCardModel struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	ToolsOK   bool   `json:"toolsOk"`
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
}

// PreviousAssignment is a model assignment of the thread's latest approved
// plan, used to prefill entries of the same name.
type PreviousAssignment struct {
	PlanID string `json:"planId"`
	Name   string `json:"name"`
	Model  string `json:"model"`
}

// RuntimeAgent is a persisted agent of the agent runtime.
type RuntimeAgent struct {
	ID            string            `json:"id"`
	TaskID        string            `json:"taskId"`
	ParentAgentID string            `json:"parentAgentId,omitempty"`
	Name          string            `json:"name"`
	Purpose       string            `json:"purpose"`
	PlanID        string            `json:"planId,omitempty"`
	PlanEntry     string            `json:"planEntry,omitempty"`
	Assignment    AgentAssignment   `json:"assignment"`
	Capabilities  AgentCapabilities `json:"capabilities"`
	// Rejected is admission's list of requested capabilities that were not
	// granted, kept so an idempotent spawn replay reports it again.
	Rejected       []CapabilityRejection `json:"rejected,omitempty"`
	ModelRequested string                `json:"modelRequested,omitempty"`
	ModelActual    string                `json:"modelActual,omitempty"`
	RouteTrail     []string              `json:"routeTrail,omitempty"`
	BudgetShare    float64               `json:"budgetShare"`
	Depth          int                   `json:"depth"`
	ContextMode    string                `json:"contextMode,omitempty"`
	WorkspaceID    string                `json:"workspaceId,omitempty"`
	Revision       string                `json:"revision,omitempty"`
	State          AgentState            `json:"state"`
	Result         AgentResult           `json:"result"`
	Usage          UsageTotals           `json:"usage"`
	Version        int64                 `json:"version"`
	CreatedAt      time.Time             `json:"createdAt"`
	UpdatedAt      time.Time             `json:"updatedAt"`
}

// AgentMessage is a message between runtime agents.
type AgentMessage struct {
	ID          string `json:"id"`
	TaskID      string `json:"taskId"`
	FromAgentID string `json:"fromAgentId"`
	ToAgentID   string `json:"toAgentId"`
	Body        string `json:"body"`
	// CallID is the sender's tool call identity; a repeated (task, call ID)
	// returns the stored message instead of queuing a duplicate. Empty means
	// no deduplication.
	CallID      string     `json:"-"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
}

// CapabilityRejection explains why a requested capability was refused.
type CapabilityRejection struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
