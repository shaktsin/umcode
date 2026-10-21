package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/policy"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/work"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requestHasTool(req llm.Request, name string) bool {
	for _, s := range req.Tools {
		if s.Name == tools.ToWire(name) {
			return true
		}
	}
	return false
}
func TestProgressiveDiscoveryNextRequest(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	target := &engineTestTool{name: "special.action", risk: tools.RiskGreen, output: "specialist completed"}
	e.Tools.Add(target)
	provider.script = func(req llm.Request) []llm.Event {
		switch len(provider.requests) {
		case 1:
			if requestHasTool(req, "special.action") || !requestHasTool(req, "tools.discover") {
				t.Fatal("initial discovery/core selection missing")
			}
			return memoryCall("tools.discover", `{"names":["special.action"]}`)
		case 2:
			if !requestHasTool(req, "special.action") || target.calls.Load() != 0 {
				t.Fatal("discovery executed target or next schema missing")
			}
			return memoryCall("special.action", `{}`)
		default:
			return memoryAnswer(req)
		}
	}
	runMemoryTurn(t, e, th, turn)
	if target.calls.Load() != 1 || len(provider.requests) != 3 {
		t.Fatal("discovery changed call lifecycle")
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.Tool != nil && it.Tool.Name == "tools.discover" {
			found = true
			if it.Status != protocol.ItemCompleted || it.Tool.Output == "" {
				t.Fatal("discovery result not persisted")
			}
		}
	}
	if !found {
		t.Fatal("discovery item absent")
	}
}
func TestProgressiveGuessedToolPinsSchema(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	target := &engineTestTool{name: "special.action", risk: tools.RiskGreen, output: "done"}
	e.Tools.Add(target)
	provider.script = func(req llm.Request) []llm.Event {
		if len(provider.requests) == 1 {
			if requestHasTool(req, "special.action") {
				t.Fatal("specialist initially visible")
			}
			return memoryCall("special.action", `{}`)
		}
		if !requestHasTool(req, "special.action") {
			t.Fatal("guessed permitted tool not pinned")
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if target.calls.Load() != 1 {
		t.Fatal("permitted guessed call blocked")
	}
}
func TestProgressiveSnapshotIdentity(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	original := &engineTestTool{name: "special.action", risk: tools.RiskGreen, output: "original"}
	replacement := &engineTestTool{name: "special.action", risk: tools.RiskGreen, output: "replacement"}
	e.Tools.Add(original)
	provider.script = func(req llm.Request) []llm.Event {
		if len(provider.requests) == 1 {
			e.Tools.Add(replacement)
			return memoryCall("special.action", `{}`)
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if replacement.calls.Load() != 0 {
		t.Fatal("frozen tool silently replaced")
	}
}

func TestProgressiveGuessedToolAndRevocation(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	e.Cfg.MCPServers = []config.MCPServerConfig{{Name: "server_name"}}
	target := &engineTestTool{name: "mcp_server_name_action", risk: tools.RiskGreen, output: "must not run"}
	e.Tools.Add(&catalogMetadataTool{engineTestTool: target, family: "mcp:server_name", origin: "mcp:server_name"})
	provider.script = func(req llm.Request) []llm.Event {
		switch len(provider.requests) {
		case 1:
			return memoryCall("tools.discover", `{"names":["mcp_server_name_action"]}`)
		case 2:
			if !requestHasTool(req, "mcp_server_name_action") {
				t.Fatal("permitted schema missing")
			}
			disabled := false
			e.Cfg.MCPServers[0].Enabled = &disabled
			return memoryCall("mcp_server_name_action", `{}`)
		case 3:
			if requestHasTool(req, "mcp_server_name_action") || target.calls.Load() != 0 {
				t.Fatal("revocation ignored")
			}
			return memoryCall("tools.discover", `{"names":["mcp_server_name_action"]}`)
		default:
			for _, m := range req.Messages {
				if tools.FromWire(m.ToolName) == "tools.discover" && strings.Contains(m.Result, `"entries":[]`) {
					return memoryAnswer(req)
				}
			}
			t.Fatal("revoked tool rediscovered")
			return nil
		}
	}
	runMemoryTurn(t, e, th, turn)
	if target.calls.Load() != 0 {
		t.Fatal("revoked target executed")
	}
}

func TestProgressiveFallbackAndPairing(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	target := &engineTestTool{name: "special.action", risk: tools.RiskGreen, output: "done"}
	e.Tools.Add(target)
	provider.script = func(req llm.Request) []llm.Event {
		if len(provider.requests) == 1 {
			calls := memoryCall("tools.discover", `{"names":["special.action"]}`)
			calls[0].ToolCall.ID = "discover"
			other := memoryCall("special.action", `{}`)
			other[0].ToolCall.ID = "action"
			return append(calls, other...)
		}
		results := map[string]bool{}
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool {
				results[m.ToolCallID] = true
			}
		}
		if !results["discover"] || !results["action"] || !requestHasTool(req, "special.action") {
			t.Fatal("sibling result pairing lost")
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if target.calls.Load() != 1 || len(provider.requests) != 2 {
		t.Fatal("unexpected sibling calls")
	}
}

func TestProgressiveSelectionPanicUsesFullCatalog(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	target := &engineTestTool{name: "special.action", risk: tools.RiskGreen, output: "done"}
	e.Tools.Add(target)
	e.selectionHook = func() { panic("private selector detail") }
	provider.script = func(req llm.Request) []llm.Event {
		if !requestHasTool(req, "special.action") {
			t.Fatal("panic fallback lost permitted tools")
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if len(provider.requests) != 1 || target.calls.Load() != 0 {
		t.Fatal("fallback added provider/action call")
	}
}

func TestProgressiveFlagOffRequestEquivalent(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	target := &engineTestTool{name: "special.action", risk: tools.RiskGreen, output: "done"}
	e.Tools.Add(target)
	var expected []llm.ToolSpec
	for _, c := range e.turnTools(nil) {
		if toolAllowed(c.tool.Name(), nil) {
			expected = append(expected, llm.ToolSpec{Name: tools.ToWire(c.tool.Name()), Description: c.tool.Description(), Schema: c.tool.Schema()})
		}
	}
	e.selectionHook = func() { t.Fatal("flag-off selector invoked") }
	provider.script = func(req llm.Request) []llm.Event {
		a, _ := json.Marshal(req.Tools)
		b, _ := json.Marshal(expected)
		if !bytes.Equal(a, b) || strings.Contains(req.System, discoveryInstruction) {
			t.Fatal("flag-off request changed")
		}
		if len(provider.requests) == 1 {
			return memoryCall("special.action", `{}`)
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if target.calls.Load() != 1 {
		t.Fatal("flag-off dispatch changed")
	}
}

func TestProgressiveCatalogCollisionCannotGuessIdentity(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	a := &engineTestTool{name: "odd.a", risk: tools.RiskGreen, output: "a"}
	b := &engineTestTool{name: "odd__a", risk: tools.RiskGreen, output: "b"}
	e.Tools.Add(a)
	e.Tools.Add(b)
	provider.script = func(req llm.Request) []llm.Event {
		if len(provider.requests) == 1 {
			return memoryCall("odd__a", `{}`)
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if a.calls.Load() != 0 || b.calls.Load() != 0 {
		t.Fatal("ambiguous provider wire identity executed")
	}
}

func TestProgressiveCollisionResolution(t *testing.T) {
	a := &engineTestTool{name: "odd.a", risk: tools.RiskGreen}
	b := &engineTestTool{name: "odd__a", risk: tools.RiskGreen}
	catalog := []turnTool{{tool: a, spec: llm.ToolSpec{Name: "odd__a"}}, {tool: b, plugin: true, spec: llm.ToolSpec{Name: "odd__a"}}}
	if _, ok := resolveTurnTool(catalog, "odd__a"); ok {
		t.Fatal("wire alias selected a different plugin winner")
	}
}

func TestProgressiveAccounting(t *testing.T) {
	e, th, _, _ := pluginHookEngine(t)
	installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	e.Tools.Add(&engineTestTool{name: "special.action", risk: tools.RiskGreen})
	s := e.startToolSelection(t.Context(), th, nil, nil, "implement parser")
	if s.state == nil {
		t.Fatal("selection state absent")
	}
	if _, err := s.state.Discover(json.RawMessage(`{"names":["special.action"]}`)); err != nil {
		t.Fatal(err)
	}
	specs := s.specs()
	r := s.accounting(specs)
	if r.FullSchemaTokens != toolSpecTokens(s.baseline) || r.ExposedSchemaTokens != toolSpecTokens(specs) || r.DiscoverySchemaTokens <= 0 || r.PromptTokens != tokens(discoveryInstruction) || r.DiscoveryCalls != 1 || r.Additions != 1 {
		t.Fatalf("incorrect progressive attribution: %+v", r)
	}
}

func TestProgressiveDiscoveryBypassesActionHooksAndWorkflow(t *testing.T) {
	e, th, turn, st, _ := workflowEngine(t)
	installMemoryProvider(t, e)
	marker := filepath.Join(t.TempDir(), "hook")
	target := &engineTestTool{name: "special.action", risk: tools.RiskRed}
	snapshot := &fakePluginSnapshot{tool: target, hookSet: captureHookSet(t, hooks.BeforeToolUse, marker)}
	s := e.startToolSelection(t.Context(), th, snapshot, nil, "implement parser")
	ctx := context.WithValue(t.Context(), selectionContextKey{}, s)
	var before int
	if err := st.DB.QueryRow(`SELECT count(*) FROM evidence`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "discover", Name: tools.ToWire("tools.discover"), Args: json.RawMessage(`{"names":["special.action"]}`)}, snapshot)
	if out.IsError || target.calls.Load() != 0 || !requestHasTool(llm.Request{Tools: s.specs()}, target.Name()) {
		t.Fatalf("discovery blocked or acted: %+v", out)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("action hook ran: %v", err)
	}
	var after, approvals int
	_ = st.DB.QueryRow(`SELECT count(*) FROM evidence`).Scan(&after)
	_ = st.DB.QueryRow(`SELECT count(*) FROM approvals`).Scan(&approvals)
	if before != after || approvals != 0 {
		t.Fatalf("discovery created facts or approvals: %d/%d/%d", before, after, approvals)
	}
}
func TestProgressiveGuessedToolRetainsGuardAndApproval(t *testing.T) {
	for _, guard := range []bool{true, false} {
		t.Run(map[bool]string{true: "guard", false: "approval"}[guard], func(t *testing.T) {
			e, th, turn, st := pluginHookEngine(t)
			installMemoryProvider(t, e)
			th.ApprovalMode = policy.ApprovalNormal
			target := &engineTestTool{name: "special.action", risk: tools.RiskRed, forbidden: guard}
			snapshot := &fakePluginSnapshot{tool: target}
			s := e.startToolSelection(t.Context(), th, snapshot, nil, "implement parser")
			args := json.RawMessage(`{}`)
			if !guard {
				if err := st.RememberThreadDecision(t.Context(), th.ID, target.Name(), ApprovalSignature(target.Name(), args), "deny"); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.WithValue(t.Context(), selectionContextKey{}, s)
			out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "action", Name: tools.ToWire(target.Name()), Args: args}, snapshot)
			if !out.IsError || target.calls.Load() != 0 || !requestHasTool(llm.Request{Tools: s.specs()}, target.Name()) {
				t.Fatalf("guard/approval bypass or no pin: %+v", out)
			}
		})
	}
}
func TestProgressiveDiscoveryPanicFallback(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	target := &engineTestTool{name: "special.action", risk: tools.RiskGreen}
	e.Tools.Add(target)
	e.discoveryHook = func() { panic("private detail") }
	provider.script = func(req llm.Request) []llm.Event {
		if len(provider.requests) == 1 {
			return memoryCall("tools.discover", `{"query":"action"}`)
		}
		if !requestHasTool(req, target.Name()) || strings.Contains(joined(req.Messages), "private detail") {
			t.Fatal("fallback or error bounds failed")
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if len(provider.requests) != 2 || target.calls.Load() != 0 {
		t.Fatal("fallback performed extra action")
	}
}
func TestProgressiveReservedDiscoveryCannotShadow(t *testing.T) {
	for _, name := range []string{"tools.discover", "tools__discover"} {
		t.Run(name, func(t *testing.T) {
			e, th, turn, _ := pluginHookEngine(t)
			installMemoryProvider(t, e)
			target := &engineTestTool{name: name, risk: tools.RiskGreen}
			snapshot := &fakePluginSnapshot{tool: target}
			s := e.startToolSelection(t.Context(), th, snapshot, nil, "implement parser")
			ctx := context.WithValue(t.Context(), selectionContextKey{}, s)
			out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "shadow", Name: "tools__discover", Args: json.RawMessage(`{"query":"action"}`)}, snapshot)
			if !out.IsError || target.calls.Load() != 0 {
				t.Fatal("untrusted reserved tool executed")
			}
		})
	}
}

func TestProgressivePlannedBrowserCheckRetainsFamily(t *testing.T) {
	e, th, _, st := workEngine(t)
	installMemoryProvider(t, e)
	if _, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, WorkflowDepth: "guided"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Work.Observe(t.Context(), th.ID, work.Observation{Tool: "verification.plan", Risk: "green", Output: `{"checks":[{"label":"UI tests","command":"npm run test:e2e","kind":"browser","reason":"Check configured flows"}]}`}); err != nil {
		t.Fatal(err)
	}
	e.Tools.Add(&engineTestTool{name: "browser.verify", risk: tools.RiskGreen})
	s := e.startToolSelection(t.Context(), th, nil, nil, "implement parser")
	if !requestHasTool(llm.Request{Tools: s.specs()}, "browser.verify") {
		t.Fatal("typed planned browser check did not retain lifecycle family")
	}
}
