package protocol

import "time"

// Method names (client → engine).
const (
	MethodInitialize      = "initialize"
	MethodEngineStatus    = "engine/status"
	MethodEventsSubscribe = "events/subscribe"

	MethodThreadStart       = "thread/start"
	MethodThreadList        = "thread/list"
	MethodThreadRead        = "thread/read"
	MethodThreadRename      = "thread/rename"
	MethodThreadPin         = "thread/pin"
	MethodThreadArchive     = "thread/archive"
	MethodThreadDelete      = "thread/delete"
	MethodThreadFork        = "thread/fork"
	MethodThreadSearch      = "thread/search"
	MethodThreadExport      = "thread/export"
	MethodThreadSetSettings = "thread/setSettings"
	MethodThreadSetApproval = "thread/setApprovalMode"
	MethodThreadCompact     = "thread/compact"

	MethodTurnStart     = "turn/start"
	MethodTurnInterrupt = "turn/interrupt"
	MethodPreviewList   = "preview/list"
	MethodPreviewStop   = "preview/stop"

	MethodApprovalList    = "approval/list"
	MethodApprovalRespond = "approval/respond"

	MethodProviderList   = "provider/list"
	MethodModelList      = "model/list"
	MethodModelSetHidden = "model/setHidden"
	MethodModelRefresh   = "model/refresh"
	MethodModelSetPrice  = "model/setPrice"
	MethodRoutingGet     = "routing/get"
	MethodRoutingSet     = "routing/set"

	MethodCredentialList   = "credential/list"
	MethodCredentialAdd    = "credential/add"
	MethodCredentialTest   = "credential/test"
	MethodCredentialUpdate = "credential/update"
	MethodCredentialRotate = "credential/rotate"
	MethodCredentialDelete = "credential/delete"

	// Sign in with a ChatGPT account (OpenAI only) instead of adding an API key.
	MethodChatGPTSignInStart  = "chatgpt/signin/start"
	MethodChatGPTSignInCancel = "chatgpt/signin/cancel"

	MethodUsageSummary   = "usage/summary"
	MethodUsageSetBudget = "usage/setBudget"

	MethodComplexityGetDefaults = "complexity/getDefaults"
	MethodComplexitySetDefaults = "complexity/setDefaults"
	MethodComputerUseGetDefault = "settings/computerUse/getDefault"
	MethodComputerUseSetDefault = "settings/computerUse/setDefault"
	MethodComputerUseAct        = "computer/act"
	MethodComputerUseInspect    = "computer/inspect"
	MethodComputerUseStop       = "computer/stop"
)

// Notification names (engine → client).
const (
	NotifyChatGPTSignIn    = "chatgpt/signin/completed"
	NotifyThreadUpdated    = "thread/updated"
	NotifyTurnStarted      = "turn/started"
	NotifyTurnCompleted    = "turn/completed"
	NotifyItemStarted      = "item/started"
	NotifyItemDelta        = "item/delta"
	NotifyItemCompleted    = "item/completed"
	NotifyApprovalRequest  = "approval/request"
	NotifyApprovalResolved = "approval/resolved"
	NotifyBudgetWarning    = "usage/budgetWarning"
	NotifyPreviewStarted   = "preview/started"
	NotifyPreviewOutput    = "preview/output"
	NotifyPreviewStopped   = "preview/stopped"
)

// InitializeParams is sent first by every client.
type InitializeParams struct {
	ClientName      string `json:"clientName"`
	ClientVersion   string `json:"clientVersion"`
	ProtocolVersion string `json:"protocolVersion"`
	// Admin clients receive approval requests and may answer them.
	Admin bool `json:"admin"`
}

// InitializeResult is the engine's reply to initialize.
type InitializeResult struct {
	EngineVersion   string `json:"engineVersion"`
	ProtocolVersion string `json:"protocolVersion"`
	ClientID        string `json:"clientId"`
}

// EngineStatus is returned by engine/status.
type EngineStatus struct {
	EngineVersion    string    `json:"engineVersion"`
	BuildID          string    `json:"buildId,omitempty"`
	ProtocolVersion  string    `json:"protocolVersion"`
	StartedAt        time.Time `json:"startedAt"`
	Clients          int       `json:"clients"`
	ActiveTurns      int       `json:"activeTurns"`
	PendingApprovals int       `json:"pendingApprovals"`
	DBPath           string    `json:"dbPath"`
}

