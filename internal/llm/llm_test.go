package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func collect(t *testing.T, ch <-chan Event) (text, reasoning string, calls []ToolCall, done Event) {
	t.Helper()
	for ev := range ch {
		switch ev.Type {
		case EventTextDelta:
			text += ev.Text
		case EventReasoningDelta:
			reasoning += ev.Text
		case EventToolCall:
			calls = append(calls, *ev.ToolCall)
		case EventDone:
			done = ev
		case EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	return
}

func sseServer(t *testing.T, check func(r *http.Request, body map[string]any), events string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		check(r, body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, events)
	}))
}

func testRequest() Request {
	return Request{
		Model:  "m1",
		System: "be brief",
		Messages: []Message{
			Text(RoleUser, "list files"),
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "file__list", Args: json.RawMessage(`{"path":"."}`)}}},
			{Role: RoleTool, ToolCallID: "c1", ToolName: "file__list", Result: "a.txt"},
		},
		Tools:     []ToolSpec{{Name: "file__list", Description: "list", Schema: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 1000,
		Reasoning: ReasoningMedium,
	}
}

func TestAnthropicStream(t *testing.T) {
	events := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":1}}}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`, ``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`, ``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`, ``,
		`data: {"type":"content_block_stop","index":0}`, ``,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`, ``,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}`, ``,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo"}}`, ``,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t9","name":"shell__run"}}`, ``,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`, ``,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`, ``,
		`data: {"type":"content_block_stop","index":2}`, ``,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`, ``,
		`data: {"type":"message_stop"}`, ``,
	}, "\n")
	srv := sseServer(t, func(r *http.Request, body map[string]any) {
		if r.Header.Get("x-api-key") != "k" || r.URL.Path != "/v1/messages" {
			t.Errorf("bad request %s %v", r.URL.Path, r.Header)
		}
		if th, _ := body["thinking"].(map[string]any); th == nil || th["type"] != "adaptive" {
			t.Errorf("adaptive thinking missing: %v", body["thinking"])
		}
		if oc, _ := body["output_config"].(map[string]any); oc == nil || oc["effort"] != "medium" {
			t.Errorf("effort missing: %v", body["output_config"])
		}
		msgs := body["messages"].([]any)
		if len(msgs) != 3 {
			t.Errorf("want 3 messages, got %d", len(msgs))
		}
	}, events)
	defer srv.Close()
	ch, err := (&Anthropic{}).Stream(context.Background(), Credential{APIKey: "k", BaseURL: srv.URL}, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, reasoning, calls, done := collect(t, ch)
	if text != "Hello" || reasoning != "hmm" {
		t.Fatalf("text=%q reasoning=%q", text, reasoning)
	}
	if len(calls) != 1 || calls[0].Name != "shell__run" || string(calls[0].Args) != `{"cmd":"ls"}` {
		t.Fatalf("calls=%+v", calls)
	}
	if done.Usage.InputTokens != 100 || done.Usage.CachedInputTokens != 90 || done.Usage.OutputTokens != 42 {
		t.Fatalf("usage=%+v", done.Usage)
	}
}

