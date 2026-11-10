package engine

import (
	"errors"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// Agent tool refusals the lead can act on: a short message for the model and
// the chat, and a NextStep naming the action instead of the generic
// execution-failure advice. Rule refusals are typed (*tools.RuleRefusal) and
// recognised only by identity and only for agent.* tools: a failure whose
// output merely quotes a refusal keeps the generic classification.

const (
	// agentsNotEnabledMessage is the refusal of an agent.* call in a turn
	// that is not admitted for delegation. Agent authority comes only from a
	// plan the user approved (the plan card), so it names that rule rather
	// than an activation control.
	agentsNotEnabledMessage = "Agents are not enabled for this message. Agent tools work only after the user approves an agent plan."
	// agentsNotEnabledNextStep is that refusal's NextStep.
	agentsNotEnabledNextStep = "Continue without agents in this message; do not retry agent tools until the user has approved an agent plan."
	// agentGitNextStep is the NextStep of a refusal for a project that is
	// not a committed Git repository.
	agentGitNextStep = "Agents need a committed Git project: tell the user what this refusal says is missing, then agents can be enabled again."
	// suggestRuleNextStep is the NextStep of agent.suggest's own rules.
	suggestRuleNextStep = "Follow the rule this refusal names; do not call agent.suggest again in this message."
)

// errAgentsNotEnabled is advisory: the lead continues without agents, so it
// does not block the turn's outcome.
var errAgentsNotEnabled error = &tools.RuleRefusal{Msg: agentsNotEnabledMessage, NextStep: agentsNotEnabledNextStep, Advisory: true}

// errSuggestAdmitted is agent.suggest's refusal in a turn admitted for
// delegation.
var errSuggestAdmitted error = tools.Refuse(
	"agent.suggest refused: this turn is already admitted for delegation; agents are enabled, so do not suggest them; use agent.propose_plan",
	"Agents are already enabled for this message: call agent.propose_plan instead.")

// suggestRefusal is one of agent.suggest's rules.
func suggestRefusal(msg string) error { return tools.Refuse(msg, suggestRuleNextStep) }

// agentToolName reports an agent.* tool of the lead or an agent (including
// agent.suggest), by registered or wire name.
func agentToolName(name string) bool {
	return strings.HasPrefix(tools.FromWire(name), "agent.")
}

// agentRuleFailure classifies a typed rule refusal of an agent.* tool. It
// reports false for anything else: untyped errors whatever their wording,
// and every non-agent tool.
func agentRuleFailure(tool string, err error) (protocol.ActionFailure, bool) {
	var r *tools.RuleRefusal
	if err == nil || !agentToolName(tool) || !errors.As(err, &r) {
		return protocol.ActionFailure{}, false
	}
	return protocol.ActionFailure{Category: "policy_denied", Message: err.Error(), NextStep: r.NextStep, Advisory: r.Advisory}, true
}

// classifyToolFailure is classifyActionFailure for the error a known tool
// returned: agent tool rule refusals get their own NextStep and are not
// retryable.
func classifyToolFailure(operationID, tool string, err error) protocol.ActionFailure {
	if f, ok := agentRuleFailure(tool, err); ok {
		f.OperationID = operationID
		return f
	}
	return classifyActionFailure(operationID, err)
}
