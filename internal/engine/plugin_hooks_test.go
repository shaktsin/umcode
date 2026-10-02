package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/policy"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

func TestMain(m *testing.M) {
	if os.Getenv("UMCODE_ENGINE_HOOK_HELPER") != "" {
		engineHookHelper()
		return
	}
	os.Exit(m.Run())
}

func TestTurnStartHookContextReachesModel(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	snapshot := &fakePluginSnapshot{hookSet: hookSet(t, hooks.TurnStart, `{"version":1,"context":["Use the plugin release checklist."]}`)}
	outcome := e.runPluginHooks(t.Context(), snapshot, hooks.Invocation{Event: hooks.TurnStart})
	prompt := e.systemPrompt(t.Context(), "ship it", nil, "", outcome.Context)
	if !strings.Contains(prompt, "Use the plugin release checklist.") {
		t.Fatalf("turn-start context missing from prompt: %s", prompt)
	}
}

func TestPluginHookWarningCreatesUserVisibleTurnItem(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	snapshot := &fakePluginSnapshot{hookSet: hookSet(t, hooks.TurnStart, `{"version":1,"warning":"plugin needs attention"}`)}
	e.runPluginHooks(t.Context(), snapshot, hooks.Invocation{Event: hooks.TurnStart, ThreadID: th.ID, TurnID: turn.ID})
	items, err := st.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != protocol.ItemError || !strings.Contains(items[0].Text, "plugin needs attention") {
		t.Fatalf("warning items = %#v", items)
	}
}

func TestBeforeToolHookCanBlock(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	tool := &engineTestTool{name: "test.safe", risk: tools.RiskGreen, output: "ran"}
	snapshot := &fakePluginSnapshot{tool: tool, hookSet: hookSet(t, hooks.BeforeToolUse, `{"version":1,"block":true,"reason":"plugin refused"}`)}
	result := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "one", Name: tools.ToWire(tool.Name()), Args: json.RawMessage(`{}`)}, snapshot)
	if !result.IsError || !strings.Contains(result.Output, "plugin refused") || tool.calls.Load() != 0 {
		t.Fatalf("blocked result = %#v, calls=%d", result, tool.calls.Load())
	}
}

func TestAfterToolAndFailureHooksObserveFinalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolErr    error
		event      hooks.Event
		wantOutput string
	}{
		{name: "success", event: hooks.AfterToolUse, wantOutput: "completed"},
		{name: "failure", toolErr: errors.New("boom"), event: hooks.ToolUseFailed, wantOutput: "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, th, turn, _ := pluginHookEngine(t)
			capture := filepath.Join(t.TempDir(), "hook-envelope.json")
			tool := &engineTestTool{name: "test.observe", risk: tools.RiskGreen, output: "completed", err: tc.toolErr}
			snapshot := &fakePluginSnapshot{tool: tool, hookSet: captureHookSet(t, tc.event, capture)}
			result := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "observe", Name: tools.ToWire(tool.Name()), Args: json.RawMessage(`{"x":1}`)}, snapshot)
			if result.IsError != (tc.toolErr != nil) {
				t.Fatalf("result = %#v", result)
			}
			data, err := os.ReadFile(capture)
			if err != nil || !strings.Contains(string(data), tc.wantOutput) {
				t.Fatalf("captured envelope = %q, %v", data, err)
			}
		})
	}
}

