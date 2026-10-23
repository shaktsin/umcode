package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
)

var errRequestOverCapacity = errors.New("request exceeds model serving capacity")

type requestBudgetReport struct {
	CapacitySource                                                                                    string
	RetryCause                                                                                        string
	Window, EstimatedInput, BaseEstimatedInput, ReservedOutput, Margin                                int
	SystemTokens, ToolSchemaTokens, ConversationTokens, ToolResultTokens, MediaTokens, ProtocolTokens int
	EstimateOrigin                                                                                    string
	CalibrationFactor                                                                                 float64
	Reductions                                                                                        int
}
type requestCapacityError struct {
	Report requestBudgetReport
	Reason string
}

func (e *requestCapacityError) Error() string {
	if e.Reason == "unknown_capacity" {
		return ErrCapacityUnknown.Error()
	}
	if e.Reason != "capacity" {
		return "model request contains invalid capacity or schema metadata"
	}
	r := e.Report
	return fmt.Sprintf("request exceeds endpoint capacity: approximately %d input + %d response + %d headroom tokens, but the serving window is %d tokens", r.EstimatedInput, r.ReservedOutput, r.Margin, r.Window)
}
func (e *requestCapacityError) Unwrap() error {
	if e.Reason == "unknown_capacity" {
		return ErrCapacityUnknown
	}
	if e.Reason == "capacity" {
		return errRequestOverCapacity
	}
	return nil
}

// This is deliberately more conservative than the billing fallback's bytes/4.
// Provider-reported usage calibrates it further; a tokenizer can replace it.
func requestTextTokens(text string) int { return (len(text) + 2) / 3 }
func countRequestInput(req llm.Request) (requestBudgetReport, error) {
	r := requestBudgetReport{EstimateOrigin: "heuristic", CalibrationFactor: 1, ProtocolTokens: 16}
	r.SystemTokens = requestTextTokens(req.System)
	if req.System != "" {
		r.ProtocolTokens += 8
	}
	if len(req.Tools) > 0 {
		schemas, err := json.Marshal(req.Tools)
		if err != nil {
			return r, err
		}
		r.ToolSchemaTokens = requestTextTokens(string(schemas))
		r.ProtocolTokens += 24 * len(req.Tools)
	}
	for _, m := range req.Messages {
		r.ProtocolTokens += 12
		for _, p := range m.Parts {
			r.ConversationTokens += requestTextTokens(p.Text)
			if p.Type != "" && p.Type != "text" {
				r.MediaTokens += 4096
			}
		}
		for _, thought := range m.Thinking {
			r.ConversationTokens += requestTextTokens(thought.Text) + requestTextTokens(thought.Signature)
		}
		for _, call := range m.ToolCalls {
			quoted, _ := json.Marshal(string(call.Args))
			r.ConversationTokens += requestTextTokens(call.Name) + requestTextTokens(string(quoted)) + requestTextTokens(call.Signature)
			r.ProtocolTokens += 12
		}
		r.ToolResultTokens += requestTextTokens(m.Result)
	}
	r.BaseEstimatedInput = r.SystemTokens + r.ToolSchemaTokens + r.ConversationTokens + r.ToolResultTokens + r.MediaTokens + r.ProtocolTokens
	r.EstimatedInput = r.BaseEstimatedInput
	return r, nil
}
func cloneModelRequest(req llm.Request) llm.Request {
	out := req
	out.Tools = append([]llm.ToolSpec(nil), req.Tools...)
	for i := range out.Tools {
		out.Tools[i].Schema = append(json.RawMessage(nil), req.Tools[i].Schema...)
	}
	out.Messages = append([]llm.Message(nil), req.Messages...)
	for i := range out.Messages {
		out.Messages[i].Parts = append([]llm.Part(nil), req.Messages[i].Parts...)
		out.Messages[i].Thinking = append([]llm.Thinking(nil), req.Messages[i].Thinking...)
		out.Messages[i].ToolCalls = append([]llm.ToolCall(nil), req.Messages[i].ToolCalls...)
		for j := range out.Messages[i].ToolCalls {
			out.Messages[i].ToolCalls[j].Args = append(json.RawMessage(nil), req.Messages[i].ToolCalls[j].Args...)
		}
	}
	return out
}
func budgetRequest(req llm.Request, capacity models.Capacity, configuredReserve int) (llm.Request, requestBudgetReport, error) {
	return budgetCalibratedRequest(req, capacity, configuredReserve, 1, nil)
}
func budgetCalibratedRequest(req llm.Request, capacity models.Capacity, configuredReserve int, factor float64, measuredInput *int) (llm.Request, requestBudgetReport, error) {
	r, err := countRequestInput(req)
	r.Window = capacity.Window
	r.CapacitySource = capacity.Source
	if err != nil {
		return llm.Request{}, r, &requestCapacityError{Report: r, Reason: "invalid_schema"}
	}
	if capacity.Window <= 0 {
		return llm.Request{}, r, &requestCapacityError{Report: r, Reason: "unknown_capacity"}
	}
	if int64(capacity.Window) > 2147483647 || configuredReserve < 0 || req.MaxTokens < 0 {
		return llm.Request{}, r, &requestCapacityError{Report: r, Reason: "invalid_capacity"}
	}
	if factor < 1 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		factor = 1
	}
	r.CalibrationFactor = factor
	r.EstimatedInput = int(math.Ceil(float64(r.BaseEstimatedInput) * factor))
	if factor > 1 {
		r.EstimateOrigin = "heuristic_calibrated"
	}
	if measuredInput != nil && *measuredInput > 0 {
		r.EstimatedInput = *measuredInput
		r.EstimateOrigin = "provider_tokenizer"
		r.CalibrationFactor = 1
	}
	r.Margin = max(256, int(math.Ceil(float64(capacity.Window)*0.05)))
	r.ReservedOutput = capacity.Window / 4
	if req.MaxTokens > 0 {
		r.ReservedOutput = min(r.ReservedOutput, req.MaxTokens)
	}
	if configuredReserve > 0 {
		r.ReservedOutput = configuredReserve
	}
	if capacity.MaxOutput > 0 {
		r.ReservedOutput = min(r.ReservedOutput, capacity.MaxOutput)
	}
	if r.ReservedOutput <= 0 || int64(r.EstimatedInput)+int64(r.ReservedOutput)+int64(r.Margin) > int64(capacity.Window) {
		return llm.Request{}, r, &requestCapacityError{Report: r, Reason: "capacity"}
	}
	out := cloneModelRequest(req)
	out.MaxTokens = r.ReservedOutput
	return out, r, nil
}
func (e *Engine) requestEstimatorFactor(res resolved) float64 {
	key, err := capacityIdentity(res)
	if err != nil {
		return 1
	}
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	factor := e.requestCalibration[key]
	if factor < 1 {
		return 1
	}
	return factor
}
func (e *Engine) observeRequestUsage(res resolved, r requestBudgetReport, input int64, reported bool) {
	if !reported || input <= 0 || input > 2147483647 || r.BaseEstimatedInput <= 0 || r.EstimateOrigin == "provider_tokenizer" {
		return
	}
	key, err := capacityIdentity(res)
	if err != nil {
		return
	}
	factor := float64(input) / float64(r.BaseEstimatedInput) * 1.1
	if factor < 1 {
		return
	}
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	if e.requestCalibration == nil {
		e.requestCalibration = make(map[capacityKey]float64)
	}
	if factor > e.requestCalibration[key] {
		e.requestCalibration[key] = factor
	}
}
