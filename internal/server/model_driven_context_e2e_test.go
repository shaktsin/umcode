package server_test

import (
	"encoding/json"
	"fmt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModelDrivenScaffoldEditsAndGrep(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "fixture", "test-secret")
	path := "docs/plans/stages/stage-1-scaffold.md"
	before := "# Task 1 Step 3\n```sh\nmkdir goodreads\ncd goodreads\nnpm install\n```\nFiles:\n- src/components/ui/rating.tsx\n- src/app/page.tsx\nNo test needed\nNot applicable\n"
	after := "# Task 1 Step 3\n```sh\ngit init\nnpm install\n```\nFiles:\n- src/app/page.tsx\nRun: npm run build. Expected: exits 0\nRun: npm run build. Expected: exits 0\n"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(h.ws, path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, path), []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": path, "content": after})
	h.fake.push(toolReply("file__read", `{"path":"`+path+`"}`), toolReply("file__write", string(args)), toolReply("shell__run", `{"command":"grep -n 'mkdir\\|rating.tsx\\|No test needed' docs/plans/stages/stage-1-scaffold.md"}`), func(req llm.Request, _ string) ([]llm.Event, error) {
		seen := false
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool && strings.Contains(m.Result, "exit_code: 1") {
				seen = true
			}
		}
		if !seen {
			t.Error("grep exit 1 not returned as factual command evidence")
		}
		return textReply("grep produced no matching lines (exit status 1).")(req, "")
	})
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID}, &th)
	var started protocol.TurnStartResult
	start := time.Now()
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Open docs/plans/stages/stage-1-scaffold.md. Make only these edits: delete mkdir goodreads and cd goodreads; add git init first in Task 1 Step 3; remove src/components/ui/rating.tsx from every file list; replace every No test needed and Not applicable step with Run: npm run build. Expected: exits 0. Then grep -n mkdir\\|rating.tsx\\|No test needed and paste the output."}, &started)
	turn, text, tools := h.waitTurn(started.Turn.ID, func(protocol.Approval) bool { return true })
	if turn.Status != protocol.TurnCompleted || !strings.Contains(text, "exit status 1") || len(tools) != 3 {
		t.Fatalf("scenario failed: %s %s tools=%d", turn.Status, turn.Error, len(tools))
	}
	got, err := os.ReadFile(filepath.Join(h.ws, path))
	if err != nil || string(got) != after {
		t.Fatal("incorrect scaffold bytes", err)
	}
	h.fake.mu.Lock()
	classifiers := len(h.fake.intentCalls)
	h.fake.mu.Unlock()
	if classifiers != 0 {
		t.Fatal("mandatory classifier called")
	}
	t.Logf("fixture calls=%d input=%d output=%d elapsed=%s correctness=exact bytes + real grep exit 1", turn.Usage.Requests, turn.Usage.InputTokens, turn.Usage.OutputTokens, time.Since(start))
}
func TestMasterSwitchDoesNotRestoreSemanticGates(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			h := newHarness(t, nil)
			h.addKey("claude", "fixture", "test-secret")
			h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: enabled}, nil)
			h.fake.push(toolReply("file__write", `{"path":"result.txt","content":"done"}`), textReply("Changed the requested file."))
			var th protocol.Thread
			h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID}, &th)
			var start protocol.TurnStartResult
			h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Write done to result.txt"}, &start)
			turn, _, _ := h.waitTurn(start.Turn.ID, func(protocol.Approval) bool { return true })
			got, err := os.ReadFile(filepath.Join(h.ws, "result.txt"))
			if turn.Status != protocol.TurnCompleted || err != nil || string(got) != "done" {
				t.Fatal(turn.Error, err)
			}
			h.fake.mu.Lock()
			n := len(h.fake.intentCalls)
			h.fake.mu.Unlock()
			if n != 0 {
				t.Fatal("intent classifier restored")
			}
		})
	}
}
func TestTurnDetailsSeparatesEstimatedAndMeasuredUsage(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "fixture", "test-secret")
	h.fake.push(textReply("Answer"))
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID}, &th)
	var start protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain this"}, &start)
	h.waitTurn(start.Turn.ID, nil)
	var details protocol.TurnDetailsResult
	h.call(protocol.MethodTurnDetails, protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: start.Turn.ID}, &details)
	if details.Diagnostics == nil || len(details.Diagnostics.Requests) == 0 {
		t.Fatal("diagnostics missing")
	}
	r := details.Diagnostics.Requests[0]
	if r.CapacityWindow == nil || r.InputEstimateTokens == nil || r.ReservedOutputTokens == nil || r.InputTokens == nil || *r.InputTokens != 1000 || r.InputEstimateOrigin == "" || r.CapacitySource != "runtime" {
		t.Fatalf("capacity estimates and measurements conflated: %+v", r)
	}
}
func TestSmallContextContinuation(t *testing.T) {
	h := newHarness(t, nil)
	key := h.addKey("claude", "fixture", "test-secret")
	var profile protocol.ModelCapacityResult
	h.call(protocol.MethodModelCapacityGet, protocol.ModelCapacityParams{CredentialID: key.ID, Model: "claude-sonnet-4-6"}, &profile)
	window := 16384
	profile.Profile.ContextWindow = &window
	h.call(protocol.MethodModelCapacitySet, protocol.ModelCapacityParams{CredentialID: key.ID, Model: profile.Profile.Model, Profile: &profile.Profile}, nil)
	h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: true}, nil)
	if err := os.WriteFile(filepath.Join(h.ws, "large.txt"), []byte(strings.Repeat("नमस्ते context ", 20000)), 0644); err != nil {
		t.Fatal(err)
	}
	h.fake.push(toolReply("file__read", `{"path":"large.txt"}`), func(req llm.Request, _ string) ([]llm.Event, error) {
		found := false
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool {
				found = strings.Contains(m.Result, "task.context")
				if len(m.Result) > 4096 {
					t.Error("newest result unbounded")
				}
			}
		}
		if !found {
			t.Error("source reference missing")
		}
		return textReply("Read the available excerpt; full source remains retrievable.")(req, "")
	})
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Settings: protocol.ModelSelection{Provider: "claude", Model: profile.Profile.Model}}, &th)
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Read large.txt and summarize the visible content"}, &started)
	turn, _, _ := h.waitTurn(started.Turn.ID, func(protocol.Approval) bool { return true })
	if turn.Status != protocol.TurnCompleted {
		t.Fatal(turn.Error)
	}
	h.fake.push(textReply("Continuing from recorded state."))
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "resume now"}, &started)
	turn, _, _ = h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted {
		t.Fatal(turn.Error)
	}
}

