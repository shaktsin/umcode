package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/toolselect"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoveryAdapterSchemaAndCancellation(t *testing.T) {
	c, err := toolselect.NewCatalog([]toolselect.Entry{{CanonicalName: "special.action", WireName: "special__action", Family: "other", Origin: "registry", Spec: llm.ToolSpec{Name: "special__action", Description: "Specialist action", Schema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := toolselect.Start(c, toolselect.Signals{})
	if err != nil {
		t.Fatal(err)
	}
	tool := &discoveryTool{state: s}
	var schema map[string]any
	if json.Unmarshal(tool.Schema(), &schema) != nil || schema["additionalProperties"] != false {
		t.Fatal("discovery schema not strict")
	}
	for _, args := range []string{`{}`, `{"query":"  "}`, `{"names":[]}`, `{"cursor":"cursor-only"}`} {
		if _, err := tool.Call(t.Context(), json.RawMessage(args)); err == nil || len(s.Specs()) != 0 {
			t.Fatalf("invalid discovery arguments changed state: %s, err=%v", args, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Call(ctx, json.RawMessage(`{"query":"special"}`)); err == nil || len(s.Specs()) != 0 {
		t.Fatal("cancelled discovery changed state")
	}
	if _, err := tool.Call(t.Context(), json.RawMessage(`{"names":["special.action"]}`)); err != nil || len(s.Specs()) != 1 {
		t.Fatal("discovery did not load schema", err)
	}
}

// Model the provider's schema acceptance at the actual HTTP request boundary,
// rather than only testing JSON-schema validity (which permits these keywords).
func TestDiscoverySchemaAcceptedByClaude(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				InputSchema map[string]any `json:"input_schema"`
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(request.Tools) != 1 {
			t.Errorf("expected discovery tool, got %d", len(request.Tools))
			w.WriteHeader(400)
			return
		}
		schema := request.Tools[0].InputSchema
		for _, keyword := range []string{"oneOf", "allOf", "anyOf"} {
			if _, present := schema[keyword]; present {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"input_schema does not support oneOf, allOf, or anyOf at the top level"}}`)
				return
			}
		}
		if schema["type"] != "object" {
			t.Errorf("schema must be an object: %v", schema)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	tool := &discoveryTool{}
	events, err := (&llm.Anthropic{}).Stream(t.Context(), llm.Credential{APIKey: "test", BaseURL: server.URL}, llm.Request{Model: "test", Tools: []llm.ToolSpec{{Name: "tools__discover", Description: tool.Description(), Schema: tool.Schema()}}})
	if err != nil {
		t.Fatalf("Claude rejected discovery schema: %v", err)
	}
	for event := range events {
		if event.Type == llm.EventError {
			t.Fatalf("Claude stream failed: %+v", event)
		}
	}
}
