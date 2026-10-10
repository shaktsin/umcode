package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/router"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/work"
)

const memoryCommandText = "Use go test ./... for this repository"
const memoryVerificationCommand = `GOCACHE="$PWD/.go-cache" go test ./...`

// Only the external provider is scripted; prompt composition, looping, tool
// execution, Work observation/completion, promotion and persistence remain real.
type memoryScriptProvider struct {
	requests []llm.Request
	script   func(llm.Request) []llm.Event
}

func (*memoryScriptProvider) ID() string { return "memory-script" }
func (*memoryScriptProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *memoryScriptProvider) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	// Copy the request: later loop appends must not change measured inputs.
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var captured llm.Request
	if err := json.Unmarshal(b, &captured); err != nil {
		return nil, err
	}
	p.requests = append(p.requests, captured)
	events := p.script(req)
	ch := make(chan llm.Event, len(events)+1)
	for _, event := range events {
		ch <- event
	}
	ch <- llm.Event{Type: llm.EventDone}
	close(ch)
	return ch, nil
}
func memoryCall(name, args string) []llm.Event {
	return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: name, Name: tools.ToWire(name), Args: json.RawMessage(args)}}}
}
func memoryAnswer(llm.Request) []llm.Event {
	return []llm.Event{{Type: llm.EventTextDelta, Text: "Done; verification passed."}}
}

