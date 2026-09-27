package protocol

import (
	"encoding/json"
	"time"
)

// Complexity is the user-facing reasoning dial.
type Complexity string

const (
	ComplexityAuto     Complexity = "auto"
	ComplexityQuick    Complexity = "quick"
	ComplexityStandard Complexity = "standard"
	ComplexityDeep     Complexity = "deep"
)

// Valid reports whether c is a known complexity (empty counts as valid: inherit).
func (c Complexity) Valid() bool {
	switch c {
	case "", ComplexityAuto, ComplexityQuick, ComplexityStandard, ComplexityDeep:
		return true
	}
	return false
}

// ModelSelection is the provider/model/complexity/key choice for a thread or turn.
// Empty fields inherit from the next level (turn > thread > role > defaults).
type ModelSelection struct {
	Provider     string     `json:"provider,omitempty"`
	Model        string     `json:"model,omitempty"`
	Complexity   Complexity `json:"complexity,omitempty"`
	CredentialID string     `json:"credentialId,omitempty"`
}

// Thread is a durable conversation.
type Thread struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// ProjectID is the project this chat works in; empty means no project,
	// which makes the chat read-only for file and shell tools.
	ProjectID string `json:"projectId,omitempty"`
	// WorkspaceMode is "local", "worktree", or the upgrade-only "legacy" value
	// resolved by the engine when opening an older chat.
	WorkspaceMode string         `json:"workspaceMode,omitempty"`
	Channel       string         `json:"channel"`
	Pinned        bool           `json:"pinned"`
	Archived      bool           `json:"archived"`
	Settings      ModelSelection `json:"settings"`
	ForkedFrom    string         `json:"forkedFrom,omitempty"`
	Usage         UsageTotals    `json:"usage"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

// TurnStatus values.
const (
	TurnRunning     = "running"
	TurnCompleted   = "completed"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
)

// Turn is one unit of agent work.
type Turn struct {
	ID         string         `json:"id"`
	ThreadID   string         `json:"threadId"`
	Status     string         `json:"status"`
	Selection  ModelSelection `json:"selection"`            // what was requested
	Resolved   ModelSelection `json:"resolved"`             // what was actually used
	AutoPicked bool           `json:"autoPicked,omitempty"` // complexity chosen by Auto
	Error      string         `json:"error,omitempty"`
	Usage      UsageTotals    `json:"usage"`
	// RouteTrail is every route the turn tried, in order: what served it, and
	// anything it moved off and why.
	RouteTrail []RouteStep `json:"routeTrail,omitempty"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
}

// Item kinds.
const (
	ItemUserMessage       = "userMessage"
	ItemAgentMessage      = "agentMessage"
	ItemReasoning         = "reasoning"
	ItemToolCall          = "toolCall"
	ItemInboundEvent      = "inboundEvent"
	ItemApproval          = "approval"
	ItemFileChange        = "fileChange"
	ItemContextCompaction = "contextCompaction"
	ItemError             = "error"
)

// Item statuses.
const (
	ItemInProgress = "inProgress"
	ItemCompleted  = "completed"
	ItemFailed     = "failed"
	ItemDenied     = "denied"
)

// Item is one piece of a turn.
type Item struct {
	ID        string          `json:"id"`
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Text      string          `json:"text,omitempty"`
	Tool      *ToolCallData   `json:"tool,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ToolCallData describes a tool call item.
type ToolCallData struct {
	CallID string          `json:"callId"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
	Risk   string          `json:"risk,omitempty"`
}

// Attachment is an input file (image, document) sent with a turn.
type Attachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	DataB64  string `json:"dataB64"`
}