// SubscribeParams selects which threads a client wants events for.
// Empty ThreadIDs with All=true means every thread.
type SubscribeParams struct {
	ThreadIDs []string `json:"threadIds,omitempty"`
	All       bool     `json:"all,omitempty"`
}

type ThreadStartParams struct {
	Title         string `json:"title,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	WorkspaceMode string `json:"workspaceMode,omitempty"`
	// ParentThreadID marks this chat as a side chat of another one: same
	// project, its own turns, shown beside its parent.
	ParentThreadID string         `json:"parentThreadId,omitempty"`
	Channel        string         `json:"channel,omitempty"`
	Settings       ModelSelection `json:"settings,omitempty"`
	ApprovalMode   string         `json:"approvalMode,omitempty"`
}

type ThreadListParams struct {
	Archived  *bool  `json:"archived,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Before    string `json:"before,omitempty"` // updatedAt cursor (RFC3339Nano)
}

type ThreadListResult struct {
	Threads    []Thread `json:"threads"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

type ThreadIDParams struct {
	ThreadID string `json:"threadId"`
}

type PreviewStopParams struct {
	PreviewID string `json:"previewId"`
}

type PreviewSession struct {
	ID        string `json:"id"`
	ThreadID  string `json:"threadId"`
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	Output    string `json:"output,omitempty"`
}

type PreviewListResult struct {
	Previews []PreviewSession `json:"previews"`
}

type PreviewEvent struct {
	Preview PreviewSession `json:"preview"`
	Output  string         `json:"output,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type ThreadReadResult struct {
	Thread Thread `json:"thread"`
	Turns  []Turn `json:"turns"`
	Items  []Item `json:"items"`
}

type ThreadRenameParams struct {
	ThreadID string `json:"threadId"`
	Title    string `json:"title"`
}

type ThreadFlagParams struct {
	ThreadID string `json:"threadId"`
	Value    bool   `json:"value"`
}

type ThreadForkParams struct {
	ThreadID string `json:"threadId"`
	// Items up to and including this item are copied into the new thread.
	UpToItemID string `json:"upToItemId,omitempty"`
	// SideChat creates a contextual fork intended for the side-chat pane.
	SideChat bool `json:"sideChat,omitempty"`
}

type ThreadSearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type SearchHit struct {
	ThreadID string `json:"threadId"`
	Title    string `json:"title"`
	ItemID   string `json:"itemId"`
	Snippet  string `json:"snippet"`
}

type ThreadSearchResult struct {
	Hits []SearchHit `json:"hits"`
}

type ThreadExportResult struct {
	Markdown string `json:"markdown"`
}

type ThreadSetSettingsParams struct {
	ThreadID string         `json:"threadId"`
	Settings ModelSelection `json:"settings"`
}

type ThreadSetApprovalModeParams struct {
	ThreadID string `json:"threadId"`
	Mode     string `json:"mode"`
}

type ComputerUseDefaultParams struct {
	Enabled bool `json:"enabled"`
}
type ComputerUseDefaultResult struct {
	Enabled bool `json:"enabled"`
}
type ComputerUseActParams struct {
	ThreadID          string  `json:"threadId"`
	Action            string  `json:"action"`
	ObservationID     string  `json:"observation_id,omitempty"`
	ElementID         string  `json:"element_id,omitempty"`
	TargetDescription string  `json:"target_description,omitempty"`
	X                 float64 `json:"x,omitempty"`
	Y                 float64 `json:"y,omitempty"`
	Text              string  `json:"text,omitempty"`
	Key               string  `json:"key,omitempty"`
	Delta             int     `json:"delta,omitempty"`
}

