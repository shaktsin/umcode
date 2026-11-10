package engine

import (
	"context"
	"strings"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/policy"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// runtimeToolAllowed reports whether a runtime agent with caps may call tool.
// Authority comes from the capabilities alone, never from a name, purpose or
// role. skill.get_instructions and task.context are always-safe context tools.
func runtimeToolAllowed(caps protocol.AgentCapabilities, tool string) bool {
	if tool == "task.context" {
		return true
	}
	for _, allowed := range agentrt.AllowedTools(caps) {
		if tool == allowed || tools.ToWire(allowed) == tool {
			return true
		}
	}
	return false
}

// runtimeScope returns a copy of base shaped by an agent's capabilities. The
// agent's network is granted only by caps.Network, never by the project or
// tools.default_network alone, and the project's effective network (the
// lead's base.AllowNet) is its ceiling: authority only narrows down the tree.
// modeFn supplies the lead's approval mode.
func runtimeScope(base *tools.Scope, caps protocol.AgentCapabilities, conf tools.Confinement, modeFn func() string) *tools.Scope {
	s := *base
	s.RestrictedChild = true
	s.RuntimeAgent = true
	s.ReadOnly = caps.Files != protocol.FilesReadWrite
	// Defence in depth: only a read-write shell with read-write files may
	// change the workspace (admission already clamps it).
	s.ShellReadOnly = caps.Shell != protocol.ShellReadWrite || caps.Files != protocol.FilesReadWrite
	// The lead's mode is read live through modeFn; a copied stale mode must
	// never make TrustedAutonomy true for an agent.
	s.ApprovalMode = ""
	s.AllowNet = caps.Network && base.AllowNet
	s.Confined = conf != tools.ConfNone && caps.Shell != protocol.ShellNone && caps.Shell != ""
	s.ApprovalModeFn = modeFn
	return &s
}

// confinedShellTool names the tools whose damage a confined shell bounds.
// exec.* sessions are not among them: restricted children are refused those
// tools, so they are never pre-approved here.
func confinedShellTool(name string) bool {
	switch name {
	case "shell.run", "verification.run":
		return true
	}
	switch tools.FromWire(name) {
	case "shell.run", "verification.run":
		return true
	}
	return false
}

func projectLocalActionTool(name string) bool {
	switch tools.FromWire(name) {
	case "file.write", "file.edit", "shell.run", "verification.run", "exec.start", "exec.write", "exec.stop":
		return true
	}
	return false
}

// gateDecision is the approval gate for one tool call. Lead chats and legacy
// children use their thread's mode. A runtime agent uses the lead's mode read
// at call time (the agent's internal thread has none; nil means strictest),
// and its confined shell needs no prompt because the confinement bounds it.
func (e *Engine) gateDecision(ctx context.Context, name string, risk tools.Risk, fromListener bool, threadMode string) (policy.Decision, string) {
	scope := tools.ScopeFrom(ctx)
	if scope == nil || !scope.RuntimeAgent {
		if scope != nil && scope.ProjectID != "" && scope.Root != "" && !fromListener && projectLocalActionTool(name) &&
			(strings.HasPrefix(tools.FromWire(name), "file.") || scope.Confined) {
			return policy.Allow, "project-local action requested in this chat"
		}
		return e.gate.Check(name, risk, fromListener, normalizedApprovalMode(threadMode))
	}
	// The listener-channel rule outranks the confinement: a red action
	// triggered by an inbound message always asks, as it does for the lead.
	if risk == tools.RiskRed && fromListener {
		return policy.Ask, "red action triggered by an inbound message"
	}
	if scope.Confined && confinedShellTool(name) {
		return policy.Allow, "confined agent shell"
	}
	mode := policy.ApprovalNormal
	if scope.ApprovalModeFn != nil {
		mode = normalizedApprovalMode(scope.ApprovalModeFn())
	}
	return e.gate.Check(name, risk, fromListener, mode)
}
