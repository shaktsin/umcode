package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// OpenAI speaks the Chat Completions API. With compatible=true it targets
// OpenAI-compatible servers (Ollama, LM Studio, OpenRouter, vLLM).
type OpenAI struct {
	id          string
	defaultBase string
	compatible  bool
}

func (o *OpenAI) ID() string { return o.id }

func (o *OpenAI) base(cred Credential) string {
	if cred.BaseURL != "" {
		return strings.TrimRight(cred.BaseURL, "/")
	}
	return o.defaultBase
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaContentPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL string `json:"url"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func (o *OpenAI) buildRequest(req Request) map[string]any {
	var msgs []oaMessage
	if req.System != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			hasImage := false
			for _, p := range m.Parts {
				if p.Type == "image" {
					hasImage = true
				}
			}
			if !hasImage {
				msgs = append(msgs, oaMessage{Role: "user", Content: m.JoinedText()})
				continue
			}
			var parts []oaContentPart
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					parts = append(parts, oaContentPart{Type: "text", Text: p.Text})
				case "image":
					parts = append(parts, oaContentPart{Type: "image_url",
						ImageURL: &oaImageURL{URL: "data:" + p.MimeType + ";base64," + p.DataB64}})
				}
			}
			msgs = append(msgs, oaMessage{Role: "user", Content: parts})
		case RoleAssistant:
			am := oaMessage{Role: "assistant"}
			if t := m.JoinedText(); t != "" {
				am.Content = t
			}
			for _, tc := range m.ToolCalls {
				var c oaToolCall
				c.ID, c.Type = tc.ID, "function"
				c.Function.Name = tc.Name
				c.Function.Arguments = string(tc.Args)
				if c.Function.Arguments == "" {
					c.Function.Arguments = "{}"
				}
				am.ToolCalls = append(am.ToolCalls, c)
			}
			msgs = append(msgs, am)
		case RoleTool:
			msgs = append(msgs, oaMessage{Role: "tool", ToolCallID: m.ToolCallID, Content: m.Result})
		}
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
	}
	if len(req.Tools) > 0 {
		tools := make([]oaTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			var ot oaTool
			ot.Type = "function"
			ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.Schema
			tools = append(tools, ot)
		}
		body["tools"] = tools
	}
	if req.MaxTokens > 0 {
		if o.compatible {
			body["max_tokens"] = req.MaxTokens
		} else {
			body["max_completion_tokens"] = req.MaxTokens
		}
	}
	if len(req.Tools) > 0 && req.Reasoning == ReasoningOff {
		// Quick/Auto turns explicitly disable reasoning. Omitting this field lets
		// some reasoning models apply their default effort, which Chat Completions
		// rejects when function tools are present.
		body["reasoning_effort"] = "none"
	}
	switch req.Reasoning {
	case ReasoningLow, ReasoningMedium, ReasoningHigh:
		// Reasoning models on Chat Completions reject non-none reasoning_effort
		// when function tools are present. Apply this to OpenAI-compatible
		// endpoints too: proxies often forward OpenAI's validation unchanged.
		// Keep tool use available; use the requested effort on turns without
		// tools. The Responses API can preserve both, but this adapter currently
		// uses Chat Completions.
		effort := req.Reasoning
		if len(req.Tools) > 0 {
			effort = "none"
		}
		body["reasoning_effort"] = effort
	}
	return body
}

func (o *OpenAI) Stream(ctx context.Context, cred Credential, req Request) (<-chan Event, error) {
	if cred.Kind == KindChatGPT {
		return o.streamChatGPT(ctx, cred, req)
	}
	body, err := json.Marshal(o.buildRequest(req))
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base(cred)+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if cred.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+cred.APIKey)
	}
	resp, err := HTTPClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, readError(o.id, resp)
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		type pending struct {
			id, name string
			args     strings.Builder
		}
		calls := map[int]*pending{}
		var usage Usage
		stop := ""
		var streamErr error
		err := readSSE(resp.Body, func(ev sseEvent) bool {
			if ev.Data == "" {
				return true
			}
			if ev.Data == "[DONE]" {
				return false
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
						Reasoning        string `json:"reasoning"`
						ToolCalls        []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens        int64 `json:"prompt_tokens"`
					CompletionTokens    int64 `json:"completion_tokens"`
					PromptTokensDetails struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
					CompletionTokensDetails struct {
						ReasoningTokens int64 `json:"reasoning_tokens"`
					} `json:"completion_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
				streamErr = fmt.Errorf("%s: bad chunk: %w", o.id, err)
				return false
			}
			if chunk.Error != nil {
				streamErr = fmt.Errorf("%s: %s", o.id, chunk.Error.Message)
				return false
			}
			if chunk.Usage != nil {
				usage = Usage{
					InputTokens:       chunk.Usage.PromptTokens,
					CachedInputTokens: chunk.Usage.PromptTokensDetails.CachedTokens,
					OutputTokens:      chunk.Usage.CompletionTokens,
					ReasoningTokens:   chunk.Usage.CompletionTokensDetails.ReasoningTokens,
					Reported:          true,
				}
			}
			for _, c := range chunk.Choices {
				if c.Delta.Content != "" {
					ch <- Event{Type: EventTextDelta, Text: c.Delta.Content}
				}
				if r := c.Delta.ReasoningContent + c.Delta.Reasoning; r != "" {
					ch <- Event{Type: EventReasoningDelta, Text: r}
				}
				for _, tc := range c.Delta.ToolCalls {
					p := calls[tc.Index]
					if p == nil {
						p = &pending{}
						calls[tc.Index] = p
					}
					if tc.ID != "" {
						p.id = tc.ID
					}
					if tc.Function.Name != "" {
						p.name = tc.Function.Name
					}
					p.args.WriteString(tc.Function.Arguments)
				}
				if c.FinishReason != nil && *c.FinishReason != "" {
					stop = *c.FinishReason
				}
			}
			return true
		})
		if err == nil {
			err = streamErr
		}
		if err != nil {
			ch <- Event{Type: EventError, Err: err}
			return
		}
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for _, i := range idx {
			p := calls[i]
			args := p.args.String()
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			id := p.id
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			ch <- Event{Type: EventToolCall, ToolCall: &ToolCall{ID: id, Name: p.name, Args: json.RawMessage(args)}}
		}
		ch <- Event{Type: EventDone, Usage: usage, StopReason: stop}
	}()
	return ch, nil
}

func (o *OpenAI) ListModels(ctx context.Context, cred Credential) ([]string, error) {
	if cred.Kind == KindChatGPT {
		return o.listChatGPTModels(ctx, cred)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base(cred)+"/models", nil)
	if err != nil {
		return nil, err
	}
	if cred.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+cred.APIKey)
	}
	return listIDs(hreq, o.id, func(b []byte) ([]string, error) {
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

func listIDs(hreq *http.Request, provider string, parse func([]byte) ([]string, error)) ([]string, error) {
	resp, err := HTTPClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, readError(provider, resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	ids, err := parse(b)
	if err != nil {
		return nil, errors.New(provider + ": unexpected model list response")
	}
	sort.Strings(ids)
	return ids, nil
}
