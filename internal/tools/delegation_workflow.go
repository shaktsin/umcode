package tools

import (
	"context"
	"encoding/json"
)

// AgentWorkflowCall keeps task/child authority in the engine, never model args.
type AgentWorkflowCall func(context.Context, string, json.RawMessage) (string, error)
type agentWorkflowTool struct {
	action string
	call   AgentWorkflowCall
}

func NewAgentWorkflowTools(call AgentWorkflowCall) []Tool {
	var out []Tool
	for _, action := range []string{"integrate", "complete", "review_report"} {
		out = append(out, &agentWorkflowTool{action, call})
	}
	return out
}
func (t *agentWorkflowTool) Name() string                        { return "agent." + t.action }
func (t *agentWorkflowTool) SelectionMetadata() (string, string) { return "core", "engine" }
func (t *agentWorkflowTool) Description() string {
	switch t.action {
	case "integrate":
		return "Apply the independently approved implementation to the admitted parent checkout. Returns required lead verification checks. Normal write approval applies."
	case "complete":
		return "Complete the task only after all required checks actually passed in the integrated parent. Returns retained workspace cleanup warnings separately."
	default:
		return "Reviewer only: submit an explicit structured verdict for the exact bound revision. approved requires no unresolved findings; changes_requested requires findings. Summary text never constitutes approval."
	}
}
func (t *agentWorkflowTool) Schema() json.RawMessage {
	if t.action == "review_report" {
		return schema(`{"type":"object","additionalProperties":false,"required":["revision","verdict","findings"],"properties":{"revision":{"type":"string","minLength":1},"verdict":{"type":"string","enum":["approved","changes_requested"]},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["message","severity"],"properties":{"path":{"type":"string"},"line":{"type":"integer"},"message":{"type":"string"},"severity":{"type":"string"}}}}}}`)
	}
	return schema(`{"type":"object","additionalProperties":false,"properties":{}}`)
}
func (t *agentWorkflowTool) Assess(json.RawMessage) (Risk, string) {
	if t.action == "integrate" {
		return RiskYellow, "Apply reviewed delegated changes to the parent checkout"
	}
	return RiskGreen, "Record delegated " + t.action
}
func (t *agentWorkflowTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	return t.call(ctx, t.action, args)
}
