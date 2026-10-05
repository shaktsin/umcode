package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/tools"
)

const passingReducerMarker = "PASSING_OUTPUT_MUST_STAY_CANONICAL"
const failingReducerMarker = "FAILING_OUTPUT_MUST_REACH_MODEL"

func reducerVerificationOutput(t *testing.T) string {
	t.Helper()
	payload := map[string]any{"status": "failed", "results": []map[string]any{
		{"label": "passing", "command": "go test ./passing", "status": "passed", "duration_ms": 12, "output": strings.Repeat(passingReducerMarker+"\n", 1200)},
		{"label": "failing", "command": "go test ./failing", "status": "failed", "duration_ms": 19, "exit_code": 1, "output": failingReducerMarker},
	}}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReduceToolResultFlagOffIsByteIdentical(t *testing.T) {
	output := reducerVerificationOutput(t)
	e := &Engine{Cfg: &config.Config{}}
	model, report := e.reduceToolResult("verification.run", json.RawMessage(`{}`), output, false)
	if model != output || report.Strategy != "" || e.toolReducerFailures.Load() != 0 {
		t.Fatalf("flag-off result changed: model=%q report=%+v failures=%d", model, report, e.toolReducerFailures.Load())
	}
}

func TestRunToolKeepsCanonicalStoredAndModelProjectionSeparate(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	e.Cfg.Models.ToolResultReducers = true
	output := reducerVerificationOutput(t)
	tool := &engineTestTool{name: "verification.run", risk: tools.RiskGreen, output: output}
	e.Tools.Add(tool)
	res := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "reduce-one", Name: tools.ToWire(tool.Name()), Args: json.RawMessage(`{}`)}, &fakePluginSnapshot{})
	if res.IsError || res.Output != output || res.ModelOutput == output || res.Reduction.Strategy != "verification" {
		t.Fatalf("tool result was not isolated: %+v", res)
	}
	if strings.Contains(res.ModelOutput, passingReducerMarker) || !strings.Contains(res.ModelOutput, failingReducerMarker) {
		t.Fatalf("model projection lost required output or kept passing output: %q", res.ModelOutput)
	}
	items, err := st.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Tool != nil && item.Tool.CallID == "reduce-one" {
			if item.Tool.Output != output {
				t.Fatal("stored item contains reduced output")
			}
			return
		}
	}
	t.Fatal("tool item was not stored")
}

func TestReducerPanicFallsBackCountsAndCompletes(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	e.Cfg.Models.ToolResultReducers = true
	output := reducerVerificationOutput(t)
	var logs bytes.Buffer
	e.Log = slog.New(slog.NewTextHandler(&logs, nil))
	e.reduceHook = func() { panic("boom") }
	other := &Engine{Cfg: e.Cfg}
	if model, _ := other.reduceToolResult("verification.run", nil, output, false); model == output || other.toolReducerFailures.Load() != 0 {
		t.Fatal("panic injection leaked into a separate engine")
	}
	tool := &engineTestTool{name: "verification.run", risk: tools.RiskGreen, output: output}
	e.Tools.Add(tool)
	res := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "panic", Name: tools.ToWire(tool.Name()), Args: json.RawMessage(`{}`)}, &fakePluginSnapshot{})
	if res.IsError || res.Output != output || res.ModelOutput != output || e.toolReducerFailures.Load() != 1 {
		t.Fatalf("panic escaped or changed result: %+v failures=%d", res, e.toolReducerFailures.Load())
	}
	if strings.Contains(logs.String(), passingReducerMarker) || strings.Contains(logs.String(), failingReducerMarker) || strings.Contains(logs.String(), "boom") {
		t.Fatalf("result text leaked into logs: %s", logs.String())
	}
}

func TestReducerLogsSafeMetadataForAppliedAndDeclined(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			var logs bytes.Buffer
			e := &Engine{Cfg: &config.Config{}, Log: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
			e.Cfg.Models.ToolResultReducers = true
			output := reducerVerificationOutput(t)
			if malformed {
				output = strings.TrimSuffix(output, "}")
			}
			_, report := e.reduceToolResult("verification.run", json.RawMessage(`{"secret":"ARGUMENT_SECRET"}`), output, false)
			var entry map[string]any
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatalf("missing structured reducer debug entry: %v", err)
			}
			if entry["tool"] != "verification.run" || entry["strategy"] != "verification" || entry["original_tokens"] != float64(report.OriginalTokens) || entry["sent_tokens"] != float64(report.SentTokens) {
				t.Fatalf("missing token/strategy metadata: %v", entry)
			}
			if malformed {
				if entry["decline_reason"] != "empty_candidate" {
					t.Fatalf("missing decline reason: %v", entry)
				}
			} else if counts, ok := entry["omitted"].(map[string]any); !ok || counts["passing_output_bytes"] != float64(len(strings.Repeat(passingReducerMarker+"\n", 1200))) {
				t.Fatalf("missing omission counts: %v", entry)
			}
			for _, secret := range []string{passingReducerMarker, failingReducerMarker, "ARGUMENT_SECRET"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("reducer logs leaked %q", secret)
				}
			}
		})
	}
}

func TestReducerHandlesNilConfigAndLogger(t *testing.T) {
	output := reducerVerificationOutput(t)
	e := &Engine{}
	if model, _ := e.reduceToolResult("verification.run", nil, output, false); model != output {
		t.Fatal("nil config changed output")
	}
	e.Cfg = &config.Config{}
	e.Cfg.Models.ToolResultReducers = true
	model, report := e.reduceToolResult("verification.run", nil, output, false)
	if model == output || report.Strategy != "verification" {
		t.Fatalf("nil logger prevented reduction: report=%+v", report)
	}
	if model, _ := e.reduceToolResult("verification.run", nil, "", false); model != "" {
		t.Fatal("empty output changed")
	}
	if model, report := e.reduceToolResult("unsupported.tool", nil, output, false); model != output || report.Declined != "unsupported_tool" {
		t.Fatalf("declined reduction changed output: report=%+v", report)
	}
}
