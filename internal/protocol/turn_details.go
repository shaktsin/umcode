package protocol

// TurnDetailsParams scopes inspection to the turn's owning chat.
type TurnDetailsParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}
type TurnDetailsResult struct {
	Turn         Turn             `json:"turn"`
	Diagnostics  *TurnDiagnostics `json:"diagnostics"`
	Availability string           `json:"availability"`
}

// OptimizationSnapshot records effective admission policy, not today's settings.
type OptimizationSnapshot struct {
	Source             string `json:"source"`
	ContextCompiler    bool   `json:"contextCompiler"`
	ContextRetrieval   bool   `json:"contextRetrieval"`
	ProgressiveTools   bool   `json:"progressiveTools"`
	ToolResultReducers bool   `json:"toolResultReducers"`
	DesignedWorkflow   bool   `json:"designedWorkflow"`
	AutoPromote        bool   `json:"autoPromote"`
	AutomaticWorkflow  bool   `json:"automaticWorkflow"`
}
type ContextSourceRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// Nullable measurements preserve unknown versus measured zero. No prompt or
// tool-output bodies belong in this persisted diagnostic schema.
type RequestDiagnostics struct {
	CapacityWindow          *int               `json:"capacityWindow,omitempty"`
	CapacitySource          string             `json:"capacitySource,omitempty"`
	InputEstimateTokens     *int               `json:"inputEstimateTokens,omitempty"`
	InputEstimateOrigin     string             `json:"inputEstimateOrigin,omitempty"`
	ReservedOutputTokens    *int               `json:"reservedOutputTokens,omitempty"`
	CapacityMargin          *int               `json:"capacityMargin,omitempty"`
	CalibrationFactor       *float64           `json:"calibrationFactor,omitempty"`
	ContextReductions       *int               `json:"contextReductions,omitempty"`
	RetryCause              string             `json:"retryCause,omitempty"`
	InterruptionKind        string             `json:"interruptionKind,omitempty"`
	Attempted               *bool              `json:"attempted,omitempty"`
	StopReason              string             `json:"stopReason,omitempty"`
	Ordinal                 int                `json:"ordinal"`
	Phase                   string             `json:"phase"`
	Provider                string             `json:"provider"`
	Model                   string             `json:"model"`
	InputTokens             *int64             `json:"inputTokens"`
	CachedInputTokens       *int64             `json:"cachedInputTokens"`
	OutputTokens            *int64             `json:"outputTokens"`
	ReasoningTokens         *int64             `json:"reasoningTokens"`
	CostUSD                 *float64           `json:"costUsd"`
	CostState               string             `json:"costState"`
	MeasurementOrigin       string             `json:"measurementOrigin"`
	SelectedSources         []ContextSourceRef `json:"selectedSources,omitempty"`
	SubmittedSources        []ContextSourceRef `json:"submittedSources,omitempty"`
	ToolNames               []string           `json:"toolNames,omitempty"`
	ToolCalls               map[string]int     `json:"toolCalls,omitempty"`
	Estimates               map[string]int     `json:"estimates,omitempty"`
	Fallback                string             `json:"fallback,omitempty"`
	OmittedSelectedSources  int                `json:"omittedSelectedSources"`
	OmittedSubmittedSources int                `json:"omittedSubmittedSources"`
	OmittedTools            int                `json:"omittedTools"`
}
type TurnDiagnostics struct {
	Version         int                  `json:"version"`
	Policy          OptimizationSnapshot `json:"policy"`
	Complete        bool                 `json:"complete"`
	Requests        []RequestDiagnostics `json:"requests"`
	OmittedRequests int                  `json:"omittedRequests"`
}
