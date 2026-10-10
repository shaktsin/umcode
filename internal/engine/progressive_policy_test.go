package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/computeruse"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/policy"
	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"os"
	"strings"
	"testing"
	"time"
)

type selectionDesktopDriver struct{}

func (*selectionDesktopDriver) List(context.Context) ([]computeruse.App, error) {
	return []computeruse.App{{Name: "Fixture"}}, nil
}
func (*selectionDesktopDriver) Open(_ context.Context, t computeruse.Target) (computeruse.Target, error) {
	return t, nil
}
func (*selectionDesktopDriver) Inspect(_ context.Context, _ computeruse.Target, path string) (computeruse.State, error) {
	return computeruse.State{App: computeruse.App{Name: "Fixture"}, Permission: "ready"}, os.WriteFile(path, []byte("png"), 0600)
}
func (*selectionDesktopDriver) Act(context.Context, computeruse.Target, computeruse.Action) error {
	return nil
}
func selectionProject(t *testing.T, e *Engine, th *protocol.Thread, pt protocol.ProjectTools) protocol.Project {
	t.Helper()
	p, err := e.Store.CreateProject(t.Context(), protocol.Project{Name: "selection", Root: t.TempDir(), Tools: pt})
	if err != nil {
		t.Fatal(err)
	}
	e.Projects = projects.New(e.Store, e.Cfg)
	th.ProjectID = p.ID
	if _, err = e.Store.DB.Exec(`UPDATE threads SET project_id=? WHERE id=?`, p.ID, th.ID); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestProgressiveInheritedComputerSession(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	provider := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	p := selectionProject(t, e, &th, protocol.ProjectTools{})
	if _, err := e.SetComputerUseDefault(t.Context(), protocol.ComputerUseDefaultParams{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	m := computeruse.NewManagerWithDriver(t.Context(), &selectionDesktopDriver{})
	e.ComputerUse = m
	t.Cleanup(m.Close)
	if _, err := m.Start(t.Context(), th.ID, p.Root, computeruse.Target{Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	tools.RegisterBuiltins(e.Tools, e.Cfg, tools.NewWorkspaces(e.Cfg), nil, tools.BuiltinServices{ComputerUse: m})
	provider.script = func(req llm.Request) []llm.Event {
		if len(provider.requests) == 1 {
			if !requestHasTool(req, "computer.inspect") || !requestHasTool(req, "computer.stop") {
				t.Fatal("inherited permission lost required session tools")
			}
			return memoryCall("computer.stop", `{}`)
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
	if m.ForThread(th.ID) != nil {
		t.Fatal("existing session could not be stopped")
	}
	if len(e.requiredToolFamilies("foreign")) != 0 {
		t.Fatal("foreign session inherited")
	}
}

type scopeSelectionTool struct {
	*engineTestTool
	scope *tools.Scope
}

func (s *scopeSelectionTool) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	s.calls.Add(1)
	cp := *tools.ScopeFrom(ctx)
	s.scope = &cp
	return "done", nil
}
func TestProgressiveCurrentScopeAtAction(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	installMemoryProvider(t, e)
	yes, no := true, false
	cpu, mem, disk := 4, 4096, 2048
	p := selectionProject(t, e, &th, protocol.ProjectTools{Network: &yes, Compute: &yes, ComputeVCPUs: &cpu, ComputeMemoryMiB: &mem, ComputeDiskMiB: &disk})
	target := &scopeSelectionTool{engineTestTool: &engineTestTool{name: "special.scope", risk: tools.RiskGreen}}
	e.Tools.Add(target)
	s := e.startToolSelection(t.Context(), th, nil, &p, "implement parser")
	ctx := context.WithValue(t.Context(), selectionContextKey{}, s)
	ctx = tools.WithScope(ctx, &tools.Scope{ThreadID: th.ID, ProjectID: p.ID, Root: p.Root, AllowNet: true, UseCompute: true, ComputeVCPUs: 4, ComputeMemoryMiB: 4096, ComputeDiskMiB: 2048})
	cpu, mem, disk = 1, 512, 1024
	if _, err := e.Projects.Update(t.Context(), protocol.ProjectUpdateParams{ProjectID: p.ID, Tools: &protocol.ProjectTools{Network: &no, Compute: &no, ComputeVCPUs: &cpu, ComputeMemoryMiB: &mem, ComputeDiskMiB: &disk}}); err != nil {
		t.Fatal(err)
	}
	out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "scope", Name: tools.ToWire(target.Name()), Args: json.RawMessage(`{}`)}, nil)
	if out.IsError || target.scope == nil || target.scope.AllowNet || target.scope.UseCompute || target.scope.ComputeVCPUs != 1 || target.scope.ComputeMemoryMiB != 512 || target.scope.ComputeDiskMiB != 1024 || target.scope.Root != p.Root {
		t.Fatalf("action used stale capabilities: %+v %+v", out, target.scope)
	}
}
func TestProgressiveRevocationDuringApproval(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	installMemoryProvider(t, e)
	th.ApprovalMode = policy.ApprovalNormal
	yes := true
	p := selectionProject(t, e, &th, protocol.ProjectTools{ComputerUse: &yes})
	target := &engineTestTool{name: "computer.action", risk: tools.RiskRed}
	e.Tools.Add(target)
	s := e.startToolSelection(t.Context(), th, nil, &p, "operate desktop")
	ctx := context.WithValue(t.Context(), selectionContextKey{}, s)
	ctx = tools.WithScope(ctx, &tools.Scope{ThreadID: th.ID, ProjectID: p.ID, Root: p.Root, AllowComputerUse: true})
	done := make(chan toolRunResult, 1)
	go func() {
		done <- e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "action", Name: tools.ToWire(target.Name()), Args: json.RawMessage(`{}`)}, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	var approval protocol.Approval
	for time.Now().Before(deadline) {
		rows, err := st.ListApprovals(t.Context(), "pending")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range rows {
			e.mu.Lock()
			live := e.approvals[a.ID] != nil
			e.mu.Unlock()
			if live {
				approval = a
				break
			}
		}
		if approval.ID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if approval.ID == "" {
		t.Fatal("approval did not become pending")
	}
	no := false
	if _, err := e.Projects.Update(t.Context(), protocol.ProjectUpdateParams{ProjectID: p.ID, Tools: &protocol.ProjectTools{ComputerUse: &no}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), approval.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if !out.IsError || target.calls.Load() != 0 {
		t.Fatalf("revocation during approval bypassed: %+v calls=%d", out, target.calls.Load())
	}
}

type oversizedSelectionTool struct{ *engineTestTool }

func (*oversizedSelectionTool) Description() string { return strings.Repeat("x", 4097) }
func TestProgressiveFallbackPolicyFailure(t *testing.T) {
	e, th, _, _ := pluginHookEngine(t)
	installMemoryProvider(t, e)
	p := selectionProject(t, e, &th, protocol.ProjectTools{})
	e.Tools.Add(&oversizedSelectionTool{&engineTestTool{name: "special.action", risk: tools.RiskGreen}})
	s := e.startToolSelection(t.Context(), th, nil, &p, "implement parser")
	if s.state != nil || len(s.specs()) == 0 {
		t.Fatal("catalog fallback not exercised")
	}
	if _, err := e.Store.DB.Exec(`ALTER TABLE projects RENAME TO unavailable_projects`); err != nil {
		t.Fatal(err)
	}
	e.refreshToolSelection(t.Context(), s)
	if len(s.specs()) != 0 {
		t.Fatal("unconfirmed fallback permissions retained schemas")
	}
}

func TestProgressiveMCPRevocationDuringApproval(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	installMemoryProvider(t, e)
	th.ApprovalMode = policy.ApprovalNormal
	e.Cfg.MCPServers = []config.MCPServerConfig{{Name: "exact_server"}}
	p := selectionProject(t, e, &th, protocol.ProjectTools{MCPServers: []string{"exact_server"}})
	target := &engineTestTool{name: "mcp_exact_server_action", risk: tools.RiskRed}
	e.Tools.Add(&catalogMetadataTool{engineTestTool: target, family: "mcp:exact_server", origin: "mcp:exact_server"})
	s := e.startToolSelection(t.Context(), th, nil, &p, "use exact_server")
	ctx := context.WithValue(t.Context(), selectionContextKey{}, s)
	done := make(chan toolRunResult, 1)
	go func() {
		done <- e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "action", Name: tools.ToWire(target.Name()), Args: json.RawMessage(`{}`)}, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	var approval protocol.Approval
	for time.Now().Before(deadline) {
		rows, err := st.ListApprovals(t.Context(), "pending")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range rows {
			e.mu.Lock()
			live := e.approvals[a.ID] != nil
			e.mu.Unlock()
			if live {
				approval = a
				break
			}
		}
		if approval.ID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if approval.ID == "" {
		t.Fatal("approval did not become pending")
	}
	if _, err := e.Projects.Update(t.Context(), protocol.ProjectUpdateParams{ProjectID: p.ID, Tools: &protocol.ProjectTools{MCPServers: []string{"different_server"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), approval.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if !out.IsError || target.calls.Load() != 0 {
		t.Fatalf("MCP revocation during approval bypassed: %+v calls=%d", out, target.calls.Load())
	}
}
