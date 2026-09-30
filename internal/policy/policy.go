// Package policy decides whether a tool call runs, needs approval, or is denied.
package policy

import (
	"strings"

	"github.com/shaktsin/umcode/internal/tools"
)

// Decision is the outcome of a policy check.
type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

// Approval-mode tiers are selected per chat and travel on tools.Scope.
const (
	// ApprovalNormal is the default: red-risk actions always ask.
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

// Gate applies risk decisions selected by the chat.
type Gate struct{}

// New returns a Gate.
func New() *Gate { return &Gate{} }

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
//   - red: needs approval, unless the chat selected an auto-approve tier
//
// approvalMode is the calling chat's auto-approve tier (ApprovalNormal,
// ApprovalAutoWorkspace, or ApprovalAutoAll; "" behaves like
// ApprovalNormal). It never overrides the listener-channel rule above: an
// inbound message can never auto-approve a red action, no matter the tier.
func (g *Gate) Check(tool string, risk tools.Risk, fromListener bool, approvalMode string) (Decision, string) {
	if risk == tools.RiskRed && fromListener {
		return Ask, "red action triggered by an inbound message"
	}
	switch risk {
	case tools.RiskGreen:
		return Allow, ""
	case tools.RiskYellow:
		return Allow, ""
	default:
		switch approvalMode {
		case ApprovalAutoAll:
			return Allow, "auto-approved: chat approval mode is auto_all"
		case ApprovalAutoWorkspace:
			if !isHostActionTool(tool) {
				return Allow, "auto-approved: chat approval mode is auto_workspace"
			}
		}
		return Ask, "red action requires approval"
	}
}
