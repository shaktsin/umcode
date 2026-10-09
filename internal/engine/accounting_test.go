package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/toolreduce"
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

func TestMeasureRequestWorkUpdateAttribution(t *testing.T) {
	req := llm.Request{Tools: []llm.ToolSpec{{Name: "work__update", Description: "12345678", Schema: json.RawMessage(`{}`)}}, Messages: []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "same", Name: "work__update", Args: json.RawMessage(`{"x":12}`)}}},
		{Role: llm.RoleTool, ToolCallID: "same", Result: "12345678"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "same", Name: "file__read", Args: json.RawMessage(`{}`)}}},
		{Role: llm.RoleTool, ToolCallID: "same", ToolName: "work__update", Result: strings.Repeat("x", 100)},
		{Role: llm.RoleTool, ToolCallID: "orphan", ToolName: "work__update", Result: strings.Repeat("x", 100)},
	}}
	b := measureRequest(nil, req, RequestPackets{}, nil)
	data, _ := json.Marshal(b)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	for name, want := range map[string]float64{"workUpdateSpecTokens": 6, "workUpdateCallTokens": 5, "workUpdateResultTokens": 2} {
		if fields[name] != want {
			t.Errorf("%s=%v, want %v", name, fields[name], want)
		}
	}
	if b.TotalTokens != b.ToolSpecTokens+b.ConversationTokens+b.ToolResultTokens {
		t.Fatal("attribution double counted total")
	}
	req.Tools = nil // work.update is not exposed when Designed workflow is off.
	plain := measureRequest(nil, req, RequestPackets{}, nil)
	data, _ = json.Marshal(plain)
	if strings.Contains(string(data), "workUpdate") {
		t.Fatalf("disabled/absent tool attributed: %s", data)
	}
}

func TestMeasureRequestSumsParts(t *testing.T) {
	layers, req := fixtureRequest()
	b := measureRequest(layers, req, RequestPackets{}, nil)
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
	b := measureRequest(nil, llm.Request{}, RequestPackets{}, nil)
	if b.Layers == nil || len(b.Layers) != 0 || b.TotalTokens != 0 || b.ToolCount != 0 {
		t.Fatalf("empty request = %+v", b)
	}
}

// The baseline fixture pins today's accounting so later phases can show how
// the same request measures after optimization. Regenerate with UPDATE_GOLDEN=1.
func TestRequestBaselineGolden(t *testing.T) {
	layers, req := fixtureRequest()
	got, err := json.MarshalIndent(measureRequest(layers, req, RequestPackets{}, nil), "", "  ")
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

func TestMeasureRequestAttributesReducerSavingsByCallID(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{
		{Role: llm.RoleTool, ToolCallID: "reduced", Result: strings.Repeat("r", 400)},
		{Role: llm.RoleTool, ToolCallID: "plain", Result: strings.Repeat("p", 800)},
	}}
	b := measureRequest(nil, req, RequestPackets{}, ToolReductions{
		{CallID: "reduced", Occurrence: 1}: {Strategy: "verification", OriginalTokens: 1000, SentTokens: 100},
	})
	if b.ToolResultTokens != 300 || b.ToolResultOriginalTokens != 1200 || b.ToolResultSavedTokens != 900 || b.ToolResultsReduced != 1 {
		t.Fatalf("attribution = %+v", b)
	}
}

func TestMeasureRequestSeparatesEmergencyTrimFromReducerSavings(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, ToolCallID: "reduced", Result: "[tool result omitted to save context]"}}}
	b := measureRequest(nil, req, RequestPackets{}, ToolReductions{
		{CallID: "reduced", Occurrence: 1}: {Strategy: "verification", OriginalTokens: 1000, SentTokens: 100},
	})
	if b.ToolResultTokens != tokens(req.Messages[0].Result) || b.ToolResultOriginalTokens != 1000 || b.ToolResultSavedTokens != 900 || b.ToolResultsReduced != 1 {
		t.Fatalf("emergency trim attribution = %+v", b)
	}
}

func TestMeasureRequestIgnoresReportsForAbsentToolMessages(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, ToolCallID: "present", Result: strings.Repeat("p", 800)}}}
	b := measureRequest(nil, req, RequestPackets{}, ToolReductions{
		{CallID: "absent", Occurrence: 1}: {Strategy: "verification", OriginalTokens: 1000, SentTokens: 100},
	})
	if b.ToolResultTokens != 200 || b.ToolResultOriginalTokens != 200 || b.ToolResultSavedTokens != 0 || b.ToolResultsReduced != 0 {
		t.Fatalf("absent report attribution = %+v", b)
	}
}

func TestMeasureRequestCountsUnchangedResultsWhenReducersEnabled(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, ToolCallID: "plain", Result: strings.Repeat("p", 800)}}}
	b := measureRequest(nil, req, RequestPackets{}, ToolReductions{})
	if b.ToolResultTokens != 200 || b.ToolResultOriginalTokens != 200 || b.ToolResultSavedTokens != 0 || b.ToolResultsReduced != 0 {
		t.Fatalf("unchanged result attribution = %+v", b)
	}
}

