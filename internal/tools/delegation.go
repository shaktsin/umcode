package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/protocol"
)

type AgentAuthorize func(context.Context) (protocol.AgentTask, error)
type agentTool struct {
	action    string
	service   delegation.Service
	authorize AgentAuthorize
}
type agentCallKey struct{}

// WithAgentCallID carries the provider's stable call identity, never model args.
func WithAgentCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentCallKey{}, id)
}
func NewAgentTools(service delegation.Service, authorize AgentAuthorize) []Tool {
	out := make([]Tool, 0, 5)
	for _, action := range []string{"spawn", "status", "wait", "followup", "cancel"} {
		out = append(out, &agentTool{action: action, service: service, authorize: authorize})
	}
	return out
}
func (t *agentTool) Name() string { return "agent." + t.action }
func (t *agentTool) Description() string {
	switch t.action {
	case "spawn":
		return "Start one scoped worker for a bounded assignment. A busy error names the active worker; wait or cancel it before spawning another."
	case "status":
		return "Read the durable state and recoverable result of a child in this task."
	case "wait":
		return "Wait for any selected child to finish or cancellation; uses completion events without model polling."
	case "followup":
		return "Send a correction to the same child. Running workers receive it at the next safe boundary; at most two correction rounds."
	default:
		return "Cancel a child, preserving partial results and recoverable workspace edits."
	}
}
func (t *agentTool) Schema() json.RawMessage {
	switch t.action {
	case "spawn":
		return schema(`{"type":"object","additionalProperties":false,"required":["role","assignment"],"properties":{"role":{"type":"string","enum":["implementer","reviewer"]},"assignment":{"type":"object","additionalProperties":false,"required":["prompt"],"properties":{"prompt":{"type":"string","minLength":1},"acceptanceChecks":{"type":"array","items":{"type":"string"}},"contextRefs":{"type":"array","items":{"type":"string","description":"Opaque scoped context reference ID; does not grant filesystem authority."}}}}}}`)
	case "wait":
		return schema(`{"type":"object","additionalProperties":false,"required":["ids"],"properties":{"ids":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string","minLength":1}}}}`)
	case "followup":
		return schema(`{"type":"object","additionalProperties":false,"required":["id","message"],"properties":{"id":{"type":"string","minLength":1},"message":{"type":"string","minLength":1}}}`)
	default:
		return schema(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1}}}`)
	}
}
func (t *agentTool) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Manage scoped delegation: " + t.action
}
func (t *agentTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if t.authorize == nil || t.service == nil {
		return "", errors.New("delegation unavailable")
	}
	task, err := t.authorize(ctx)
	if err != nil {
		return "", err
	}
	if task.ID == "" {
		return "", errors.New("admitted delegation task required")
	}
	var request struct {
		ID         string                   `json:"id"`
		IDs        []string                 `json:"ids"`
		Role       protocol.AgentRole       `json:"role"`
		Assignment protocol.AgentAssignment `json:"assignment"`
		Message    string                   `json:"message"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return "", err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", errors.New("one argument object required")
	}
	callID, _ := ctx.Value(agentCallKey{}).(string)
	var result any
	switch t.action {
	case "spawn":
		if callID == "" {
			return "", errors.New("stable tool call ID required")
		}
		result, err = t.service.Spawn(ctx, task.ID, callID, request.Role, request.Assignment)
	case "wait":
		if len(request.IDs) == 0 || len(request.IDs) > 32 {
			return "", errors.New("provide 1 to 32 child IDs")
		}
		result, err = t.service.Wait(ctx, task.ID, request.IDs)
	default:
		if strings.TrimSpace(request.ID) == "" {
			return "", errors.New("child ID required")
		}
		switch t.action {
		case "status":
			result, err = t.service.Status(ctx, task.ID, request.ID)
		case "followup":
			if callID == "" {
				return "", errors.New("stable tool call ID required")
			}
			result, err = t.service.Followup(ctx, task.ID, request.ID, callID, request.Message)
		case "cancel":
			err = t.service.Cancel(ctx, task.ID, request.ID)
			result = map[string]string{"id": request.ID, "status": "cancelled"}
		}
	}
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(result)
	return string(out), err
}
