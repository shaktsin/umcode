// Package llm defines a provider-neutral streaming chat interface and
// HTTP implementations for Claude, OpenAI (and OpenAI-compatible servers)
// and Gemini. No vendor SDKs: each adapter speaks the public REST API.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Role of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Part is a piece of user content.
type Part struct {
	Type     string `json:"type"` // text | image
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	DataB64  string `json:"dataB64,omitempty"`
}

// ToolCall is a model's request to call a tool.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	// Signature is provider state that must be echoed back (Gemini thought signatures).
	Signature string `json:"signature,omitempty"`
}

// Thinking is a reasoning block that some providers require to be echoed back
// within the same tool loop (Claude extended thinking).
type Thinking struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
	Redacted  string `json:"redacted,omitempty"`
}

// Message is one conversation message.
type Message struct {
	Role      Role       `json:"role"`
	Parts     []Part     `json:"parts,omitempty"`
	Thinking  []Thinking `json:"thinking,omitempty"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	// Tool result fields (Role == RoleTool).
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Result     string `json:"result,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
}

// Text returns a user or assistant message with plain text.
func Text(role Role, text string) Message {
	return Message{Role: role, Parts: []Part{{Type: "text", Text: text}}}
}

// JoinedText concatenates text parts.
func (m Message) JoinedText() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ToolSpec describes a tool offered to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Reasoning levels.
const (
	ReasoningOff    = "off"
	ReasoningLow    = "low"
	ReasoningMedium = "medium"
	ReasoningHigh   = "high"
)

// Request is a provider-neutral chat request.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int
	// Reasoning is off|low|medium|high; adapters map it to provider settings.
	// Leave empty for models that do not support reasoning.
	Reasoning string
	// ReasoningStyle tells the adapter which provider mechanism the model uses:
	//   claude: "adaptive" (thinking.type=adaptive + output_config.effort, default) or "budget"
	//   gemini: "level" (thinkingConfig.thinkingLevel, default) or "budget" (thinkingBudget)
	//   openai: ignored (reasoning_effort)
	ReasoningStyle string
}

// Usage is token usage reported by a provider for one request.
type Usage struct {
	InputTokens       int64 // all prompt tokens, including cached
	CachedInputTokens int64 // subset of InputTokens read from cache
	OutputTokens      int64 // all generated tokens, including reasoning
	ReasoningTokens   int64 // subset of OutputTokens spent on reasoning
	Reported          bool  // false if the provider returned no usage
}

// EventType enumerates stream events.
type EventType int

const (
	EventTextDelta EventType = iota
	EventReasoningDelta
	EventToolCall
	EventDone
	EventError
)

// Event is one stream event.
type Event struct {
	Type       EventType
	Text       string
	ToolCall   *ToolCall
	Thinking   *Thinking // completed thinking block (with signature), on EventReasoningDelta with Text==""
	Usage      Usage
	StopReason string
	Err        error
}

// Credential carries what an adapter needs to authenticate.
type Credential struct {
	APIKey  string
	BaseURL string
}

// Provider is implemented by each adapter.
type Provider interface {
	// ID is the canonical provider id: claude | openai | openai_compatible | gemini.
	ID() string
	// Stream sends a request and streams events. The channel is closed after
	// EventDone or EventError.
	Stream(ctx context.Context, cred Credential, req Request) (<-chan Event, error)
	// ListModels returns the model ids available to the credential.
	ListModels(ctx context.Context, cred Credential) ([]string, error)
}

// Error is a non-2xx provider response.
type Error struct {
	Provider   string
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, body)
}

// Retryable reports whether the error is a rate limit or transient server error.
func (e *Error) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == 529 || e.Status >= 500
}

// IsRateLimit reports whether err is a provider rate-limit or quota error.
func IsRateLimit(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && (pe.Status == http.StatusTooManyRequests || pe.Status == 529)
}

func readError(provider string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &Error{Provider: provider, Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

// HTTPClient is used by all adapters; tests may replace it.
var HTTPClient = &http.Client{Timeout: 10 * time.Minute}

// Registry holds adapters by id.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns a registry with the built-in adapters.
func NewRegistry() *Registry {
	r := &Registry{providers: map[string]Provider{}}
	r.Register(&Anthropic{})
	r.Register(&OpenAI{id: "openai", defaultBase: "https://api.openai.com/v1"})
	r.Register(&OpenAI{id: "openai_compatible", defaultBase: "http://localhost:11434/v1", compatible: true})
	r.Register(&Gemini{})
	return r
}

// Register adds or replaces an adapter.
func (r *Registry) Register(p Provider) { r.providers[p.ID()] = p }

// Get returns an adapter by id.
func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.providers[id]
	return p, ok
}

// IDs lists registered provider ids.
func (r *Registry) IDs() []string {
	return []string{"claude", "openai", "gemini", "openai_compatible"}
}

// EstimateTokens is a rough fallback (≈4 characters per token).
func EstimateTokens(s string) int64 { return int64(len(s)+3) / 4 }
