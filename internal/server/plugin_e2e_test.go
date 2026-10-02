package server_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/plugins"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPluginEndToEndInstallUseDisableUninstall(t *testing.T) {
	h := newHarness(t, nil)
	source := writeFullPluginFixture(t, "portable", false)
	var inspection protocol.PluginInspection
	h.call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: source}, &inspection)
	var installed protocol.PluginInfo
	h.call(protocol.MethodPluginInstall, protocol.PluginInstallParams{Token: inspection.Token, ProjectID: h.proj.ID}, &installed)
	h.call(protocol.MethodPluginConfigure, protocol.PluginConfigureParams{PluginID: installed.ID, ProjectID: h.proj.ID, Settings: map[string]any{"tenant": "e2e"}}, &installed)
	record, err := h.eng.Store.GetPluginInstallation(h.ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := record.Root
	dataMarker := filepath.Join(h.eng.Cfg.Home, "plugins", "data", strings.Split(installed.ID, "/")[0], "full-plugin", "state.txt")
	if err := os.MkdirAll(filepath.Dir(dataMarker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataMarker, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}

	h.addKey("claude", "plugin-e2e", "sk-plugin-e2e")
	mcpWire := "mcp_full-plugin__echo_ping"
	h.fake.push(func(req llm.Request, _ string) ([]llm.Event, error) {
		if !strings.Contains(req.System, "FULL_PLUGIN_HOOK_CONTEXT") || !strings.Contains(req.System, "full-plugin:greet") {
			t.Errorf("first request missing hook or skill context: %s", req.System)
		}
		available := map[string]bool{}
		for _, tool := range req.Tools {
			available[tool.Name] = true
		}
		if !available["skill__run_script"] || !available[mcpWire] {
			t.Errorf("plugin tools missing: %#v", available)
		}
		return []llm.Event{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "skill-call", Name: "skill__run_script", Args: json.RawMessage(`{"skill":"full-plugin:greet","script":"greet","args":{"name":"Ada"}}`)}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "mcp-call", Name: mcpWire, Args: json.RawMessage(`{}`)}},
			{Type: llm.EventDone, Usage: llm.Usage{Reported: true}},
		}, nil
	}, textReply("plugin capabilities worked"))
	var thread protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{Title: "plugin e2e", ProjectID: h.proj.ID, ApprovalMode: "auto_all"}, &thread)
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "use the plugin"}, &started)
	turn, _, items := h.waitTurn(started.Turn.ID, func(protocol.Approval) bool { return true })
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %#v", turn)
	}
	outputs := map[string]string{}
	for _, item := range items {
		if item.Tool != nil {
			outputs[item.Tool.Name] = item.Tool.Output
		}
	}
	if !strings.Contains(outputs["skill.run_script"], "skill-ok") || outputs["mcp_full-plugin__echo_ping"] != "mcp-ok" {
		t.Fatalf("tool outputs = %#v", outputs)
	}
	runs, err := h.eng.Store.ListPluginHookRuns(h.ctx, installed.ID, h.proj.ID, turn.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]int{}
	for _, run := range runs {
		events[run.Event]++
	}
	if events["TurnStart"] == 0 || events["BeforeToolUse"] < 2 || events["AfterToolUse"] < 2 || events["TurnComplete"] == 0 {
		t.Fatalf("hook audit events = %#v", events)
	}

	h.call(protocol.MethodPluginSetEnabled, protocol.PluginSetEnabledParams{PluginID: installed.ID, ProjectID: h.proj.ID, Enabled: false}, nil)
	h.fake.push(func(req llm.Request, _ string) ([]llm.Event, error) {
		if strings.Contains(req.System, "FULL_PLUGIN_HOOK_CONTEXT") || strings.Contains(req.System, "full-plugin:greet") {
			t.Errorf("disabled plugin remained in prompt")
		}
		for _, tool := range req.Tools {
			if tool.Name == mcpWire {
				t.Errorf("disabled plugin MCP tool remained")
			}
		}
		return textReply("disabled")(req, "")
	})
	var second protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "after disable"}, &second)
	disabledTurn, _, _ := h.waitTurn(second.Turn.ID, nil)
	if disabledTurn.Status != protocol.TurnCompleted {
		t.Fatalf("disabled turn = %#v", disabledTurn)
	}

	h.call(protocol.MethodPluginUninstall, protocol.PluginUninstallParams{PluginID: installed.ID}, nil)
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("cache root still exists: %v", err)
	}
	if data, err := os.ReadFile(dataMarker); err != nil || string(data) != "retain" {
		t.Fatalf("plugin data = %q, %v", data, err)
	}
}

