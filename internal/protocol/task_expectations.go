package protocol

import "encoding/json"

// TaskSourceSpan is a half-open rune range in a persisted user message.
type TaskSourceSpan struct {
	ItemID string `json:"itemId"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}
type TaskExpectationProposal struct {
	Source        TaskSourceSpan  `json:"source"`
	Kind          string          `json:"kind"`
	Predicate     string          `json:"predicate"`
	Resource      json.RawMessage `json:"resource"`
	AllowExisting bool            `json:"allowExisting,omitempty"`
}
type TaskExpectation struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"taskId"`
	OriginItemID  string          `json:"originItemId"`
	RequestTurnID string          `json:"requestTurnId"`
	Revision      int             `json:"revision"`
	Source        TaskSourceSpan  `json:"source"`
	Kind          string          `json:"kind"`
	Predicate     string          `json:"predicate"`
	Status        string          `json:"status"`
	Resource      json.RawMessage `json:"resource"`
	AllowExisting bool            `json:"allowExisting,omitempty"`
	EvidenceIDs   []string        `json:"evidenceIds,omitempty"`
}
type TaskResource struct {
	Type           string `json:"type,omitempty"`
	Path           string `json:"path,omitempty"`
	RequestedPath  string `json:"requestedPath,omitempty"`
	Tool           string `json:"tool,omitempty"`
	Target         string `json:"target,omitempty"`
	ExpectedSHA256 string `json:"expectedSHA256,omitempty"`
}
type TaskIntentProposal struct {
	TaskID           string                    `json:"taskId"`
	ExpectedRevision int                       `json:"expectedRevision"`
	OriginItemID     string                    `json:"originItemId"`
	Relation         string                    `json:"relation"`
	Expectations     []TaskExpectationProposal `json:"expectations"`
	Question         string                    `json:"question,omitempty"`
}
type TaskExpectationEvaluation struct {
	Status      string         `json:"status"`
	Missing     []string       `json:"missing,omitempty"`
	EvidenceIDs []string       `json:"evidenceIds,omitempty"`
	Failure     *ActionFailure `json:"failure,omitempty"`
}
