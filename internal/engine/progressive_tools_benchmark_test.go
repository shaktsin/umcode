//go:build darwin || linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type schemaSpecialist struct{ *engineTestTool }

func (*schemaSpecialist) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"query": map[string]string{"type": "string", "description": strings.Repeat("Detailed specialist parameter semantics and accepted values. ", 20)}}})
	return b
}

type progressiveMeasurement struct {
	calls, tokens, schemaTokens int
	artifact                    string
}

func measureProgressiveRequests(reqs []llm.Request) progressiveMeasurement {
	m := progressiveMeasurement{calls: len(reqs)}
	for _, req := range reqs {
		m.schemaTokens += toolSpecTokens(req.Tools)
		m.tokens += tokens(req.System) + toolSpecTokens(req.Tools) + estimateMessageTokens(req.Messages)
	}
	return m
}
func runProgressiveFixture(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn, text string) {
	t.Helper()
	e.runTurn(t.Context(), th, turn, resolved{sel: protocol.ModelSelection{Provider: "memory-script", Model: "deterministic"}, meta: models.Meta{ContextWindow: 200000, Tools: true}, limits: protocol.ExecutionLimits{MaxDurationMinutes: 2, MaxTokens: 1000000, MaxCostUSD: 100, MaxToolRounds: 10}}, protocol.TurnStartParams{Text: text}, func() {})
	turns, err := e.Store.ListTurns(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range turns {
		if got.ID == turn.ID {
			if got.Status != protocol.TurnCompleted {
				t.Fatalf("turn status=%s error=%s", got.Status, got.Error)
			}
			return
		}
	}
	t.Fatal("turn missing")
}
func progressiveCoding(t *testing.T, progressive, retrieval, selectionFailure, retrievalFailure bool) progressiveMeasurement {
	t.Helper()
	e, th, turn, root := retrievalObservedFixture(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = progressive
	e.Cfg.Models.ContextRetrieval = retrieval
	if selectionFailure {
		e.selectionHook = func() { panic("private selector detail") }
	}
	if retrievalFailure {
		retrievalHook = func(context.Context) { panic("private retrieval detail") }
		t.Cleanup(func() { retrievalHook = nil })
	}
	for i := 0; i < 20; i++ {
		e.Tools.Add(&schemaSpecialist{&engineTestTool{name: fmt.Sprintf("special%d.action", i), risk: tools.RiskGreen}})
	}
	provider.script = func(req llm.Request) []llm.Event {
		if len(provider.requests) == 1 {

			hasExcerpt := strings.Contains(joined(req.Messages), `"Kind":"excerpt"`)
			if hasExcerpt != (retrieval && !retrievalFailure) {
				t.Fatalf("independent retrieval lost: excerpt=%v", hasExcerpt)
			}
		}
		seen := map[string]bool{}
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool {
				if m.IsError {
					t.Fatal(m.Result)
				}
				seen[tools.FromWire(m.ToolName)] = true
				if tools.FromWire(m.ToolName) == "verification.run" && !strings.Contains(m.Result, `"status":"passed"`) {
					t.Fatal(m.Result)
				}
			}
		}
		if !seen["file.read"] {
			return memoryCall("file.read", `{"path":"result.go"}`)
		}
		if !seen["file.write"] {
			return memoryCall("file.write", `{"path":"result.go","content":"package retrievalfixture\nfunc Value() int { return 42 }\n"}`)
		}
		if !seen["verification.run"] {
			return memoryCall("verification.run", `{"checks":[{"label":"repository tests","command":"go test ./..."}]}`)
		}
		return memoryAnswer(req)
	}
	runProgressiveFixture(t, e, th, turn, "Complete the QUASAR repository change and verify it.")
	m := measureProgressiveRequests(provider.requests)
	if selectionFailure || retrievalFailure {
		if got := requestHasTool(provider.requests[0], "special0.action"); got != (!progressive || selectionFailure) {
			t.Fatal("one feature failure disabled the independent selector")
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "result.go"))
	if err != nil {
		t.Fatal(err)
	}
	m.artifact = string(b)
	if m.artifact != "package retrievalfixture\nfunc Value() int { return 42 }\n" || m.calls != 4 {
		t.Fatalf("artifact or actual tool rounds changed: %+v", m)
	}
	return m
}
func TestProgressiveCodingSavingsWithoutQualityLoss(t *testing.T) {
	var baseline, treatment progressiveMeasurement
	t.Run("baseline", func(t *testing.T) { baseline = progressiveCoding(t, false, false, false, false) })
	t.Run("progressive", func(t *testing.T) { treatment = progressiveCoding(t, true, false, false, false) })
	t.Logf("baseline calls=%d rounds=%d schema=%d total_input=%d; progressive calls=%d rounds=%d schema=%d total_input=%d", baseline.calls, baseline.calls-1, baseline.schemaTokens, baseline.tokens, treatment.calls, treatment.calls-1, treatment.schemaTokens, treatment.tokens)
	if treatment.artifact != baseline.artifact || treatment.calls != baseline.calls || treatment.schemaTokens >= baseline.schemaTokens || treatment.tokens >= baseline.tokens {
		t.Fatalf("no task savings: baseline=%+v treatment=%+v", baseline, treatment)
	}
}
func TestPhase4FlagsIndependent(t *testing.T) {
	for _, r := range []bool{false, true} {
		for _, p := range []bool{false, true} {
			t.Run(fmt.Sprintf("retrieval=%v/progressive=%v", r, p), func(t *testing.T) { progressiveCoding(t, p, r, false, false) })
		}
	}
	t.Run("retrieval-fails-progressive-works", func(t *testing.T) { progressiveCoding(t, true, true, false, true) })
	t.Run("selection-fails-retrieval-works", func(t *testing.T) { progressiveCoding(t, true, true, true, false) })
}
func TestProgressiveSpecialistDirectAndDiscovery(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(fmt.Sprintf("direct=%v", direct), func(t *testing.T) {
			run := func(enabled bool) progressiveMeasurement {
				e, th, turn, _ := pluginHookEngine(t)
				provider := installMemoryProvider(t, e)
				e.Cfg.Models.ProgressiveTools = enabled
				target := &engineTestTool{name: "web.specialist", risk: tools.RiskGreen, output: "specialist verified result"}
				e.Tools.Add(&schemaSpecialist{target})
				text := "Complete the specialist task."
				if direct {
					text = "Look up the requested information."
				}
				provider.script = func(req llm.Request) []llm.Event {
					seen := false
					for _, m := range req.Messages {
						if tools.FromWire(m.ToolName) == target.Name() {
							if m.IsError || m.Result != target.output {
								t.Fatalf("specialist result=%+v", m)
							}
							seen = true
						}
					}
					if seen {
						return memoryAnswer(req)
					}
					if !requestHasTool(req, target.Name()) {
						if direct {
							t.Fatal("explicit web family absent")
						}
						return memoryCall("tools.discover", `{"query":"specialist"}`)
					}
					return memoryCall(target.Name(), `{}`)
				}
				runProgressiveFixture(t, e, th, turn, text)
				if target.calls.Load() != 1 {
					t.Fatal("specialist not executed exactly once")
				}
				return measureProgressiveRequests(provider.requests)
			}
			baseline, treatment := run(false), run(true)
			expected := 2
			if !direct {
				expected = 3
			}
			if treatment.calls != expected {
				t.Fatal("unexpected provider rounds")
			}
			t.Logf("baseline calls=%d schema=%d total_input=%d; progressive calls=%d schema=%d total_input=%d", baseline.calls, baseline.schemaTokens, baseline.tokens, treatment.calls, treatment.schemaTokens, treatment.tokens)
		})
	}
}