func installMemoryProvider(t *testing.T, e *Engine) *memoryScriptProvider {
	t.Helper()
	p := &memoryScriptProvider{script: memoryAnswer}
	e.LLMs = llm.NewRegistry()
	e.LLMs.Register(p)
	e.Router = router.New(e.Store, nil, nil, e.LLMs, e.Cfg, e.Log)
	e.Cfg.Tools.HostSandbox = "off"
	tools.RegisterBuiltins(e.Tools, e.Cfg, tools.NewWorkspaces(e.Cfg), nil)
	return p
}
func runMemoryTurn(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn) {
	t.Helper()
	e.runTurn(t.Context(), th, turn, resolved{
		sel:    protocol.ModelSelection{Provider: "memory-script", Model: "deterministic"},
		meta:   models.Meta{ContextWindow: 200000, Tools: true},
		limits: protocol.ExecutionLimits{MaxDurationMinutes: 2, MaxTokens: 1000000, MaxCostUSD: 100, MaxToolRounds: 10},
	}, protocol.TurnStartParams{Text: "Complete the repository change and verify it."}, func() {})
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
func nextMemoryTurn(t *testing.T, e *Engine, th protocol.Thread) protocol.Turn {
	t.Helper()
	turn := protocol.Turn{ID: store.NewID("trn"), ThreadID: th.ID, Status: protocol.TurnRunning}
	if err := e.Store.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	return turn
}
func assertMemoryCompletion(t *testing.T, e *Engine, id string) protocol.WorkDetail {
	t.Helper()
	d, err := e.Store.GetWorkDetail(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Work.Status != protocol.WorkCompleted || len(work.CompletionBlockers(d)) != 0 {
		t.Fatalf("unsupported completion: work=%+v blockers=%v", d.Work, work.CompletionBlockers(d))
	}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeCriterion && n.Status != protocol.AttemptPassed {
			t.Fatalf("criterion did not pass: %+v", n)
		}
	}
	return d
}

// Removing completion promotion, instruction injection or duplicate suppression
// must lose the observed second-turn savings without changing the coding task.
func TestCuratedMemoryReducesRepeatedDiscoveryWithoutQualityLoss(t *testing.T) {
	type result struct {
		calls, secondCalls, discovery, rounds, inputBytes int
		inputTokens                                       int64
		answer, artifact                                  string
		completed, passed                                 int
	}
	run := func(t *testing.T, auto bool) result {
		e, th, turn, p, id, _ := memoryEngine(t)
		e.Cfg.Memory.AutoPromote = auto
		provider := installMemoryProvider(t, e)
		e.initMemory(t.Context())
		// The discovery output models a real project checklist: the useful command
		// is short, but discovering it costs a tool request plus the inspected text.
		for name, body := range map[string]string{
			"go.mod":         "module memoryfixture\n\ngo 1.24\n",
			"result_test.go": "package memoryfixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 42 { t.Fatal(Value()) } }\n",
			"CHECKS.md":      "# Repository checks\nRun go test ./...\n" + strings.Repeat("The repository suite checks the exported value contract.\n", 100),
			"UMCODE.md":      "# Repository guidance\nKeep exported behavior covered.\n",
		} {
			if err := os.WriteFile(filepath.Join(p.Root, name), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		var out result
		for i := 0; i < 2; i++ {
			if i == 1 {
				turn = nextMemoryTurn(t, e, th)
				id = seedMemoryWork(t, e, th, p)
			}
			criterion, err := json.Marshal(map[string]string{"command": memoryVerificationCommand})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Store.DB.Exec(`UPDATE work_nodes SET content_json=? WHERE work_id=? AND kind='criterion'`, string(criterion), id); err != nil {
				t.Fatal(err)
			}
			begin := len(provider.requests)
			provider.script = func(req llm.Request) []llm.Event {
				seen := map[string]bool{}
				// Ignore historical tool summaries: decisions use the current turn only.
				start := 0
				for j, msg := range req.Messages {
					if msg.Role == llm.RoleUser {
						start = j
					}
				}
				for _, msg := range req.Messages[start:] {
					if msg.Role == llm.RoleTool {
						if msg.IsError {
							t.Fatalf("tool %s failed: %s", msg.ToolName, msg.Result)
						}
						seen[tools.FromWire(msg.ToolName)] = true
						if tools.FromWire(msg.ToolName) == "verification.run" && !strings.Contains(msg.Result, `"status":"passed"`) {
							t.Fatalf("verification failed: %s", msg.Result)
						}
					}
				}
				if !strings.Contains(req.System, memoryCommandText) && !seen["file.read"] {
					return memoryCall("file.read", `{"path":"CHECKS.md"}`)
				}
				if !seen["file.write"] {
					return memoryCall("file.write", `{"path":"result.go","content":"package memoryfixture\nfunc Value() int { return 42 }\n"}`)
				}
				if !seen["verification.run"] {
					args, err := json.Marshal(map[string]any{"checks": []map[string]string{{"label": "repository tests", "command": memoryVerificationCommand}}})
					if err != nil {
						t.Fatal(err)
					}
					return memoryCall("verification.run", string(args))
				}
				return memoryAnswer(req)
			}
			runMemoryTurn(t, e, th, turn)
			d := assertMemoryCompletion(t, e, id)
			out.completed++
			actualPass := false
			for _, a := range d.Attempts {
				if a.Command == memoryVerificationCommand && a.Status == protocol.AttemptPassed && a.ExitCode != nil && *a.ExitCode == 0 {
					actualPass = true
				}
			}
			if !actualPass {
				t.Fatal("no real passing repository verification")
			}
			out.passed++
			items, err := e.Store.ListItems(t.Context(), th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.TurnID != turn.ID {
					continue
				}
				if item.Kind == protocol.ItemAgentMessage {
					out.answer = item.Text
				}
				if item.Tool != nil {
					out.rounds++
					if item.Tool.Name == "file.read" {
						out.discovery++
					}
				}
			}
			if i == 1 {
				out.secondCalls = len(provider.requests) - begin
				for _, req := range provider.requests[begin:] {
					b, err := json.Marshal(req)
					if err != nil {
						t.Fatal(err)
					}
					out.inputBytes += len(b)
					out.inputTokens += estimateInput(req)
					want := 0
					if auto {
						want = 1
					}
					if got := strings.Count(req.System, memoryCommandText); got != want {
						t.Fatalf("second-turn command occurrences=%d want %d", got, want)
					}
				}
			}
		}
		rows := memoryRows(t, e, p.ID)
		if auto {
			if len(rows) != 1 || rows[0].Text != "- "+memoryCommandText || rows[0].Status != protocol.MemoryStatusActive {
				t.Fatalf("incorrect memory=%+v", rows)
			}
		} else if len(rows) != 0 {
			t.Fatalf("flag off wrote memory=%+v", rows)
		}
		out.calls = len(provider.requests)
		b, err := os.ReadFile(filepath.Join(p.Root, "result.go"))
		if err != nil {
			t.Fatal(err)
		}
		out.artifact = string(b)
		return out
	}
	baseline := run(t, false)
	treatment := run(t, true)
	if baseline.answer != "Done; verification passed." || baseline.artifact != "package memoryfixture\nfunc Value() int { return 42 }\n" || baseline.answer != treatment.answer || baseline.artifact != treatment.artifact || baseline.completed != 2 || treatment.completed != 2 || baseline.passed != 2 || treatment.passed != 2 {
		t.Fatalf("quality mismatch: baseline=%+v treatment=%+v", baseline, treatment)
	}
	if treatment.calls > baseline.calls || treatment.secondCalls >= baseline.secondCalls || treatment.discovery >= baseline.discovery || treatment.rounds >= baseline.rounds || treatment.inputBytes >= baseline.inputBytes {
		t.Fatalf("no repeat-discovery benefit: baseline=%+v treatment=%+v", baseline, treatment)
	}
	// Exact expected scripted traffic catches promotion-specific provider calls.
	if baseline.calls != 8 || treatment.calls != 7 || baseline.discovery != 2 || treatment.discovery != 1 || baseline.rounds != 6 || treatment.rounds != 5 {
		t.Fatalf("unexpected model/tool traffic: baseline=%+v treatment=%+v", baseline, treatment)
	}
	t.Logf("baseline: model_calls=%d second_turn_calls=%d discovery_calls=%d tool_rounds=%d second_turn_input_bytes=%d estimated_second_turn_input_tokens=%d", baseline.calls, baseline.secondCalls, baseline.discovery, baseline.rounds, baseline.inputBytes, baseline.inputTokens)
	t.Logf("treatment: model_calls=%d second_turn_calls=%d discovery_calls=%d tool_rounds=%d second_turn_input_bytes=%d estimated_second_turn_input_tokens=%d", treatment.calls, treatment.secondCalls, treatment.discovery, treatment.rounds, treatment.inputBytes, treatment.inputTokens)
	t.Logf("quality: completed=2/2 verified=2/2 identical_answer=true identical_artifact=true bytes_saved=%d", baseline.inputBytes-treatment.inputBytes)
}