func TestMeasureRequestHandlesMultipleReducedCallsWithoutCrossAttribution(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{
		{Role: llm.RoleTool, ToolCallID: "first", Result: strings.Repeat("a", 400)},
		{Role: llm.RoleTool, ToolCallID: "second", Result: strings.Repeat("b", 200)},
		{Role: llm.RoleAssistant, ToolCallID: "ghost", Parts: []llm.Part{{Text: "not a tool"}}},
	}}
	reductions := ToolReductions{
		{CallID: "first", Occurrence: 1}:  toolreduce.Report{Strategy: "verification", OriginalTokens: 800, SentTokens: 100},
		{CallID: "second", Occurrence: 1}: toolreduce.Report{Strategy: "shell", OriginalTokens: 500, SentTokens: 50},
		{CallID: "ghost", Occurrence: 1}:  toolreduce.Report{Strategy: "search", OriginalTokens: 900, SentTokens: 10},
	}
	b := measureRequest(nil, req, RequestPackets{}, reductions)
	if b.ToolResultTokens != 150 || b.ToolResultOriginalTokens != 1300 || b.ToolResultSavedTokens != 1150 || b.ToolResultsReduced != 2 {
		t.Fatalf("multiple reports = %+v", b)
	}
}

func TestMeasureRequestRepeatedIDUnchangedThenReduced(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{
		{Role: llm.RoleTool, ToolCallID: "gm_call_1", Result: strings.Repeat("u", 400)},
		{Role: llm.RoleTool, ToolCallID: "gm_call_1", Result: strings.Repeat("r", 200)},
	}}
	b := measureRequest(nil, req, RequestPackets{}, ToolReductions{
		{CallID: "gm_call_1", Occurrence: 2}: {Strategy: "verification", OriginalTokens: 500, SentTokens: 50},
	})
	if b.ToolResultTokens != 150 || b.ToolResultOriginalTokens != 600 || b.ToolResultSavedTokens != 450 || b.ToolResultsReduced != 1 {
		t.Fatalf("reused ID assigned savings to the wrong message: %+v", b)
	}
}

func TestMeasureRequestRepeatedIDReducedTwice(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{
		{Role: llm.RoleTool, ToolCallID: "gm_call_1", Result: strings.Repeat("a", 400)},
		{Role: llm.RoleTool, ToolCallID: "gm_call_1", Result: strings.Repeat("b", 200)},
	}}
	b := measureRequest(nil, req, RequestPackets{}, ToolReductions{
		{CallID: "gm_call_1", Occurrence: 1}: {Strategy: "verification", OriginalTokens: 800, SentTokens: 100},
		{CallID: "gm_call_1", Occurrence: 2}: {Strategy: "shell", OriginalTokens: 500, SentTokens: 50},
	})
	if b.ToolResultTokens != 150 || b.ToolResultOriginalTokens != 1300 || b.ToolResultSavedTokens != 1150 || b.ToolResultsReduced != 2 {
		t.Fatalf("reused ID merged distinct reductions: %+v", b)
	}
}

func TestMeasureRequestRepeatedIDSurvivesPrefixAndEmergencyTrim(t *testing.T) {
	stub := "[tool result omitted to save context]"
	for _, prefix := range [][]llm.Message{
		{llm.Text(llm.RoleUser, "history")},
		{llm.Text(llm.RoleUser, "Current work state: compiled")},
	} {
		req := llm.Request{Messages: append(append([]llm.Message(nil), prefix...),
			llm.Message{Role: llm.RoleTool, ToolCallID: "gm_call_1", Result: stub},
			llm.Message{Role: llm.RoleTool, ToolCallID: "gm_call_1", Result: strings.Repeat("r", 200)})}
		b := measureRequest(nil, req, RequestPackets{}, ToolReductions{
			{CallID: "gm_call_1", Occurrence: 2}: {Strategy: "verification", OriginalTokens: 500, SentTokens: 50},
		})
		if b.ToolResultTokens != tokens(stub)+50 || b.ToolResultOriginalTokens != tokens(stub)+500 || b.ToolResultSavedTokens != 450 || b.ToolResultsReduced != 1 {
			t.Fatalf("prefix or emergency trim shifted identity: %+v", b)
		}
	}
}

func TestRequestBaselineGoldenStillOmitsZeroReducerFields(t *testing.T) {
	layers, req := fixtureRequest()
	b, err := json.Marshal(measureRequest(layers, req, RequestPackets{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"toolResultOriginalTokens", "toolResultSavedTokens", "toolResultsReduced"} {
		if strings.Contains(string(b), field) {
			t.Fatalf("zero reducer field %s appeared: %s", field, b)
		}
	}
}
