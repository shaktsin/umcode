package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
)

func fixtureRequest() ([]promptLayer, llm.Request) {
	layers := []promptLayer{
		{Name: layerCore, Text: strings.Repeat("c", 400)},
		{Name: layerProject, Text: strings.Repeat("p", 800)},
	}
	req := llm.Request{
		Model: "fixture",
		Tools: []llm.ToolSpec{
			{Name: "file__read", Description: strings.Repeat("d", 40), Schema: json.RawMessage(`{"type":"object"}`)},
			{Name: "shell__run", Description: strings.Repeat("e", 80), Schema: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
		Messages: []llm.Message{
			llm.Text(llm.RoleUser, strings.Repeat("u", 100)),
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "file__read", Args: json.RawMessage(`{"path":"a"}`)}}},
			{Role: llm.RoleTool, ToolCallID: "1", ToolName: "file__read", Result: strings.Repeat("r", 4000)},
		},
	}
	return layers, req
}

func TestMeasureRequestSumsParts(t *testing.T) {
	layers, req := fixtureRequest()
	b := measureRequest(layers, req, RequestPackets{})
	if b.Layers[layerCore] != 100 || b.Layers[layerProject] != 200 || b.SystemTokens != 300 {
		t.Fatalf("layers = %+v system = %d", b.Layers, b.SystemTokens)
	}
	if b.ToolCount != 2 || b.ToolSpecTokens != toolSpecTokens(req.Tools) {
		t.Fatalf("tools = %d / %d", b.ToolCount, b.ToolSpecTokens)
	}
	if b.ToolResultTokens != 1000 {
		t.Fatalf("tool result tokens = %d", b.ToolResultTokens)
	}
	if b.ConversationTokens <= 0 || b.ConversationTokens+b.ToolResultTokens != estimateMessageTokens(req.Messages) {
		t.Fatalf("conversation = %d", b.ConversationTokens)
	}
	if b.TotalTokens != b.SystemTokens+b.ToolSpecTokens+b.ConversationTokens+b.ToolResultTokens {
		t.Fatalf("total = %+v", b)
	}
}

func TestMeasureRequestEmpty(t *testing.T) {
	b := measureRequest(nil, llm.Request{}, RequestPackets{})
	if b.Layers == nil || len(b.Layers) != 0 || b.TotalTokens != 0 || b.ToolCount != 0 {
		t.Fatalf("empty request = %+v", b)
	}
}

// The baseline fixture pins today's accounting so later phases can show how
// the same request measures after optimization. Regenerate with UPDATE_GOLDEN=1.
func TestRequestBaselineGolden(t *testing.T) {
	layers, req := fixtureRequest()
	got, err := json.MarshalIndent(measureRequest(layers, req, RequestPackets{}), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "request_baseline.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("baseline drifted:\n%s\nwant:\n%s", got, want)
	}
}
