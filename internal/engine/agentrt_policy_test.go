package engine

import (
	"context"
	"testing"

	"github.com/shaktsin/umcode/internal/policy"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

func TestRuntimeToolAllowedFollowsCapabilities(t *testing.T) {
	for _, files := range []protocol.FileAccess{protocol.FilesNone, protocol.FilesRead, protocol.FilesReadWrite} {
		for _, shell := range []protocol.ShellAccess{protocol.ShellNone, protocol.ShellReadOnly, protocol.ShellReadWrite} {
			for _, spawn := range []int{0, 2} {
				caps := protocol.AgentCapabilities{Files: files, Shell: shell, SpawnMax: spawn}
				want := map[string]bool{
					"file.read":              files != protocol.FilesNone,
					"file.list":              files != protocol.FilesNone,
					"file.search":            files != protocol.FilesNone,
					"file.write":             files == protocol.FilesReadWrite,
					"file.edit":              files == protocol.FilesReadWrite,
					"shell.run":              shell != protocol.ShellNone,
					"verification.run":       shell == protocol.ShellReadWrite,
					"skill.get_instructions": true,
					"task.context":           true,
					"agent.spawn":            spawn > 0,
					"agent.wait":             spawn > 0,
					"agent.cancel":           spawn > 0,
					"exec.start":             false,
					"computer.click":         false,
					"agent.review_report":    false,
				}
				for tool, w := range want {
					if got := runtimeToolAllowed(caps, tool); got != w {
						t.Errorf("%+v %s = %v, want %v", caps, tool, got, w)
					}
				}
			}
		}
	}
	if !runtimeToolAllowed(protocol.AgentCapabilities{Files: protocol.FilesRead}, tools.ToWire("file.read")) {
		t.Error("wire name should map to canonical")
	}
}

func testEngine() *Engine { return &Engine{gate: policy.New()} }

func agentCtx(conf tools.Confinement, caps protocol.AgentCapabilities, mode func() string) context.Context {
	return tools.WithScope(context.Background(), runtimeScope(&tools.Scope{Root: "/w"}, caps, conf, mode))
}

func normalMode() string { return "normal" }

func TestAgentShellInConfinementNeedsNoApproval(t *testing.T) {
	e := testEngine()
	for _, conf := range []tools.Confinement{tools.ConfOSSandbox, tools.ConfMicroVM} {
		for _, shell := range []protocol.ShellAccess{protocol.ShellReadOnly, protocol.ShellReadWrite} {
			caps := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: shell}
			ctx := agentCtx(conf, caps, normalMode)
			d, _ := e.gateDecision(ctx, "shell.run", tools.RiskRed, false, "")
			if d != policy.Allow {
				t.Errorf("%s/%s decision = %v", conf, shell, d)
			}
		}
	}
}

// exec.* sessions are refused to restricted children outright, so a confined
// agent's exec call must never be pre-approved as if the confinement covered it.
func TestConfinedAgentExecSessionsAreNotAutoAllowed(t *testing.T) {
	e := testEngine()
	caps := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite}
	ctx := agentCtx(tools.ConfOSSandbox, caps, normalMode)
	for _, name := range []string{"exec.start", "exec.write", "exec.stop", tools.ToWire("exec.start")} {
		if d, _ := e.gateDecision(ctx, name, tools.RiskRed, false, ""); d == policy.Allow {
			t.Errorf("%s auto-allowed for a confined agent", name)
		}
		if confinedShellTool(name) {
			t.Errorf("%s counted as a confined shell tool", name)
		}
	}
	for _, name := range []string{"shell.run", "verification.run"} {
		if d, _ := e.gateDecision(ctx, name, tools.RiskRed, false, ""); d != policy.Allow {
			t.Errorf("%s = %v, want allowed in confinement", name, d)
		}
	}
}

