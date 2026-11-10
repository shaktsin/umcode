package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// Agent tool refusals the lead can act on: a short message for the model and
// the chat, and a NextStep naming the action instead of the generic
// execution-failure advice. Rule refusals are not retryable: repeating the
// same call cannot succeed until the rule is met.

const (
	// agentsNotEnabledMessage is the refusal of an agent.* call in a turn
	// that is not admitted for delegation. Agent authority comes only from a
	// plan the user approved (the plan card), so it names that rule rather
	// than an activation control.
	agentsNotEnabledMessage = "Agents are not enabled for this message. Agent tools work only after the user approves an agent plan."
	// agentsNotEnabledNextStep is that refusal's NextStep.
	agentsNotEnabledNextStep = "Continue without agents in this message; do not retry agent tools until the user has approved an agent plan."
)

// agentRuleError is an agent tool refusal under one of the delegation rules.
type agentRuleError struct{ msg, next string }

func (e *agentRuleError) Error() string { return e.msg }

var errAgentsNotEnabled error = &agentRuleError{msg: agentsNotEnabledMessage, next: agentsNotEnabledNextStep}

// agentToolName reports an agent.* tool of the lead or an agent (including
// agent.suggest), by registered or wire name.
func agentToolName(name string) bool {
	return strings.HasPrefix(tools.FromWire(name), "agent.")
}

// agentRuleFailure classifies an agent tool refusal under a delegation rule
// (not admitted, not a committed Git project, plan limits, lead-only, an
// agent.suggest rule). It reports false for anything else, including every
// non-agent tool, whose failures keep the generic classification. The
// not-enabled refusal is advisory: the lead continues without agents, so it
// does not block the turn's outcome.
func agentRuleFailure(tool, msg string) (protocol.ActionFailure, bool) {
	f := protocol.ActionFailure{Category: "policy_denied", Message: msg}
	if strings.Contains(msg, agentsNotEnabledMessage) {
		f.Message, f.NextStep, f.Advisory = agentsNotEnabledMessage, agentsNotEnabledNextStep, true
		return f, true
	}
	if !agentToolName(tool) {
		return f, false
	}
	text := strings.ToLower(msg)
	switch {
	case strings.Contains(text, "lead-only"):
		f.NextStep = "Only the lead can do this; report back to the lead instead of retrying."
	case strings.Contains(text, "committed git") || strings.Contains(text, "git repository root") || strings.Contains(text, "clean committed checkout"):
		f.NextStep = "Agents need a committed Git project: tell the user what this refusal says is missing, then agents can be enabled again."
	case strings.Contains(text, "a plan has 1 to"):
		f.NextStep = fmt.Sprintf("Propose a plan with 1 to %d agents.", tools.MaxPlanAgents)
	case strings.Contains(text, "agent.suggest refused:"):
		f.NextStep = "Follow the rule this refusal names; do not call agent.suggest again in this message."
	default:
		return f, false
	}
	return f, true
}

// classifyToolFailure is classifyActionFailure for a failure of a known tool:
// agent tool rule refusals get their own NextStep and are not retryable.
func classifyToolFailure(operationID, tool string, err error) protocol.ActionFailure {
	var typed *taskActionError
	if errors.As(err, &typed) {
		return classifyActionFailure(operationID, err)
	}
	if f, ok := agentRuleFailure(tool, err.Error()); ok {
		f.OperationID = operationID
		return f
	}
	return classifyActionFailure(operationID, err)
}
