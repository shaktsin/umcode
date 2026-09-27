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

func TestChatGPTResponsesStream(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		hdr = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"response.created","response":{}}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
			`{"type":"response.output_text.delta","delta":"Hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_9","name":"file.read","arguments":"{\"path\":\"a.txt\"}"}}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":5}}}}`,
		} {
			w.Write([]byte("data: " + ev + "\n\n"))
		}
	}))
	defer srv.Close()
	o := &OpenAI{id: "openai", defaultBase: "https://api.openai.com/v1"}
	ch, err := o.Stream(context.Background(), Credential{Kind: KindChatGPT, APIKey: "tok-1", AccountID: "acct-1", BaseURL: srv.URL}, Request{
		Model: "gpt-x", System: "be brief", Reasoning: ReasoningMedium,
		Tools: []ToolSpec{{Name: "file.read", Description: "read", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			Text(RoleUser, "hi"),
			{Role: RoleAssistant, Parts: []Part{{Type: "text", Text: "ok"}}, ToolCalls: []ToolCall{{ID: "call_1", Name: "file.read", Args: json.RawMessage(`{"path":"x"}`)}}},
			{Role: RoleTool, ToolCallID: "call_1", Result: "contents"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, reasoning, calls, done := collect(t, ch)
	if text != "Hello" || reasoning != "thinking" || len(calls) != 1 || calls[0].ID != "call_9" || string(calls[0].Args) != `{"path":"a.txt"}` {
		t.Fatalf("text=%q reasoning=%q calls=%+v", text, reasoning, calls)
	}
	u := done.Usage
	if !u.Reported || !u.Subscription || u.InputTokens != 100 || u.CachedInputTokens != 40 || u.OutputTokens != 20 || u.ReasoningTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	if hdr.Get("Authorization") != "Bearer tok-1" || hdr.Get("chatgpt-account-id") != "acct-1" {
		t.Fatalf("headers = %v", hdr)
	}
	if got["model"] != "gpt-x" || got["instructions"] != "be brief" || got["stream"] != true || got["store"] != false {
		t.Fatalf("body = %v", got)
	}
	if r, _ := got["reasoning"].(map[string]any); r["effort"] != "medium" {
		t.Fatalf("reasoning = %v", got["reasoning"])
	}
	in := got["input"].([]any)
	types := []string{}
	for _, it := range in {
		m := it.(map[string]any)
		types = append(types, m["type"].(string))
	}
	if strings.Join(types, ",") != "message,message,function_call,function_call_output" {
		t.Fatalf("input types = %v", types)
	}
	if tool := got["tools"].([]any)[0].(map[string]any); tool["type"] != "function" || tool["name"] != "file.read" {
		t.Fatalf("tools = %v", got["tools"])
	}
}

func TestChatGPTStreamFailureAndListModels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"usage limit reached\"}}}\n\n"))
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, "no", 401)
			return
		}
		w.Write([]byte(`{"models":[{"slug":"gpt-b"},{"slug":"gpt-a"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	o := &OpenAI{id: "openai"}
	ch, err := o.Stream(context.Background(), Credential{Kind: KindChatGPT, APIKey: "good", BaseURL: srv.URL}, Request{Model: "m", Messages: []Message{Text(RoleUser, "hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var sawErr error
	for ev := range ch {
		if ev.Type == EventError {
			sawErr = ev.Err
		}
	}
	if sawErr == nil || !strings.Contains(sawErr.Error(), "usage limit reached") {
		t.Fatalf("err = %v", sawErr)
	}
	ids, err := o.ListModels(context.Background(), Credential{Kind: KindChatGPT, APIKey: "good", BaseURL: srv.URL})
	if err != nil || strings.Join(ids, ",") != "gpt-a,gpt-b" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if _, err := o.ListModels(context.Background(), Credential{Kind: KindChatGPT, APIKey: "bad", BaseURL: srv.URL}); err == nil {
		t.Fatal("expected auth error for a rejected token")
	}
}
