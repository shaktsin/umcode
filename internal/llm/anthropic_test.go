package llm

import (
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