func TestPluginInspectionIncludesExecutableReview(t *testing.T) {
	h := newHarness(t, nil)
	source := writeFullPluginFixture(t, "portable", false)
	var inspection protocol.PluginInspection
	h.call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: source}, &inspection)
	if len(inspection.Plugin.Executables) < 2 {
		t.Fatalf("executable review = %#v", inspection.Plugin.Executables)
	}
	foundMCP, foundHook := false, false
	for _, executable := range inspection.Plugin.Executables {
		foundMCP = foundMCP || executable.Kind == "mcp" && strings.Contains(executable.Command, "helper")
		foundHook = foundHook || executable.Kind == "hook" && strings.Contains(executable.Command, "helper")
	}
	if !foundMCP || !foundHook {
		t.Fatalf("executable review = %#v", inspection.Plugin.Executables)
	}
}

func TestCodexPluginInstallsAndRuns(t *testing.T)  { testCompatiblePluginRuns(t, "codex") }
func TestClaudePluginInstallsAndRuns(t *testing.T) { testCompatiblePluginRuns(t, "claude") }

func testCompatiblePluginRuns(t *testing.T, format string) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeFullPluginFixture(t, format, false), plugins.InstallManaged)
	if installed.Format != format {
		t.Fatalf("format = %s", installed.Format)
	}
	h.addKey("claude", format, "sk-"+format)
	h.fake.push(toolReply("skill__get_instructions", `{"skill_name":"full-plugin:greet"}`), textReply("compatibility skill worked"))
	var thread protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{Title: format + " plugin", ProjectID: h.proj.ID}, &thread)
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "use compatible plugin"}, &started)
	turn, _, items := h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || len(items) != 1 || items[0].Tool == nil || !strings.Contains(items[0].Tool.Output, "full-plugin:greet") {
		t.Fatalf("%s runtime result: turn=%#v items=%#v", format, turn, items)
	}
}

func TestPluginBeforeToolHookBlocks(t *testing.T) {
	h := newHarness(t, nil)
	installed := installServerPlugin(t, h, writeFullPluginFixture(t, "portable", true), plugins.InstallManaged)
	h.addKey("claude", "block", "sk-block")
	h.fake.push(toolReply("skill__run_script", `{"skill":"full-plugin:greet","script":"greet","args":{}}`), textReply("blocked safely"))
	var thread protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{Title: "blocked plugin", ProjectID: h.proj.ID, ApprovalMode: "auto_all"}, &thread)
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "try blocked tool"}, &started)
	turn, _, items := h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || len(items) != 1 || items[0].Tool == nil || !strings.Contains(items[0].Tool.Error, "blocked by fixture") {
		t.Fatalf("blocked runtime result: turn=%#v items=%#v", turn, items)
	}
	runs, err := h.eng.Store.ListPluginHookRuns(h.ctx, installed.ID, h.proj.ID, turn.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, run := range runs {
		if run.Event == "BeforeToolUse" && run.Blocked {
			found = true
		}
	}
	if !found {
		t.Fatalf("blocked hook audit = %#v", runs)
	}
}

