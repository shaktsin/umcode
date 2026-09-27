// Package policy decides whether a tool call runs, needs approval, or is denied.
package policy

import (
	"path"
	"strings"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/tools"
)

// Decision is the outcome of a policy check.
type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

// Approval-mode tiers a project can pick. These travel per-turn on
// tools.Scope.ApprovalMode (set from protocol.Project.ApprovalMode) rather
// than living only in the global config, so each project can choose its own
// tier.
const (
	// ApprovalNormal is the default: red risk always asks, unless the tool
	// matches policy.auto_approve_tools.
	ApprovalNormal = "normal"
	// ApprovalAutoWorkspace auto-approves red actions that stay inside the
	// project's sandboxed workspace (shell, file writes/edits, exec
	// sessions, browser verification). It never auto-approves Computer Use,
	// because that drives the real desktop outside the sandbox.
	ApprovalAutoWorkspace = "auto_workspace"
	// ApprovalAutoAll additionally auto-approves Computer Use. This is the
	// highest-trust tier and should be presented in the UI as such.
	ApprovalAutoAll = "auto_all"
)

// Gate applies the policy config.
type Gate struct {
	cfg config.PolicyConfig
}

// New returns a Gate.
func New(cfg config.PolicyConfig) *Gate { return &Gate{cfg: cfg} }

// isHostActionTool reports whether tool drives the real desktop (Computer
// Use) rather than acting inside the project's sandboxed workspace. These
// tools are excluded from ApprovalAutoWorkspace: they can click, type and
// submit forms in the user's actual applications, so only the highest-trust
// tier (ApprovalAutoAll) auto-approves them.
func isHostActionTool(tool string) bool {
	return strings.HasPrefix(tool, "computer.")
}

// Check decides for a tool call with an assessed risk.
//
//   - green: allowed
//   - yellow: allowed, or approval in strict mode
//   - red: needs approval, unless auto-approved (see below)
//
// Tools listed in policy.auto_approve_tools (glob patterns) skip approval,
// except red calls from listener channels (inbound email/Telegram), which
// always need an admin approval.
//
// approvalMode is the calling project's auto-approve tier (ApprovalNormal,
// ApprovalAutoWorkspace, or ApprovalAutoAll; "" behaves like
// ApprovalNormal). It never overrides the listener-channel rule above: an
// inbound message can never auto-approve a red action, no matter the tier.
func (g *Gate) Check(tool string, risk tools.Risk, fromListener bool, approvalMode string) (Decision, string) {
	if risk == tools.RiskRed && fromListener {
		return Ask, "red action triggered by an inbound message"
	}
	for _, pat := range g.cfg.AutoApproveTools {
		if ok, _ := path.Match(pat, tool); ok {
			return Allow, "auto-approved by policy.auto_approve_tools"
		}
	}
	switch risk {
	case tools.RiskGreen:
		return Allow, ""
	case tools.RiskYellow:
		if g.cfg.ConfirmationStrictness == "strict" {
			return Ask, "strict mode requires approval for yellow actions"
		}
		return Allow, ""
	default:
		switch approvalMode {
		case ApprovalAutoAll:
			return Allow, "auto-approved: project approval mode is auto_all"
		case ApprovalAutoWorkspace:
			if !isHostActionTool(tool) {
				return Allow, "auto-approved: project approval mode is auto_workspace"
			}
		}
		return Ask, "red action requires approval"
	}
}