func TestPoolFallbackAfterMutationUsesSmallerCapacityExactlyOnce(t *testing.T) {
	h := newHarness(t, nil)
	key := h.addKey("claude", "fixture", "test-secret")
	routing := protocol.RoutingConfig{Models: []protocol.ConfiguredModel{{ID: "large", Provider: "claude", Model: "first", Enabled: true}, {ID: "small", Provider: "claude", Model: "second", Enabled: true}}, Pools: []protocol.ModelPool{{ID: "selected", Name: "Selected", Strategy: protocol.PoolPriority, Models: []string{"large", "small"}, Enabled: true}}}
	h.call(protocol.MethodRoutingSet, routing, nil)
	for model, window := range map[string]int{"first": 32768, "second": 16384} {
		var profile protocol.ModelCapacityResult
		h.call(protocol.MethodModelCapacityGet, protocol.ModelCapacityParams{CredentialID: key.ID, Model: model}, &profile)
		profile.Profile.ContextWindow = &window
		h.call(protocol.MethodModelCapacitySet, protocol.ModelCapacityParams{CredentialID: key.ID, Model: model, Profile: &profile.Profile}, nil)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "large.txt"), []byte(strings.Repeat("data\n", 4500)), 0644); err != nil {
		t.Fatal(err)
	}
	h.fake.push(func(req llm.Request, _ string) ([]llm.Event, error) {
		return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "write-once", Name: "file__write", Args: []byte(`{"path":"once.txt","content":"done"}`)}}, {Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "read-once", Name: "file__read", Args: []byte(`{"path":"large.txt","limit":5000}`)}}, {Type: llm.EventDone, Usage: llm.Usage{InputTokens: 500, OutputTokens: 100, Reported: true}}}, nil
	}, func(req llm.Request, _ string) ([]llm.Event, error) {
		if req.Model != "first" {
			t.Error("initial route unexpectedly changed")
		}
		return nil, &llm.Error{Provider: "claude", Status: 503, Body: "unavailable"}
	}, func(req llm.Request, _ string) ([]llm.Event, error) {
		if req.Model != "second" {
			t.Error("fallback did not use pool's smaller model")
		}
		found := false
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool && m.ToolCallID == "write-once" {
				found = true
			}
		}
		if !found {
			t.Error("completed mutation result lost at fallback")
		}
		return textReply("Completed once on the selected pool.")(req, "")
	})
	var th protocol.Thread
	selection := protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "selected", Complexity: protocol.ComplexityMedium}
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Settings: selection}, &th)
	var start protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Write done to once.txt exactly once, then read large.txt."}, &start)
	turn, _, items := h.waitTurn(start.Turn.ID, func(protocol.Approval) bool { return true })
	if turn.Status != protocol.TurnCompleted || turn.Resolved.Model != "second" {
		t.Fatal(turn.Error)
	}
	writes := 0
	for _, it := range items {
		if it.Tool != nil && it.Tool.Name == "file.write" {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("mutation count=%d", writes)
	}
	var read protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: th.ID}, &read)
	if read.Thread.Settings != selection {
		t.Fatal("fallback overwrote persisted pool")
	}
	var details protocol.TurnDetailsResult
	h.call(protocol.MethodTurnDetails, protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: turn.ID}, &details)
	last := protocol.RequestDiagnostics{}
	for _, r := range details.Diagnostics.Requests {
		if r.Phase == "chat" && r.Model == "second" {
			last = r
		}
	}
	if last.CapacityWindow == nil || *last.CapacityWindow != 16384 || last.ReservedOutputTokens == nil || last.InputEstimateTokens == nil || last.CapacityMargin == nil || *last.InputEstimateTokens+*last.ReservedOutputTokens+*last.CapacityMargin > 16384 {
		t.Fatal("fallback was not re-budgeted", last)
	}
}