func TestPluginActivationRollbackKeepsPreviousVersion(t *testing.T) {
	h := newHarness(t, nil)
	source := writeFullPluginFixture(t, "portable", false)
	installed := installServerPlugin(t, h, source, plugins.InstallLinked)
	h.addKey("claude", "rollback", "sk-rollback")
	h.fake.push(textReply("warm snapshot"))
	var thread protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{Title: "rollback plugin", ProjectID: h.proj.ID, ApprovalMode: "auto_all"}, &thread)
	var first protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "warm"}, &first)
	h.waitTurn(first.Turn.ID, nil)

	writeFixtureFile(t, filepath.Join(source, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"echo":{"type":"stdio","command":"./missing-helper","required":true}}}`)
	if err := h.callErr(protocol.MethodPluginReload, protocol.PluginIDParams{PluginID: installed.ID}); err == nil {
		t.Fatal("broken required MCP reload unexpectedly succeeded")
	}
	h.fake.push(toolReply("mcp_full-plugin__echo_ping", `{}`), textReply("rollback worked"))
	var second protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "use stable snapshot"}, &second)
	turn, _, items := h.waitTurn(second.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || len(items) != 1 || items[0].Tool == nil || items[0].Tool.Output != "mcp-ok" {
		t.Fatalf("rollback result: turn=%#v items=%#v", turn, items)
	}
}

func TestPluginInstallFailsBeforePublishingBrokenRequiredMCP(t *testing.T) {
	h := newHarness(t, nil)
	source := writeFullPluginFixture(t, "portable", false)
	writeFixtureFile(t, filepath.Join(source, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"echo":{"type":"stdio","command":"./missing-helper","required":true}}}`)
	var inspection protocol.PluginInspection
	h.call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: source}, &inspection)
	if err := h.callErr(protocol.MethodPluginInstall, protocol.PluginInstallParams{Token: inspection.Token, ProjectID: h.proj.ID}); err == nil {
		t.Fatal("broken required MCP install unexpectedly succeeded")
	}
	if installations, err := h.eng.Store.ListPluginInstallations(h.ctx); err != nil || len(installations) != 0 {
		t.Fatalf("installations after activation failure = %#v, %v", installations, err)
	}
}

func writeFullPluginFixture(t *testing.T, format string, block bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "greet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"full-plugin","version":"1.0.0","extensions":{"com.openai":{"hooks":"./hooks/hooks.json"}},"settings":[{"name":"tenant"}]}`
	if format == "codex" {
		manifest = `{"name":"full-plugin","version":"1.0.0","skills":"./skills","mcpServers":"./mcp.json","hooks":"./hooks/hooks.json"}`
		os.MkdirAll(filepath.Join(root, ".codex-plugin"), 0o700)
		writeFixtureFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), manifest)
	} else if format == "claude" {
		manifest = `{"name":"full-plugin","version":"1.0.0","skills":"./skills","mcpServers":"./mcp.json","hooks":"./hooks/hooks.json"}`
		os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o700)
		writeFixtureFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), manifest)
	} else {
		writeFixtureFile(t, filepath.Join(root, "plugin.json"), manifest)
	}
	writeFixtureFile(t, filepath.Join(root, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"echo":{"type":"stdio","command":"./helper","required":true,"env":{"UMCODE_PLUGIN_TEST_HELPER":"mcp"}}}}`)
	hookMode := "hook"
	if block {
		hookMode = "block-hook"
	}
	writeFixtureFile(t, filepath.Join(root, "hooks", "hooks.json"), `{"hooks":{"TurnStart":[{"hooks":[{"type":"command","command":"./helper","env":{"UMCODE_PLUGIN_TEST_HELPER":"hook"}}]}],"BeforeToolUse":[{"hooks":[{"type":"command","command":"./helper","env":{"UMCODE_PLUGIN_TEST_HELPER":"`+hookMode+`"}}]}],"AfterToolUse":[{"hooks":[{"type":"command","command":"./helper","env":{"UMCODE_PLUGIN_TEST_HELPER":"hook"}}]}],"TurnComplete":[{"hooks":[{"type":"command","command":"./helper","env":{"UMCODE_PLUGIN_TEST_HELPER":"hook"}}]}]}}`)
	writeFixtureFile(t, filepath.Join(root, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: E2E greeting\nrisk_level: green\nruntime:\n  type: shell\n  env:\n    UMCODE_PLUGIN_TEST_HELPER: skill\nscripts:\n  greet:\n    path: helper\n    description: Greet\n---\nUse the E2E greeting.\n")
	copyFixtureExecutable(t, filepath.Join(root, "helper"))
	copyFixtureExecutable(t, filepath.Join(root, "skills", "greet", "helper"))
	return root
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func copyFixtureExecutable(t *testing.T, destination string) {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
