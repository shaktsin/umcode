package tools_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/tools"
)

func TestAgentToolsClaudeSchemas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name   string         `json:"name"`
				Schema map[string]any `json:"input_schema"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(body.Tools) != 5 {
			t.Errorf("tools=%d", len(body.Tools))
		}
		for _, tool := range body.Tools {
			if tool.Schema["type"] != "object" || tool.Schema["properties"] == nil {
				t.Errorf("invalid Claude schema %s", tool.Name)
				w.WriteHeader(400)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	var specs []llm.ToolSpec
	for _, tool := range tools.NewAgentTools(nil, nil) {
		specs = append(specs, llm.ToolSpec{Name: tools.ToWire(tool.Name()), Description: tool.Description(), Schema: tool.Schema()})
	}
	ch, err := (&llm.Anthropic{}).Stream(t.Context(), llm.Credential{APIKey: "fake", BaseURL: server.URL}, llm.Request{Model: "test", Tools: specs, MaxTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	for event := range ch {
		if event.Type == llm.EventError {
			t.Fatal(event)
		}
	}
}