func TestPluginHookCannotBypassToolPolicy(t *testing.T) {
	t.Run("built-in guard runs first", func(t *testing.T) {
		e, th, turn, _ := pluginHookEngine(t)
		marker := filepath.Join(t.TempDir(), "hook-ran")
		tool := &engineTestTool{name: "test.forbidden", risk: tools.RiskGreen, forbidden: true}
		snapshot := &fakePluginSnapshot{tool: tool, hookSet: captureHookSet(t, hooks.BeforeToolUse, marker)}
		result := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "deny", Name: tools.ToWire(tool.Name())}, snapshot)
		if !result.IsError || tool.calls.Load() != 0 {
			t.Fatalf("policy result = %#v, calls=%d", result, tool.calls.Load())
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("before hook ran ahead of built-in guard: %v", err)
		}
	})
	t.Run("continue cannot grant approval", func(t *testing.T) {
		e, th, turn, st := pluginHookEngine(t)
		th.ApprovalMode = policy.ApprovalNormal
		tool := &engineTestTool{name: "test.red", risk: tools.RiskRed, output: "must not run"}
		snapshot := &fakePluginSnapshot{tool: tool, hookSet: hookSet(t, hooks.BeforeToolUse, `{"version":1,"continue":true}`)}
		args := json.RawMessage(`{"action":"sensitive"}`)
		if err := st.RememberThreadDecision(t.Context(), th.ID, tool.Name(), ApprovalSignature(tool.Name(), args), "deny"); err != nil {
			t.Fatal(err)
		}
		result := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "approval", Name: tools.ToWire(tool.Name()), Args: args}, snapshot)
		if !result.IsError || tool.calls.Load() != 0 || !strings.Contains(result.Output, "denied") {
			t.Fatalf("approval result = %#v, calls=%d", result, tool.calls.Load())
		}
	})
}

func TestRedPluginSkillRequiresApproval(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	th.ApprovalMode = policy.ApprovalNormal
	skillRoot := filepath.Join(t.TempDir(), "danger")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: danger\ndescription: dangerous plugin skill\nrisk_level: red\nscripts:\n  run:\n    path: run.sh\n---\nDangerous.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "run.sh"), []byte("#!/bin/sh\necho ran\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	skillSnapshot, errs := skills.CompileSnapshot([]skills.Root{{PluginID: "plugin", Paths: []string{skillRoot}}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	e.Skills.Register(e.Tools)
	snapshot := &fakePluginSnapshot{skills: skillSnapshot}
	args := json.RawMessage(`{"skill":"plugin:danger","script":"run","args":{}}`)
	if err := st.RememberThreadDecision(t.Context(), th.ID, "skill.run_script", ApprovalSignature("skill.run_script", args), "deny"); err != nil {
		t.Fatal(err)
	}
	ctx := skills.WithSnapshot(t.Context(), skillSnapshot)
	result := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "red-skill", Name: tools.ToWire("skill.run_script"), Args: args}, snapshot)
	if !result.IsError || !strings.Contains(strings.ToLower(result.Output), "denied") {
		t.Fatalf("red plugin skill bypassed approval: %#v", result)
	}
}

func TestTurnCompleteHookRunsOnFailureAndSuccess(t *testing.T) {
	e, _, turn, _ := pluginHookEngine(t)
	capture := filepath.Join(t.TempDir(), "turn-complete")
	snapshot := &fakePluginSnapshot{hookSet: captureHookSet(t, hooks.TurnComplete, capture)}
	for _, turnErr := range []error{nil, errors.New("turn failed")} {
		if outcome := e.runTurnCompleteHooks(t.Context(), snapshot, turn, turnErr); len(outcome.Records) != 1 {
			t.Fatalf("turn complete records = %#v", outcome.Records)
		}
	}
	data, err := os.ReadFile(capture)
	if err != nil || strings.Count(string(data), `"event":"TurnComplete"`) != 2 {
		t.Fatalf("turn-complete capture = %q, %v", data, err)
	}
}

func TestCompactionHooksRunForManualAndAutomaticCompaction(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	capture := filepath.Join(t.TempDir(), "compaction")
	snapshot := &fakePluginSnapshot{hookSet: hooks.Set{Declarations: append(
		captureHookSet(t, hooks.BeforeCompaction, capture).Declarations,
		captureHookSet(t, hooks.AfterCompaction, capture).Declarations...,
	)}}
	for _, currentTurn := range []string{"", turn.ID} {
		if err := e.withCompactionHooks(t.Context(), snapshot, th, currentTurn, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(capture)
	if err != nil || strings.Count(string(data), `BeforeCompaction`) != 2 || strings.Count(string(data), `AfterCompaction`) != 2 {
		t.Fatalf("compaction capture = %q, %v", data, err)
	}
}

func TestTurnRetainsPluginSnapshotAcrossReload(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	oldSnapshot := &fakePluginSnapshot{tool: &engineTestTool{name: "plugin.echo", risk: tools.RiskGreen, output: "v1"}}
	currentSnapshot := &fakePluginSnapshot{tool: &engineTestTool{name: "plugin.echo", risk: tools.RiskGreen, output: "v2"}}
	_ = currentSnapshot // A manager reload may replace the current snapshot, not the turn-owned reference.
	result := e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "old", Name: tools.ToWire("plugin.echo")}, oldSnapshot)
	if result.IsError || result.Output != "v1" {
		t.Fatalf("turn snapshot changed across reload: %#v", result)
	}
}

