package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Anthropic speaks the Claude Messages API.
type Anthropic struct{}

func (*Anthropic) ID() string { return "claude" }

const anthropicVersion = "2023-06-01"

func anthropicBase(cred Credential) string {
	if cred.BaseURL != "" {
		return strings.TrimRight(cred.BaseURL, "/")
	}
	return "https://api.anthropic.com"
}

// thinkingBudget maps a reasoning level to Claude's extended-thinking budget.
func thinkingBudget(level string) int {
	switch level {
	case ReasoningLow:
		return 2048
	case ReasoningMedium:
		return 8192
	case ReasoningHigh:
		return 24576
	}
	return 0
}

type anthBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *anthSource     `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Thinking  *string         `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
}

type anthSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthMessage struct {
	Role    string      `json:"role"`
	Content []anthBlock `json:"content"`
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       string            `json:"system,omitempty"`
	Messages     []anthMessage     `json:"messages"`
	Tools        []anthTool        `json:"tools,omitempty"`
	Stream       bool              `json:"stream"`
	Thinking     *anthThinking     `json:"thinking,omitempty"`
	OutputConfig *anthOutputConfig `json:"output_config,omitempty"`
}

type anthThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

func buildAnthropicRequest(req Request) anthRequest {
	out := anthRequest{Model: req.Model, System: req.System, Stream: true, MaxTokens: req.MaxTokens}
	if out.MaxTokens <= 0 {
		out.MaxTokens = 8192
	}
	switch {
	case req.ReasoningStyle == "budget":
		if b := thinkingBudget(req.Reasoning); b > 0 {
			// max_tokens is an admitted wire cap, including thinking. Never
			// increase it behind capacity or shared-budget accounting.
			b = min(b, out.MaxTokens-1024)
			if b >= 1024 {
				out.Thinking = &anthThinking{Type: "enabled", BudgetTokens: b}
			}
		}
	case req.Reasoning == ReasoningLow || req.Reasoning == ReasoningMedium || req.Reasoning == ReasoningHigh:
		// Current Claude models: adaptive thinking steered by effort.
		out.Thinking = &anthThinking{Type: "adaptive"}
		out.OutputConfig = &anthOutputConfig{Effort: req.Reasoning}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, anthTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			var blocks []anthBlock
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					blocks = append(blocks, anthBlock{Type: "text", Text: p.Text})
				case "image":
					blocks = append(blocks, anthBlock{Type: "image", Source: &anthSource{Type: "base64", MediaType: p.MimeType, Data: p.DataB64}})
				}
			}
			out.Messages = appendAnth(out.Messages, "user", blocks)
		case RoleAssistant:
			var blocks []anthBlock
			for _, th := range m.Thinking {
				if th.Redacted != "" {
					blocks = append(blocks, anthBlock{Type: "redacted_thinking", Data: th.Redacted})
				} else if th.Signature != "" {
					blocks = append(blocks, anthBlock{Type: "thinking", Thinking: &th.Text, Signature: th.Signature})
				}
			}
			if txt := m.JoinedText(); txt != "" {
				blocks = append(blocks, anthBlock{Type: "text", Text: txt})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Args
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				blocks = append(blocks, anthBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: args})
			}
			if len(blocks) > 0 {
				out.Messages = appendAnth(out.Messages, "assistant", blocks)
			}
		case RoleTool:
			out.Messages = appendAnth(out.Messages, "user", []anthBlock{{
				Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Result, IsError: m.IsError}})
		}
	}
	return out
}

// appendAnth merges consecutive same-role messages (Claude requires alternation).
func appendAnth(msgs []anthMessage, role string, blocks []anthBlock) []anthMessage {
	if n := len(msgs); n > 0 && msgs[n-1].Role == role {
		msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
		return msgs
	}
	return append(msgs, anthMessage{Role: role, Content: blocks})
}

