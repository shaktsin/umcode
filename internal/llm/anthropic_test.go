package llm

import (
	"encoding/json"
	"testing"
)

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
