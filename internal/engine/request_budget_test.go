package engine

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
)

func budgetFixtureRequest() llm.Request {
	return llm.Request{MaxTokens: 16384, System: "Follow the original instructions.", Tools: []llm.ToolSpec{{Name: "file__read", Description: "Read the requested file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}}, Messages: []llm.Message{llm.Text(llm.RoleUser, "Read the next section of notes.txt"), {Role: llm.RoleTool, ToolCallID: "read", ToolName: "file__read", Result: "Observed file contents"}}}
}
func TestRequestBudgetIncludesAllComponents(t *testing.T) {
	req := budgetFixtureRequest()
	prepared, r, err := budgetRequest(req, models.Capacity{Window: 32768}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.SystemTokens <= 0 || r.ToolSchemaTokens <= 0 || r.ConversationTokens <= 0 || r.ToolResultTokens <= 0 || r.ProtocolTokens <= 0 {
		t.Fatalf("request components omitted: %+v", r)
	}
	if prepared.MaxTokens != r.ReservedOutput || r.EstimatedInput+r.ReservedOutput+r.Margin > r.Window {
		t.Fatal("physical context budget exceeded", r)
	}
	req.Messages = append(req.Messages, llm.Text(llm.RoleUser, strings.Repeat("incoming requirements ", 10000)))
	_, _, err = budgetRequest(req, models.Capacity{Window: 32768}, 0)
	var capacityErr *requestCapacityError
	if !errors.As(err, &capacityErr) {
		t.Fatalf("oversized incoming request was not caught: %v", err)
	}
}
func TestRequestBudgetReserveDefaults(t *testing.T) {
	_, r, err := budgetRequest(budgetFixtureRequest(), models.Capacity{Window: 32768}, 0)
	if err != nil || r.ReservedOutput != 8192 || r.Margin != 1639 {
		t.Fatalf("default headroom mismatch: %+v %v", r, err)
	}
	_, r, err = budgetRequest(budgetFixtureRequest(), models.Capacity{Window: 32768, MaxOutput: 2048}, 4096)
	if err != nil || r.ReservedOutput != 2048 {
		t.Fatalf("provider response cap ignored: %+v %v", r, err)
	}
}
func TestRequestBudgetPreservesUnicodeAndImages(t *testing.T) {
	req := budgetFixtureRequest()
	req.Messages = []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: "कृपया यह संवाद पढ़ें αβγ"}, {Type: "image", DataB64: "AAAA"}, {Type: "audio", DataB64: "AAAA"}}}}
	prepared, r, err := budgetRequest(req, models.Capacity{Window: 32768}, 0)
	if err != nil || r.MediaTokens <= 0 || len(prepared.Messages[0].Parts) != 3 || prepared.Messages[0].Parts[0].Text != "कृपया यह संवाद पढ़ें αβγ" {
		t.Fatalf("non-text or Unicode input lost: %+v %v", r, err)
	}
}
func TestRequestBudgetDoesNotMutateOriginal(t *testing.T) {
	req := budgetFixtureRequest()
	before, _ := json.Marshal(req)
	prepared, _, err := budgetRequest(req, models.Capacity{Window: 32768}, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Messages[0].Parts[0].Text = "changed"
	prepared.Tools[0].Schema[0] = 'x'
	after, _ := json.Marshal(req)
	if string(after) != string(before) {
		t.Fatal("working request aliases original instructions or schemas")
	}
}
func TestRequestBudgetUnknownCapacity(t *testing.T) {
	_, _, err := budgetRequest(budgetFixtureRequest(), models.Capacity{}, 0)
	if !errors.Is(err, ErrCapacityUnknown) {
		t.Fatalf("unknown capacity invented: %v", err)
	}
}
func TestRequestBudgetCalibrationRetainsHeadroom(t *testing.T) {
	e := &Engine{}
	res := resolved{sel: protocol.ModelSelection{Provider: "openai_compatible", Model: "coder"}}
	res.cred.Material.BaseURL = "http://localhost:1234/v1"
	r := requestBudgetReport{EstimatedInput: 100, BaseEstimatedInput: 100, CalibrationFactor: 1, EstimateOrigin: "heuristic"}
	e.observeRequestUsage(res, r, 200, true)
	factor := e.requestEstimatorFactor(res)
	if factor < 2.19 {
		t.Fatalf("provider underestimation not calibrated: %f", factor)
	}
	e.observeRequestUsage(res, r, 10, true)
	if e.requestEstimatorFactor(res) < factor {
		t.Fatal("a smaller sample removed required headroom")
	}
	res.cred.Material.BaseURL = "http://localhost:5678/v1"
	if e.requestEstimatorFactor(res) != 1 {
		t.Fatal("calibration leaked across endpoints")
	}
}
