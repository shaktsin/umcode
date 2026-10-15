package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatGPTIncompleteResponsePreservesLimitAndSuppressesCalls(t *testing.T) {
	for _, tt := range []struct {
		name      string
		terminal  string
		arguments string
		wantStop  string
		wantError string
		wantCalls int
	}{
		{name: "completed text", terminal: `"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}`, wantStop: "stop"},
		{name: "completed function", terminal: `"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}`, arguments: `{"path":"safe.txt"}`, wantStop: "tool_calls", wantCalls: 1},
		{name: "output limit text", terminal: `"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":5}}`, wantStop: "max_output_tokens"},
		{name: "output limit after complete function item", terminal: `"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":5}}`, arguments: `{"path":"safe.txt"}`, wantStop: "max_output_tokens"},
		{name: "output limit after truncated function arguments", terminal: `"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":5}}`, arguments: `{"path":`, wantStop: "max_output_tokens"},
		{name: "unknown incomplete reason", terminal: `"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":10,"output_tokens":5}}`, arguments: `{"path":"safe.txt"}`, wantError: "content_filter"},
		{name: "missing incomplete reason", terminal: `"type":"response.incomplete","response":{"usage":{"input_tokens":10,"output_tokens":5}}`, arguments: `{"path":"safe.txt"}`, wantError: "response incomplete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Partial response\"}\n\n")
				if tt.arguments != "" {
					fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"file.read\",\"arguments\":%q}}\n\n", tt.arguments)
				}
				fmt.Fprintf(w, "data: {%s}\n\n", tt.terminal)
			}))
			defer server.Close()
			provider := &OpenAI{id: "openai"}
			ch, err := provider.Stream(t.Context(), Credential{Kind: KindChatGPT, APIKey: "test-only", BaseURL: server.URL}, Request{Model: "test", Messages: []Message{Text(RoleUser, "hello")}})
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			var terminal Event
			calls := 0
			terminalCount := 0
			for event := range ch {
				switch event.Type {
				case EventTextDelta:
					text.WriteString(event.Text)
				case EventToolCall:
					calls++
				case EventDone, EventError:
					terminal, terminalCount = event, terminalCount+1
				}
			}
			if calls != tt.wantCalls {
				t.Errorf("emitted function calls = %d, want %d", calls, tt.wantCalls)
			}
			if terminalCount != 1 {
				t.Fatalf("terminal events = %d, want 1", terminalCount)
			}
			if tt.wantError != "" {
				if terminal.Type != EventError || terminal.Err == nil || !strings.Contains(terminal.Err.Error(), tt.wantError) {
					t.Errorf("terminal error = %+v, want %q", terminal, tt.wantError)
				}
			} else if terminal.Type != EventDone || terminal.StopReason != tt.wantStop {
				t.Errorf("terminal event = %+v, want done with %q", terminal, tt.wantStop)
			}
			if !terminal.Usage.Reported || terminal.Usage.InputTokens != 10 || terminal.Usage.OutputTokens != 5 {
				t.Errorf("terminal usage = %+v", terminal.Usage)
			}
			if text.String() != "Partial response" {
				t.Errorf("partial text lost: %q", text.String())
			}
		})
	}
}