// UsageTotals aggregates token usage and cost.
type UsageTotals struct {
	InputTokens       int64   `json:"inputTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	ReasoningTokens   int64   `json:"reasoningTokens"`
	CostUSD           float64 `json:"costUsd"`
	Requests          int64   `json:"requests"`
	Estimated         bool    `json:"estimated,omitempty"`
}

// Add accumulates o into u.
func (u *UsageTotals) Add(o UsageTotals) {
	u.InputTokens += o.InputTokens
	u.CachedInputTokens += o.CachedInputTokens
	u.OutputTokens += o.OutputTokens
	u.ReasoningTokens += o.ReasoningTokens
	u.CostUSD += o.CostUSD
	u.Requests += o.Requests
	u.Estimated = u.Estimated || o.Estimated
}

// Approval is a pending or decided request to run a risky action.
type Approval struct {
	ID            string          `json:"id"`
	ThreadID      string          `json:"threadId"`
	ProjectID     string          `json:"projectId,omitempty"`
	TurnID        string          `json:"turnId"`
	ItemID        string          `json:"itemId"`
	Tool          string          `json:"tool"`
	Args          json.RawMessage `json:"args"`
	Risk          string          `json:"risk"`
	Reason        string          `json:"reason"`
	ActionSummary string          `json:"actionSummary"`
	Status        string          `json:"status"` // pending | approved | denied | expired
	DecidedBy     string          `json:"decidedBy,omitempty"`
	// Screenshot is a project-relative path to the most recent Computer Use
	// screenshot for this thread, when the approval is for a computer.* tool.
	// Fetch its bytes with project/readArtifact. Empty for every other tool.
	Screenshot string     `json:"screenshot,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	DecidedAt  *time.Time `json:"decidedAt,omitempty"`
}

// Credential is an API key record. The secret itself is never sent to clients.
type Credential struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	// Kind is "api_key" or "chatgpt" (signed in with a ChatGPT account).
	Kind             string       `json:"kind"`
	BaseURL          string       `json:"baseUrl,omitempty"`
	Last4            string       `json:"last4"`
	Enabled          bool         `json:"enabled"`
	IsDefault        bool         `json:"isDefault"`
	Fallback         bool         `json:"fallback"`
	MonthlyBudgetUSD float64      `json:"monthlyBudgetUsd,omitempty"`
	HardStop         bool         `json:"hardStop,omitempty"`
	LastTestedAt     *time.Time   `json:"lastTestedAt,omitempty"`
	LastTestOK       *bool        `json:"lastTestOk,omitempty"`
	CreatedAt        time.Time    `json:"createdAt"`
	MonthUsage       *UsageTotals `json:"monthUsage,omitempty"`
}

// Model describes a model in the catalog.
type Model struct {
	Provider           string  `json:"provider"`
	ID                 string  `json:"id"`
	DisplayName        string  `json:"displayName"`
	ContextWindow      int     `json:"contextWindow,omitempty"`
	SupportsTools      bool    `json:"supportsTools"`
	SupportsImages     bool    `json:"supportsImages"`
	SupportsReasoning  bool    `json:"supportsReasoning"`
	InputPerMTok       float64 `json:"inputPerMTok"`
	CachedInputPerMTok float64 `json:"cachedInputPerMTok"`
	OutputPerMTok      float64 `json:"outputPerMTok"`
	Hidden             bool    `json:"hidden"`
	Source             string  `json:"source"` // bundled | api | user
}

// Provider describes a configured provider.
type Provider struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Enabled      bool   `json:"enabled"`
	DefaultModel string `json:"defaultModel"`
	Credentials  int    `json:"credentials"`
}

// ComplexityPreset is the engine translation of one complexity level.
type ComplexityPreset struct {
	Level           Complexity `json:"level"`
	Reasoning       string     `json:"reasoning"`    // off | low | medium | high
	MaxToolSteps    int        `json:"maxToolSteps"` // compatibility mirror of the global round ceiling
	MultiAgent      string     `json:"multiAgent"`   // never | teamRouteOnly | allowed
	MaxOutputTokens int        `json:"maxOutputTokens"`
}

// ExecutionLimits bound a complete model/tool turn independently of reasoning level.
type ExecutionLimits struct {
	MaxDurationMinutes int     `json:"maxDurationMinutes"`
	MaxTokens          int64   `json:"maxTokens"`
	MaxCostUSD         float64 `json:"maxCostUsd"`
	MaxToolRounds      int     `json:"maxToolRounds"`
}