func TestAgentShellAsksWhenUnconfinedOrLead(t *testing.T) {
	e := testEngine()
	lead := tools.WithScope(context.Background(), &tools.Scope{Root: "/w"})
	if d, _ := e.gateDecision(lead, "shell.run", tools.RiskRed, false, ""); d != policy.Ask {
		t.Errorf("lead red shell = %v", d)
	}
	caps := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite}
	ctx := agentCtx(tools.ConfNone, caps, normalMode)
	if d, _ := e.gateDecision(ctx, "shell.run", tools.RiskRed, false, ""); d != policy.Ask {
		t.Errorf("confinement none must never auto-allow, got %v", d)
	}
	s := tools.ScopeFrom(ctx)
	if s.Confined {
		t.Error("Confined must be false for ConfNone")
	}
	// shell none is never confined either
	noShell := agentCtx(tools.ConfMicroVM, protocol.AgentCapabilities{Files: protocol.FilesRead}, normalMode)
	if tools.ScopeFrom(noShell).Confined {
		t.Error("Confined with shell none")
	}
}

func TestAgentInheritsLeadApprovalMode(t *testing.T) {
	e := testEngine()
	lead := "auto_workspace"
	caps := protocol.AgentCapabilities{Files: protocol.FilesRead}
	ctx := agentCtx(tools.ConfNone, caps, func() string { return lead })
	// A red non-host action, internal thread mode empty.
	if d, _ := e.gateDecision(ctx, "mcp.thing", tools.RiskRed, false, ""); d != policy.Allow {
		t.Errorf("auto_workspace lead: %v", d)
	}
	lead = "normal"
	if d, _ := e.gateDecision(ctx, "mcp.thing", tools.RiskRed, false, "auto_all"); d != policy.Ask {
		t.Errorf("flipped to normal: %v (internal thread mode must be ignored)", d)
	}
	lead = "auto_all"
	if d, _ := e.gateDecision(ctx, "mcp.thing", tools.RiskRed, false, ""); d != policy.Allow {
		t.Errorf("auto_all lead: %v", d)
	}
	nilFn := agentCtx(tools.ConfNone, caps, nil)
	if d, _ := e.gateDecision(nilFn, "mcp.thing", tools.RiskRed, false, "auto_all"); d != policy.Ask {
		t.Errorf("nil mode fn must be strictest: %v", d)
	}
}

func TestAgentNeverExceedsLeadMode(t *testing.T) {
	e := testEngine()
	caps := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite}
	ctx := agentCtx(tools.ConfNone, caps, normalMode)
	for _, tool := range []string{"mcp.thing", "shell.run", "exec.start"} {
		if d, _ := e.gateDecision(ctx, tool, tools.RiskRed, false, "auto_all"); d != policy.Ask {
			t.Errorf("%s outside confinement = %v", tool, d)
		}
	}
	// a confined agent's non-shell red action still follows the lead's mode.
	conf := agentCtx(tools.ConfOSSandbox, caps, normalMode)
	if d, _ := e.gateDecision(conf, "mcp.thing", tools.RiskRed, false, ""); d != policy.Ask {
		t.Errorf("confined non-shell red = %v", d)
	}
}

func TestRuntimeScopeCopiesAndDerivesFromCapabilities(t *testing.T) {
	base := &tools.Scope{Root: "/w", AllowNet: true, UseCompute: true, ApprovalMode: "auto_all"}
	caps := protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadOnly}
	s := runtimeScope(base, caps, tools.ConfMicroVM, normalMode)
	if s == base || base.RestrictedChild || base.RuntimeAgent || !base.AllowNet {
		t.Fatal("base mutated or aliased")
	}
	if !s.RestrictedChild || !s.RuntimeAgent || !s.ReadOnly || !s.ShellReadOnly || s.AllowNet || !s.Confined || s.ApprovalModeFn == nil {
		t.Fatalf("scope = %+v", s)
	}
	rw := runtimeScope(base, protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true}, tools.ConfOSSandbox, nil)
	if rw.ReadOnly || rw.ShellReadOnly || !rw.AllowNet {
		t.Fatalf("rw scope = %+v", rw)
	}
}

func TestAgentNetworkIgnoresProjectDefault(t *testing.T) {
	base := &tools.Scope{Root: "/w", AllowNet: true} // project network on / default_network true
	s := runtimeScope(base, protocol.AgentCapabilities{Files: protocol.FilesRead, Network: false}, tools.ConfOSSandbox, normalMode)
	if s.AllowNet {
		t.Fatal("agent inherited project network")
	}
}