type fakePluginSnapshot struct {
	tool    tools.Tool
	hookSet hooks.Set
	skills  *skills.Snapshot
}

func (s *fakePluginSnapshot) Tools() []tools.Tool {
	if s.tool == nil {
		return nil
	}
	return []tools.Tool{s.tool}
}
func (s *fakePluginSnapshot) Tool(name string) (tools.Tool, bool) {
	if s.tool != nil && (s.tool.Name() == name || tools.ToWire(s.tool.Name()) == name) {
		return s.tool, true
	}
	return nil, false
}
func (s *fakePluginSnapshot) SkillSnapshot() *skills.Snapshot { return s.skills }
func (s *fakePluginSnapshot) Hooks() hooks.Set                { return s.hookSet }
func (s *fakePluginSnapshot) Release()                        {}

type engineTestTool struct {
	name      string
	risk      tools.Risk
	output    string
	err       error
	forbidden bool
	calls     atomic.Int32
}

func (t *engineTestTool) Name() string                                { return t.name }
func (t *engineTestTool) Description() string                         { return "engine hook test tool" }
func (t *engineTestTool) Schema() json.RawMessage                     { return json.RawMessage(`{"type":"object"}`) }
func (t *engineTestTool) Assess(json.RawMessage) (tools.Risk, string) { return t.risk, "test" }
func (t *engineTestTool) Forbidden(json.RawMessage) (string, bool) {
	return "built-in guard", t.forbidden
}
func (t *engineTestTool) Call(context.Context, json.RawMessage) (string, error) {
	t.calls.Add(1)
	return t.output, t.err
}

func pluginHookEngine(t *testing.T) (*Engine, protocol.Thread, protocol.Turn, *store.Store) {
	t.Helper()
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "plugins", ApprovalMode: policy.ApprovalAutoAll})
	if err != nil {
		t.Fatal(err)
	}
	turn := protocol.Turn{ID: store.NewID("trn"), ThreadID: th.ID, Status: protocol.TurnRunning}
	if err := st.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: t.TempDir(), Policy: config.PolicyConfig{ApprovalTimeoutMinutes: 1}}
	e := &Engine{Cfg: cfg, Store: st, Tools: tools.NewRegistry(), Skills: skills.NewRegistry(cfg), Hooks: hooks.NewRunner(nil), Bus: NewBus(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), gate: policy.New(), approvals: map[string]chan bool{}}
	return e, th, turn, st
}

func hookSet(t *testing.T, event hooks.Event, result string) hooks.Set {
	t.Helper()
	root, command := engineHookCommand(t)
	return hooks.Set{Declarations: []hooks.Declaration{{PluginID: "test", Root: root, Event: event, Command: command, Env: map[string]string{"UMCODE_ENGINE_HOOK_HELPER": "result", "HOOK_RESULT": result}}}}
}

func captureHookSet(t *testing.T, event hooks.Event, path string) hooks.Set {
	t.Helper()
	root, command := engineHookCommand(t)
	return hooks.Set{Declarations: []hooks.Declaration{{PluginID: "test", Root: root, Event: event, Command: command, Env: map[string]string{"UMCODE_ENGINE_HOOK_HELPER": "capture", "CAPTURE_PATH": path}}}}
}

func engineHookCommand(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(root, "hook-helper"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return root, "./hook-helper"
}

func engineHookHelper() {
	input, _ := io.ReadAll(os.Stdin)
	if os.Getenv("UMCODE_ENGINE_HOOK_HELPER") == "capture" {
		path := os.Getenv("CAPTURE_PATH")
		file, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		_, _ = file.Write(append(input, '\n'))
		_ = file.Close()
	}
	result := os.Getenv("HOOK_RESULT")
	if result == "" {
		result = `{"version":1,"continue":true}`
	}
	_, _ = io.WriteString(os.Stdout, result)
}