func (a *Anthropic) Stream(ctx context.Context, cred Credential, req Request) (<-chan Event, error) {
	body, err := json.Marshal(buildAnthropicRequest(req))
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicBase(cred)+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("content-type", "application/json")
	hreq.Header.Set("x-api-key", cred.APIKey)
	hreq.Header.Set("anthropic-version", anthropicVersion)
	resp, err := HTTPClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, readError("claude", resp)
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		type block struct {
			kind      string
			id, name  string
			args      strings.Builder
			thinking  strings.Builder
			signature string
			redacted  string
		}
		blocks := map[int]*block{}
		var usage Usage
		var inputBase, cacheRead, cacheWrite int64
		stop := ""
		var streamErr error
		err := readSSE(resp.Body, func(ev sseEvent) bool {
			var m struct {
				Type    string `json:"type"`
				Index   int    `json:"index"`
				Message struct {
					Usage struct {
						InputTokens              int64 `json:"input_tokens"`
						CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
						CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
						OutputTokens             int64 `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
					Data string `json:"data"`
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					Thinking    string `json:"thinking"`
					Signature   string `json:"signature"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if ev.Data == "" {
				return true
			}
			if err := json.Unmarshal([]byte(ev.Data), &m); err != nil {
				streamErr = fmt.Errorf("claude: bad event: %w", err)
				return false
			}
			switch m.Type {
			case "message_start":
				u := m.Message.Usage
				inputBase, cacheRead, cacheWrite = u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens
				usage.OutputTokens = u.OutputTokens
				usage.InputTokens = inputBase + cacheRead + cacheWrite
				usage.CachedInputTokens = cacheRead
				usage.Reported = true
			case "content_block_start":
				blocks[m.Index] = &block{kind: m.ContentBlock.Type, id: m.ContentBlock.ID, name: m.ContentBlock.Name, redacted: m.ContentBlock.Data}
			case "content_block_delta":
				b := blocks[m.Index]
				if b == nil {
					b = &block{kind: "text"}
					blocks[m.Index] = b
				}
				switch m.Delta.Type {
				case "text_delta":
					ch <- Event{Type: EventTextDelta, Text: m.Delta.Text}
				case "input_json_delta":
					b.args.WriteString(m.Delta.PartialJSON)
				case "thinking_delta":
					b.thinking.WriteString(m.Delta.Thinking)
					ch <- Event{Type: EventReasoningDelta, Text: m.Delta.Thinking}
				case "signature_delta":
					b.signature += m.Delta.Signature
				}
			case "content_block_stop":
				b := blocks[m.Index]
				if b == nil {
					return true
				}
				switch b.kind {
				case "tool_use":
					args := b.args.String()
					if strings.TrimSpace(args) == "" {
						args = "{}"
					}
					ch <- Event{Type: EventToolCall, ToolCall: &ToolCall{ID: b.id, Name: b.name, Args: json.RawMessage(args)}}
				case "thinking":
					ch <- Event{Type: EventReasoningDelta, Thinking: &Thinking{Text: b.thinking.String(), Signature: b.signature}}
				case "redacted_thinking":
					ch <- Event{Type: EventReasoningDelta, Thinking: &Thinking{Redacted: b.redacted}}
				}
			case "message_delta":
				if m.Delta.StopReason != "" {
					stop = m.Delta.StopReason
				}
				if m.Usage.OutputTokens > 0 {
					usage.OutputTokens = m.Usage.OutputTokens
				}
			case "error":
				streamErr = fmt.Errorf("claude: %s: %s", m.Error.Type, m.Error.Message)
				return false
			}
			return true
		})
		if err == nil {
			err = streamErr
		}
		if err != nil {
			ch <- Event{Type: EventError, Err: err, Usage: usage}
			return
		}
		usage.InputTokens = inputBase + cacheRead + cacheWrite
		usage.CachedInputTokens = cacheRead
		usage.Reported = true
		ch <- Event{Type: EventDone, Usage: usage, StopReason: stop}
	}()
	return ch, nil
}

func (a *Anthropic) ListModels(ctx context.Context, cred Credential) ([]string, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicBase(cred)+"/v1/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("x-api-key", cred.APIKey)
	hreq.Header.Set("anthropic-version", anthropicVersion)
	return listIDs(hreq, "claude", func(b []byte) ([]string, error) {
		var r struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(r.Data))
		for _, d := range r.Data {
			ids = append(ids, d.ID)
		}
		return ids, nil
	})
}
