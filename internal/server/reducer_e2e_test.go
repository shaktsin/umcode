package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

const reducerCanonicalMarker = "UNIQUE_CANONICAL_PASS_OUTPUT_47"

type reducerFixtureTool struct {
	name   string
	output string
}

func (f *reducerFixtureTool) Name() string          { return f.name }
func (*reducerFixtureTool) Description() string     { return "Return a deterministic verification fixture" }
func (*reducerFixtureTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (*reducerFixtureTool) Assess(json.RawMessage) (tools.Risk, string) {
	return tools.RiskGreen, "Read verification fixture"
}
func (f *reducerFixtureTool) Call(context.Context, json.RawMessage) (string, error) {
	return f.output, nil
}

func reducerPassingFixture(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"status": "passed", "results": []map[string]any{{
		"label": "suite", "command": "go test ./...", "status": "passed", "duration_ms": 12,
		"output": strings.Repeat(reducerCanonicalMarker+"\n", 500),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func reducerFailingFixture(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"status": "failed", "framework": "playwright", "command": "go test ./failing", "directory": ".", "duration_ms": 14,
		"exit_code": 7, "output": "diagnostic tail: panic", "diagnostics": []string{"failed request /checkout"},
		"artifacts": []map[string]any{{"path": "/tmp/artifacts/failure.log", "kind": "trace", "mime_type": "text/plain", "bytes": 128}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + strings.Repeat(" ", 20000)
}

func reducerTurn(t *testing.T, toolName, output string, reducerOn, compilerOn bool) (string, protocol.ThreadReadResult, protocol.ThreadExportResult) {
	t.Helper()
	h := newHarness(t, func(c *config.Config) {
		c.Models.ToolResultReducers = reducerOn
		c.Models.ContextCompiler = compilerOn
	})
	h.eng.Tools.Add(&reducerFixtureTool{name: toolName, output: output})
	h.addKey("claude", "reducer", "sk-reducer-test")
	var thread protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "reducer fixture", ApprovalMode: "auto_all"}, &thread)
	if compilerOn {
		h.call(protocol.MethodThreadSetSettings, protocol.ThreadSetSettingsParams{ThreadID: thread.ID,
			Settings: protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-5", Complexity: protocol.ComplexityStandard}}, &thread)
		long := strings.Repeat("an explanation of the parser behavior ", 40)
		for i := 0; i < 14; i++ {
			h.fake.push(textReply("Explaining: " + long))
		}
		h.fake.push(toolReply("verification__plan", `{}`), textReply("Planned."))
		for i := 0; i < 14; i++ {
			if turn := runWorkTurn(h, thread, "describe the parser: "+long); turn.Status != protocol.TurnCompleted {
				t.Fatalf("setup turn %d = %+v", i, turn)
			}
		}
		if turn := runWorkTurn(h, thread, "plan verification"); turn.Status != protocol.TurnCompleted {
			t.Fatalf("planning turn = %+v", turn)
		}
	}
	var secondResult string
	h.fake.push(toolReply(tools.ToWire(toolName), `{}`), func(req llm.Request, _ string) ([]llm.Event, error) {
		compiled := false
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool && m.ToolCallID == "call_1" {
				secondResult = m.Result
			}
			for _, p := range m.Parts {
				compiled = compiled || strings.Contains(p.Text, "Current work state")
			}
		}
		if secondResult == "" {
			t.Error("second model request has no matching tool result")
		}
		if compilerOn && !compiled {
			t.Error("context compiler did not replace the earlier transcript")
		}
		return textReply("Verification observed.")(req, "")
	})
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "run verification",
		Override: protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-5", Complexity: protocol.ComplexityStandard}}, &started)
	turn, _, _ := h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	var read protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: thread.ID}, &read)
	var export protocol.ThreadExportResult
	h.call(protocol.MethodThreadExport, protocol.ThreadIDParams{ThreadID: thread.ID}, &export)
	return secondResult, read, export
}

func assertCanonicalReducerTranscript(t *testing.T, toolName, output string, read protocol.ThreadReadResult, export protocol.ThreadExportResult) {
	t.Helper()
	for _, item := range read.Items {
		if item.Tool != nil && item.Tool.CallID == "call_1" && item.Tool.Name == toolName {
			if item.Tool.Output != output {
				t.Fatal("thread/read lost the canonical verification result")
			}
			if !strings.Contains(export.Markdown, output) {
				t.Fatal("ExportThread lost the canonical verification result")
			}
			return
		}
	}
	t.Fatal("thread/read has no verification tool item")
}

func TestReducerFlagOffRequestAndTranscriptAreCanonical(t *testing.T) {
	output := reducerPassingFixture(t)
	model, read, export := reducerTurn(t, "verification.run", output, false, false)
	if model != output {
		t.Fatal("flag-off request changed the tool result")
	}
	assertCanonicalReducerTranscript(t, "verification.run", output, read, export)
}

func TestReducerFlagOnShrinksSecondRequestButPreservesTranscript(t *testing.T) {
	output := reducerPassingFixture(t)
	model, read, export := reducerTurn(t, "verification.run", output, true, false)
	if len(model)*100 > len(output)*60 || strings.Contains(model, reducerCanonicalMarker) || !strings.Contains(model, "verification: passed") {
		t.Fatalf("model projection did not save at least 40%%: original=%d sent=%d", len(output), len(model))
	}
	assertCanonicalReducerTranscript(t, "verification.run", output, read, export)
}

func TestReducerWorksWithContextCompilerOnAndOff(t *testing.T) {
	for _, compilerOn := range []bool{false, true} {
		name := "compiler_off"
		if compilerOn {
			name = "compiler_on"
		}
		t.Run(name, func(t *testing.T) {
			output := reducerPassingFixture(t)
			model, read, export := reducerTurn(t, "verification.run", output, true, compilerOn)
			if model == output || strings.Contains(model, reducerCanonicalMarker) || !strings.Contains(model, "verification: passed") {
				t.Fatalf("reducer did not project live result with compiler=%v", compilerOn)
			}
			assertCanonicalReducerTranscript(t, "verification.run", output, read, export)
		})
	}
}

func TestFailingReductionKeepsCommandExitTailAndArtifact(t *testing.T) {
	output := reducerFailingFixture(t)
	model, read, export := reducerTurn(t, "browser.verify", output, true, false)
	if model == output {
		t.Fatal("failing result was not reduced")
	}
	for _, required := range []string{"go test ./failing", "exit_code: 7", "diagnostic tail: panic", "artifact: /tmp/artifacts/failure.log", "kind: trace"} {
		if !strings.Contains(model, required) {
			t.Fatalf("model result lost %q: %s", required, model)
		}
	}
	assertCanonicalReducerTranscript(t, "browser.verify", output, read, export)
}
