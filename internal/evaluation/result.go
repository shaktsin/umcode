package evaluation

import (
	"context"
	"github.com/shaktsin/umcode/internal/optimization"
)

type Runner interface {
	Run(context.Context, RunInput) (TaskResult, error)
}
type RunInput struct {
	Manifest      Manifest
	Unit          Unit
	Workspace     string
	Live          bool
	Credential    string
	AttemptSink   func(Attempt) error
	BeforeAttempt func(context.Context, string, float64) error
}
type TaskResult struct {
	Unit              Unit                  `json:"unit"`
	ConfigurationHash string                `json:"configuration_hash"`
	ArtifactHash      string                `json:"artifact_hash"`
	Status            string                `json:"status"`
	Response          string                `json:"response"`
	Attempts          []Attempt             `json:"attempts"`
	Checks            []CheckResult         `json:"checks"`
	Policies          []optimization.Policy `json:"policies"`
	IncompleteReasons []string              `json:"incomplete_reasons,omitempty"`
	ArtifactDir       string                `json:"artifact_dir"`
}
type Attempt struct {
	ID             string           `json:"id"`
	Provider       string           `json:"provider"`
	Model          string           `json:"model"`
	PriceID        string           `json:"price_id"`
	Reported       bool             `json:"reported"`
	Subscription   bool             `json:"subscription"`
	Synthetic      bool             `json:"synthetic"`
	Input          *int64           `json:"input"`
	Cached         *int64           `json:"cached"`
	Output         *int64           `json:"output"`
	Reasoning      *int64           `json:"reasoning"`
	ReservedUSD    float64          `json:"reserved_usd"`
	ObservedUSD    *float64         `json:"observed_usd"`
	Outcome        string           `json:"outcome"`
	ElapsedMS      int64            `json:"elapsed_ms"`
	ToolRounds     int64            `json:"tool_rounds"`
	SchemaTokens   int64            `json:"schema_tokens"`
	LayerEstimates map[string]int64 `json:"layer_estimates,omitempty"`
	MissingReason  string           `json:"missing_reason,omitempty"`
}
type CheckResult struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	EvidenceHash      string `json:"evidence_hash"`
	Output            string `json:"output,omitempty"`
	Safety            bool   `json:"safety"`
	SafetyViolations  *int   `json:"safety_violations"`
	UnsupportedClaims *int   `json:"unsupported_claims"`
	ContextOmissions  *int   `json:"context_omissions"`
	StaleAuthority    *int   `json:"stale_authority"`
}