type TurnStartParams struct {
	ThreadID    string         `json:"threadId"`
	Text        string         `json:"text"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	Override    ModelSelection `json:"override,omitempty"`
}

type TurnStartResult struct {
	Turn Turn `json:"turn"`
}

type TurnInterruptParams struct {
	TurnID string `json:"turnId"`
}

type ApprovalRespondParams struct {
	ApprovalID string `json:"approvalId"`
	Approve    bool   `json:"approve"`
	// Remember stores the answer for this project, so the same action is not
	// asked about again ("always allow `npm test` here").
	Remember bool `json:"remember,omitempty"`
}

type ApprovalListResult struct {
	Approvals []Approval `json:"approvals"`
}

type ProviderListResult struct {
	Providers []Provider `json:"providers"`
}

type ModelListParams struct {
	Provider      string `json:"provider,omitempty"`
	IncludeHidden bool   `json:"includeHidden,omitempty"`
}

type ModelListResult struct {
	Models []Model `json:"models"`
}

type ModelSetHiddenParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Hidden   bool   `json:"hidden"`
}

type ModelSetPriceParams struct {
	Provider           string  `json:"provider"`
	Model              string  `json:"model"`
	InputPerMTok       float64 `json:"inputPerMTok"`
	CachedInputPerMTok float64 `json:"cachedInputPerMTok"`
	OutputPerMTok      float64 `json:"outputPerMTok"`
}

type ModelRefreshParams struct {
	CredentialID string `json:"credentialId"`
}

type CredentialListResult struct {
	Credentials []Credential `json:"credentials"`
}

type CredentialAddParams struct {
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	Secret    string `json:"secret"`
	BaseURL   string `json:"baseUrl,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

type CredentialIDParams struct {
	CredentialID string `json:"credentialId"`
}

type CredentialTestResult struct {
	OK        bool     `json:"ok"`
	LatencyMs int64    `json:"latencyMs"`
	Error     string   `json:"error,omitempty"`
	Models    []string `json:"models,omitempty"`
}

type CredentialUpdateParams struct {
	CredentialID string  `json:"credentialId"`
	Label        *string `json:"label,omitempty"`
	Enabled      *bool   `json:"enabled,omitempty"`
	IsDefault    *bool   `json:"isDefault,omitempty"`
	Fallback     *bool   `json:"fallback,omitempty"`
	BaseURL      *string `json:"baseUrl,omitempty"`
}

type CredentialRotateParams struct {
	CredentialID string `json:"credentialId"`
	Secret       string `json:"secret"`
}

type UsageSummaryParams struct {
	GroupBy string    `json:"groupBy"` // credential | model | thread | role | day
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	// Optional filters.
	CredentialID string `json:"credentialId,omitempty"`
	ThreadID     string `json:"threadId,omitempty"`
}

type UsageRow struct {
	Key   string      `json:"key"`
	Label string      `json:"label"`
	Usage UsageTotals `json:"usage"`
}

type UsageSummaryResult struct {
	Rows  []UsageRow  `json:"rows"`
	Total UsageTotals `json:"total"`
}

type UsageSetBudgetParams struct {
	CredentialID     string  `json:"credentialId"`
	MonthlyBudgetUSD float64 `json:"monthlyBudgetUsd"`
	HardStop         bool    `json:"hardStop"`
}

type BudgetWarning struct {
	CredentialID string  `json:"credentialId"`
	Label        string  `json:"label"`
	SpentUSD     float64 `json:"spentUsd"`
	BudgetUSD    float64 `json:"budgetUsd"`
	Percent      float64 `json:"percent"`
}

type ComplexityDefaults struct {
	Default Complexity         `json:"default"`
	Presets []ComplexityPreset `json:"presets"`
	Limits  ExecutionLimits    `json:"limits"`
}

// ItemDelta is streamed while an item is in progress.
type ItemDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Text     string `json:"text,omitempty"`
	Output   string `json:"output,omitempty"`
}

// ItemEvent wraps an item for item/started and item/completed.
type ItemEvent struct {
	Item Item `json:"item"`
}

// TurnEvent wraps a turn for turn/started and turn/completed.
type TurnEvent struct {
	Turn Turn `json:"turn"`
}

// ThreadEvent wraps a thread for thread/updated.
type ThreadEvent struct {
	Thread Thread `json:"thread"`
}

// ApprovalEvent wraps an approval.
type ApprovalEvent struct {
	Approval Approval `json:"approval"`
}

// ChatGPTSignInStart is returned when a ChatGPT sign-in begins: the user opens
// VerificationURL in a browser and enters UserCode.
type ChatGPTSignInStart struct {
	SessionID       string `json:"sessionId"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
	ExpiresInSec    int    `json:"expiresInSec"`
}

type ChatGPTSignInCancelParams struct {
	SessionID string `json:"sessionId"`
}

// ChatGPTSignInResult is the notification sent when a sign-in ends.
type ChatGPTSignInResult struct {
	SessionID  string      `json:"sessionId"`
	OK         bool        `json:"ok"`
	Error      string      `json:"error,omitempty"`
	Credential *Credential `json:"credential,omitempty"`
}
