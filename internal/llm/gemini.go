package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Gemini speaks the Gemini API (generativelanguage.googleapis.com).
type Gemini struct{}

func (*Gemini) ID() string { return "gemini" }

func geminiBase(cred Credential) string {
	if cred.BaseURL != "" {
		return strings.TrimRight(cred.BaseURL, "/")
	}
	return "https://generativelanguage.googleapis.com/v1beta"
}

// geminiThinkingBudget maps reasoning levels to Gemini thinking budgets.
// "off" leaves the model default, because some models cannot disable thinking.
func geminiThinkingBudget(level string) (int, bool) {
	switch level {
	case ReasoningLow:
		return 1024, true
	case ReasoningMedium:
		return 8192, true
	case ReasoningHigh:
		return 24576, true
	}
	return 0, false
}

type gmPart struct {
	Text             string          `json:"text,omitempty"`
	Thought          bool            `json:"thought,omitempty"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	InlineData       *gmInline       `json:"inlineData,omitempty"`
	FunctionCall     *gmFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *gmFunctionResp `json:"functionResponse,omitempty"`
}

type gmInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gmFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gmFunctionResp struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type gmContent struct {
	Role  string   `json:"role,omitempty"`
	Parts []gmPart `json:"parts"`
}

func buildGeminiRequest(req Request) map[string]any {
	var contents []gmContent
	add := func(role string, parts ...gmPart) {
		if n := len(contents); n > 0 && contents[n-1].Role == role {
			contents[n-1].Parts = append(contents[n-1].Parts, parts...)
			return
		}
		contents = append(contents, gmContent{Role: role, Parts: parts})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			var parts []gmPart
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					parts = append(parts, gmPart{Text: p.Text})
				case "image":
					parts = append(parts, gmPart{InlineData: &gmInline{MimeType: p.MimeType, Data: p.DataB64}})
				}
			}
			add("user", parts...)
		case RoleAssistant:
			var parts []gmPart
			if t := m.JoinedText(); t != "" {
				parts = append(parts, gmPart{Text: t})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Args
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				parts = append(parts, gmPart{FunctionCall: &gmFunctionCall{Name: tc.Name, Args: args}, ThoughtSignature: tc.Signature})
			}
			if len(parts) > 0 {
				add("model", parts...)
			}
		case RoleTool:
			resp := map[string]any{"output": m.Result}
			if m.IsError {
				resp = map[string]any{"error": m.Result}
			}
			add("user", gmPart{FunctionResponse: &gmFunctionResp{Name: m.ToolName, Response: resp}})
		}
	}
	body := map[string]any{"contents": contents}
	if req.System != "" {
		body["systemInstruction"] = gmContent{Parts: []gmPart{{Text: req.System}}}
	}
	if len(req.Tools) > 0 {
		var decls []map[string]any
		for _, t := range req.Tools {
			decls = append(decls, map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Schema})
		}
		body["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	gen := map[string]any{}
	if req.MaxTokens > 0 {
		gen["maxOutputTokens"] = req.MaxTokens
	}
	if req.ReasoningStyle == "budget" {
		if b, ok := geminiThinkingBudget(req.Reasoning); ok {
			gen["thinkingConfig"] = map[string]any{"thinkingBudget": b, "includeThoughts": true}
		}
	} else {
		switch req.Reasoning {
		case ReasoningLow:
			gen["thinkingConfig"] = map[string]any{"thinkingLevel": "low", "includeThoughts": true}
		case ReasoningMedium, ReasoningHigh:
			gen["thinkingConfig"] = map[string]any{"thinkingLevel": "high", "includeThoughts": true}
		}
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	return body
}

func (g *Gemini) Stream(ctx context.Context, cred Credential, req Request) (<-chan Event, error) {
	body, err := json.Marshal(buildGeminiRequest(req))
	if err != nil {
		return nil, err
	}
	u := geminiBase(cred) + "/models/" + url.PathEscape(req.Model) + ":streamGenerateContent?alt=sse"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("x-goog-api-key", cred.APIKey)
	resp, err := HTTPClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, readError("gemini", resp)
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		var usage Usage
		stop := ""
		n := 0
		var streamErr error
		err := readSSE(resp.Body, func(ev sseEvent) bool {
			if ev.Data == "" {
				return true
			}
			var chunk struct {
				Candidates []struct {
					Content struct {
						Parts []gmPart `json:"parts"`
					} `json:"content"`
					FinishReason string `json:"finishReason"`
				} `json:"candidates"`
				UsageMetadata *struct {
					PromptTokenCount        int64 `json:"promptTokenCount"`
					CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
					ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
					CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
				} `json:"usageMetadata"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
				streamErr = fmt.Errorf("gemini: bad chunk: %w", err)
				return false
			}
			if chunk.Error != nil {
				streamErr = fmt.Errorf("gemini: %s", chunk.Error.Message)
				return false
			}
			if um := chunk.UsageMetadata; um != nil {
				usage = Usage{
					InputTokens:       um.PromptTokenCount,
					CachedInputTokens: um.CachedContentTokenCount,
					OutputTokens:      um.CandidatesTokenCount + um.ThoughtsTokenCount,
					ReasoningTokens:   um.ThoughtsTokenCount,
					Reported:          true,
				}
			}
			for _, c := range chunk.Candidates {
				for _, p := range c.Content.Parts {
					switch {
					case p.FunctionCall != nil:
						n++
						id := p.FunctionCall.ID
						if id == "" {
							id = fmt.Sprintf("gm_call_%d", n)
						}
						args := p.FunctionCall.Args
						if len(args) == 0 {
							args = json.RawMessage("{}")
						}
						ch <- Event{Type: EventToolCall, ToolCall: &ToolCall{ID: id, Name: p.FunctionCall.Name, Args: args, Signature: p.ThoughtSignature}}
					case p.Thought && p.Text != "":
						ch <- Event{Type: EventReasoningDelta, Text: p.Text}
					case p.Text != "":
						ch <- Event{Type: EventTextDelta, Text: p.Text}
					}
				}
				if c.FinishReason != "" {
					stop = c.FinishReason
				}
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
		ch <- Event{Type: EventDone, Usage: usage, StopReason: stop}
	}()
	return ch, nil
}

func (g *Gemini) ListModels(ctx context.Context, cred Credential) ([]string, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiBase(cred)+"/models?pageSize=1000", nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("x-goog-api-key", cred.APIKey)
	return listIDs(hreq, "gemini", func(b []byte) ([]string, error) {
		var r struct {
			Models []struct {
				Name    string   `json:"name"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		var ids []string
		for _, m := range r.Models {
			for _, meth := range m.Methods {
				if meth == "generateContent" {
					ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
					break
				}
			}
		}
		return ids, nil
	})
}
