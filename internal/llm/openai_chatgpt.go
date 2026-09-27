package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ChatGPTBase is the backend that serves Codex-style requests for a ChatGPT
// sign-in. Tests may override it.
var ChatGPTBase = "https://chatgpt.com/backend-api/codex"

// ChatGPTFallbackModels is offered when the backend's model list can't be read.
var ChatGPTFallbackModels = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}

func chatGPTBase(cred Credential) string {
	if cred.BaseURL != "" {
		return strings.TrimRight(cred.BaseURL, "/")
	}
	return ChatGPTBase
}

func setChatGPTHeaders(h http.Header, cred Credential) {
	h.Set("Authorization", "Bearer "+cred.APIKey)
	if cred.AccountID != "" {
		h.Set("chatgpt-account-id", cred.AccountID)
	}
	h.Set("OpenAI-Beta", "responses=experimental")
	h.Set("originator", "umcode")
}

// buildResponsesRequest converts a Request to a Responses API body.
func buildResponsesRequest(req Request) map[string]any {
	var input []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			var parts []map[string]any
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					parts = append(parts, map[string]any{"type": "input_text", "text": p.Text})
				case "image":
					parts = append(parts, map[string]any{"type": "input_image",
						"image_url": "data:" + p.MimeType + ";base64," + p.DataB64})
				}
			}
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"type": "input_text", "text": ""})
			}
			input = append(input, map[string]any{"type": "message", "role": "user", "content": parts})
		case RoleAssistant:
			if t := m.JoinedText(); t != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": t}}})
			}
			for _, tc := range m.ToolCalls {
				args := string(tc.Args)
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": args})
			}
		case RoleTool:
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Result})
		}
	}
	body := map[string]any{
		"model":               req.Model,
		"instructions":        req.System,
		"input":               input,
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
		"store":               false,
		"stream":              true,
		"include":             []string{},
	}
	if body["instructions"] == "" {
		body["instructions"] = "You are a helpful assistant."
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{"type": "function", "name": t.Name,
				"description": t.Description, "parameters": schema, "strict": false})
		}
		body["tools"] = tools
	}
	switch req.Reasoning {
	case ReasoningLow, ReasoningMedium, ReasoningHigh:
		body["reasoning"] = map[string]any{"effort": req.Reasoning, "summary": "auto"}
	}
	return body
}

func (o *OpenAI) streamChatGPT(ctx context.Context, cred Credential, req Request) (<-chan Event, error) {
	body, err := json.Marshal(buildResponsesRequest(req))
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGPTBase(cred)+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	setChatGPTHeaders(hreq.Header, cred)
	resp, err := HTTPClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, readError("openai (ChatGPT sign-in)", resp)
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		var usage Usage
		var streamErr error
		calls := 0
		done := false
		err := readSSE(resp.Body, func(ev sseEvent) bool {
			if ev.Data == "" || ev.Data == "[DONE]" {
				return ev.Data != "[DONE]"
			}
			var e struct {
				Type    string `json:"type"`
				Delta   string `json:"delta"`
				Message string `json:"message"`
				Item    struct {
					Type      string `json:"type"`
					CallID    string `json:"call_id"`
					ID        string `json:"id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"item"`
				Response struct {
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
					IncompleteDetails *struct {
						Reason string `json:"reason"`
					} `json:"incomplete_details"`
					Usage *struct {
						InputTokens        int64 `json:"input_tokens"`
						OutputTokens       int64 `json:"output_tokens"`
						InputTokensDetails struct {
							CachedTokens int64 `json:"cached_tokens"`
						} `json:"input_tokens_details"`
						OutputTokensDetails struct {
							ReasoningTokens int64 `json:"reasoning_tokens"`
						} `json:"output_tokens_details"`
					} `json:"usage"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
				streamErr = fmt.Errorf("%s: bad chunk: %w", o.id, err)
				return false
			}
			switch e.Type {
			case "response.output_text.delta":
				if e.Delta != "" {
					ch <- Event{Type: EventTextDelta, Text: e.Delta}
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				if e.Delta != "" {
					ch <- Event{Type: EventReasoningDelta, Text: e.Delta}
				}
			case "response.output_item.done":
				if e.Item.Type == "function_call" {
					args := strings.TrimSpace(e.Item.Arguments)
					if args == "" {
						args = "{}"
					}
					id := e.Item.CallID
					if id == "" {
						id = e.Item.ID
					}
					calls++
					ch <- Event{Type: EventToolCall, ToolCall: &ToolCall{ID: id, Name: e.Item.Name, Args: json.RawMessage(args)}}
				}
			case "response.completed", "response.incomplete":
				if u := e.Response.Usage; u != nil {
					usage = Usage{InputTokens: u.InputTokens, CachedInputTokens: u.InputTokensDetails.CachedTokens,
						OutputTokens: u.OutputTokens, ReasoningTokens: u.OutputTokensDetails.ReasoningTokens, Reported: true}
				}
				done = true
				return false
			case "response.failed":
				msg := "the ChatGPT request failed"
				if e.Response.Error != nil && e.Response.Error.Message != "" {
					msg = e.Response.Error.Message
				}
				streamErr = errors.New(o.id + " (ChatGPT sign-in): " + msg)
				return false
			case "error":
				msg := e.Message
				if msg == "" {
					msg = "the ChatGPT request failed"
				}
				streamErr = errors.New(o.id + " (ChatGPT sign-in): " + msg)
				return false
			}
			return true
		})
		if err == nil {
			err = streamErr
		}
		if err == nil && !done {
			err = errors.New(o.id + " (ChatGPT sign-in): stream ended before the response completed")
		}
		if err != nil {
			ch <- Event{Type: EventError, Err: err}
			return
		}
		usage.Subscription = true
		stop := "stop"
		if calls > 0 {
			stop = "tool_calls"
		}
		ch <- Event{Type: EventDone, Usage: usage, StopReason: stop}
	}()
	return ch, nil
}

// listChatGPTModels verifies the sign-in against the backend and returns the
// models it offers. The model list endpoint is undocumented, so when it can't
// be read (but the token is accepted) a built-in list is returned.
func (o *OpenAI) listChatGPTModels(ctx context.Context, cred Credential) ([]string, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, chatGPTBase(cred)+"/models?client_version=1.0.0", nil)
	if err != nil {
		return nil, err
	}
	setChatGPTHeaders(hreq.Header, cred)
	ids, err := listIDs(hreq, "openai (ChatGPT sign-in)", func(b []byte) ([]string, error) {
		var r struct {
			Models []struct {
				Slug string `json:"slug"`
				ID   string `json:"id"`
			} `json:"models"`
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		var out []string
		for _, m := range r.Models {
			if m.Slug != "" {
				out = append(out, m.Slug)
			} else if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		for _, m := range r.Data {
			out = append(out, m.ID)
		}
		return out, nil
	})
	if err == nil && len(ids) > 0 {
		return ids, nil
	}
	var pe *Error
	switch {
	case err == nil:
	case errors.As(err, &pe):
		if pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden {
			return nil, err
		}
	case strings.Contains(err.Error(), "unexpected model list"):
	default:
		return nil, err // network failure: not a verdict on the sign-in
	}
	return append([]string(nil), ChatGPTFallbackModels...), nil
}