func TestOpenAIStream(t *testing.T) {
	events := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hi"}}]}`, ``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"shell__run","arguments":"{\"cmd\""}}]}}]}`, ``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`, ``,
		`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":100},"completion_tokens_details":{"reasoning_tokens":20}}}`, ``,
		`data: [DONE]`, ``,
	}, "\n")
	srv := sseServer(t, func(r *http.Request, body map[string]any) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/chat/completions" {
			t.Errorf("bad request %s", r.URL.Path)
		}
		if body["reasoning_effort"] != "none" || body["max_completion_tokens"].(float64) != 1000 {
			t.Errorf("body=%v", body)
		}
		msgs := body["messages"].([]any)
		if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" {
			t.Errorf("messages=%v", msgs)
		}
	}, events)
	defer srv.Close()
	p := &OpenAI{id: "openai", defaultBase: srv.URL}
	ch, err := p.Stream(context.Background(), Credential{APIKey: "k"}, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, _, calls, done := collect(t, ch)
	if text != "Hi" || len(calls) != 1 || string(calls[0].Args) != `{"cmd":"ls"}` {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
	if done.Usage.InputTokens != 120 || done.Usage.CachedInputTokens != 100 || done.Usage.ReasoningTokens != 20 {
		t.Fatalf("usage=%+v", done.Usage)
	}
}

func TestOpenAIReasoningEffortWithTools(t *testing.T) {
	req := testRequest()
	openAI := (&OpenAI{}).buildRequest(req)
	if openAI["reasoning_effort"] != "none" {
		t.Fatalf("official OpenAI tool request effort = %v, want none", openAI["reasoning_effort"])
	}

	compatible := (&OpenAI{compatible: true}).buildRequest(req)
	if compatible["reasoning_effort"] != "none" {
		t.Fatalf("compatible API tool request effort = %v, want none", compatible["reasoning_effort"])
	}

	req.Tools = nil
	noTools := (&OpenAI{}).buildRequest(req)
	if noTools["reasoning_effort"] != ReasoningMedium {
		t.Fatalf("OpenAI no-tools effort = %v, want medium", noTools["reasoning_effort"])
	}

	// Quick/Auto has reasoning explicitly off. It must still send "none" with
	// tools rather than omit the field and let reasoning models use a default.
	quickReq := testRequest()
	quickReq.Reasoning = ReasoningOff
	quick := (&OpenAI{}).buildRequest(quickReq)
	if quick["reasoning_effort"] != "none" {
		t.Fatalf("quick tool request effort = %v, want none", quick["reasoning_effort"])
	}
	compatibleQuick := (&OpenAI{compatible: true}).buildRequest(quickReq)
	if compatibleQuick["reasoning_effort"] != "none" {
		t.Fatalf("compatible quick tool request effort = %v, want none", compatibleQuick["reasoning_effort"])
	}
}

func TestGeminiStream(t *testing.T) {
	events := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"thinking…","thought":true},{"text":"Sure"}]}}]}`, ``,
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"shell__run","args":{"cmd":"ls"}},"thoughtSignature":"s1"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":10,"thoughtsTokenCount":5}}`, ``,
	}, "\n")
	srv := sseServer(t, func(r *http.Request, body map[string]any) {
		if r.Header.Get("x-goog-api-key") != "k" || !strings.HasSuffix(r.URL.Path, "/models/m1:streamGenerateContent") {
			t.Errorf("bad request %s", r.URL.Path)
		}
		gc := body["generationConfig"].(map[string]any)
		if gc["thinkingConfig"].(map[string]any)["thinkingLevel"] != "high" {
			t.Errorf("thinking=%v", gc)
		}
	}, events)
	defer srv.Close()
	ch, err := (&Gemini{}).Stream(context.Background(), Credential{APIKey: "k", BaseURL: srv.URL}, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, reasoning, calls, done := collect(t, ch)
	if text != "Sure" || reasoning != "thinking…" || len(calls) != 1 || calls[0].Signature != "s1" {
		t.Fatalf("text=%q reasoning=%q calls=%+v", text, reasoning, calls)
	}
	if done.Usage.OutputTokens != 15 || done.Usage.ReasoningTokens != 5 {
		t.Fatalf("usage=%+v", done.Usage)
	}
}

func TestProviderErrorAndList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"b"},{"id":"a"}]}`)
			return
		}
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":"slow down"}`)
	}))
	defer srv.Close()
	p := &OpenAI{id: "openai_compatible", defaultBase: srv.URL, compatible: true}
	ids, err := p.ListModels(context.Background(), Credential{})
	if err != nil || strings.Join(ids, ",") != "a,b" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	_, err = p.Stream(context.Background(), Credential{}, testRequest())
	if !IsRateLimit(err) {
		t.Fatalf("want rate limit, got %v", err)
	}
	if pe := err.(*Error); pe.RetryAfter.Seconds() != 7 {
		t.Fatalf("retry-after %v", pe.RetryAfter)
	}
}

func TestReasoningStyles(t *testing.T) {
	req := testRequest()
	req.ReasoningStyle = "budget"
	req.MaxTokens = 16384
	a := buildAnthropicRequest(req)
	if a.Thinking == nil || a.Thinking.Type != "enabled" || a.Thinking.BudgetTokens != 8192 || a.OutputConfig != nil {
		t.Fatalf("budget style: %+v %+v", a.Thinking, a.OutputConfig)
	}
	req.Reasoning = ReasoningOff
	if a := buildAnthropicRequest(req); a.Thinking != nil {
		t.Fatal("off should omit thinking")
	}
	req = testRequest()
	req.ReasoningStyle = "budget"
	g := buildGeminiRequest(req)
	tc := g["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if tc["thinkingBudget"] != 8192 {
		t.Fatalf("gemini budget: %v", tc)
	}
}
