package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicErrorRetainsPartialUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20,\"output_tokens\":1}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"failure\",\"message\":\"test\"}}\n\n")
	}))
	defer server.Close()
	ch, err := (&Anthropic{}).Stream(t.Context(), Credential{APIKey: "test", BaseURL: server.URL}, Request{Model: "test", MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	var last Event
	for event := range ch {
		last = event
	}
	if last.Type != EventError || !last.Usage.Reported || last.Usage.InputTokens != 20 {
		t.Fatal("partial usage lost", last)
	}
}

func TestAnthropicPreservesEmptySignedThinking(t *testing.T) {
	req := buildAnthropicRequest(Request{Messages: []Message{{
		Role:     RoleAssistant,
		Thinking: []Thinking{{Text: "", Signature: "signed"}},
	}}})
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Messages []struct{ Content []map[string]any }
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	block := decoded.Messages[0].Content[0]
	if value, present := block["thinking"]; !present || value != "" {
		t.Fatalf("required empty thinking field missing: %s", body)
	}
	if block["signature"] != "signed" {
		t.Fatalf("signature lost: %s", body)
	}
}

func TestAnthropicNeverExpandsExplicitOutputCap(t *testing.T) {
	for _, limit := range []int{512, 1024, 2048, 4096, 8192} {
		req := Request{Model: "bounded", MaxTokens: limit, Reasoning: ReasoningMedium, ReasoningStyle: "budget"}
		wire := buildAnthropicRequest(req)
		if wire.MaxTokens != limit {
			t.Fatalf("reserved %d, wire sends %d", limit, wire.MaxTokens)
		}
		if wire.Thinking != nil && (wire.Thinking.BudgetTokens < 1024 || wire.Thinking.BudgetTokens >= limit) {
			t.Fatal("invalid bounded thinking", wire.Thinking)
		}
	}
}
