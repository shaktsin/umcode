package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
)

type runtimeAgentTool struct {
	action    string
	svc       agentrt.Service
	authorize AgentAuthorize
	caller    func(context.Context) (string, bool)
}

// NewRuntimeAgentTools returns the model-facing tools of the agent runtime.
// There is deliberately no role field and no followup tool.
//
// caller resolves the calling agent from ctx: ("", true) is the lead, (id,
// true) a runtime agent. When it reports !ok (or is nil) every tool fails
// closed without calling the service.
func NewRuntimeAgentTools(svc agentrt.Service, authorize AgentAuthorize, caller func(context.Context) (string, bool)) []Tool {
	actions := []string{"spawn", "list", "status", "wait", "message", "cancel"}
	out := make([]Tool, 0, len(actions))
	for _, a := range actions {
		out = append(out, &runtimeAgentTool{action: a, svc: svc, authorize: authorize, caller: caller})
	}
	return out
}

func (t *runtimeAgentTool) SelectionMetadata() (string, string) { return "core", "engine" }
func (t *runtimeAgentTool) Name() string                        { return "agent." + t.action }
func (t *runtimeAgentTool) Description() string {
	switch t.action {
	case "spawn":
		return "Start one agent from an entry of the approved plan, with an assignment and optional model, context mode and budget share. Omit capabilities entirely to inherit the approved plan entry's capabilities; supply all four fields (files, shell, network, spawnMax) only to narrow them. The result lists what was granted and what was rejected."
	case "list":
		return "List the agents of this task with their states."
	case "status":
		return "Read the durable state and result of one agent in this task."
	case "wait":
		return "Wait until any (default) or all of the listed agents reach a terminal state."
	case "message":
		return "Send a message to a live agent of this task."
	default:
		return "Cancel an agent and its descendants, preserving partial results."
	}
}

func (t *runtimeAgentTool) Schema() json.RawMessage {
	switch t.action {
	case "spawn":
		return schema(`{"type":"object","additionalProperties":false,"required":["planEntry","assignment"],"properties":{` +
			`"name":{"type":"string"},` +
			`"planEntry":{"type":"string","minLength":1,"description":"Name of an agent in the approved plan."},` +
			`"assignment":{"type":"object","additionalProperties":false,"required":["prompt"],"properties":{"prompt":{"type":"string","minLength":1},"acceptanceChecks":{"type":"array","items":{"type":"string"}},"contextRefs":{"type":"array","items":{"type":"string","description":"Opaque scoped context reference ID; does not grant filesystem authority."}}}},` +
			`"capabilities":{"type":"object","additionalProperties":false,"required":["files","shell","network","spawnMax"],"properties":{"files":{"type":"string","enum":["none","read","read-write"]},"shell":{"type":"string","enum":["none","read-only","read-write"]},"network":{"type":"boolean"},"spawnMax":{"type":"integer","minimum":0}}},` +
			`"model":{"type":"string"},` +
			`"context":{"type":"string","enum":["none","summary","full"]},` +
			`"budget":{"type":"object","additionalProperties":false,"properties":{"share":{"type":"number","minimum":0,"maximum":1}}}}}`)
	case "list":
		return schema(`{"type":"object","additionalProperties":false,"properties":{}}`)
	case "wait":
		return schema(`{"type":"object","additionalProperties":false,"required":["ids"],"properties":{"ids":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string","minLength":1}},"mode":{"type":"string","enum":["any","all"],"default":"any"}}}`)
	case "message":
		return schema(`{"type":"object","additionalProperties":false,"required":["to","message"],"properties":{"to":{"type":"string","minLength":1},"message":{"type":"string","minLength":1}}}`)
	default:
		return schema(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1}}}`)
	}
}

func (t *runtimeAgentTool) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Manage runtime agents: " + t.action
}

func decodeStrict(args json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("one argument object required")
	}
	return nil
}

// admissionText turns a service refusal into text naming the field.
func admissionText(err error) error {
	var ae *agentrt.AdmissionError
	if errors.As(err, &ae) {
		return fmt.Errorf("spawn refused, field %q: %s", ae.Field, ae.Message)
	}
	return err
}

type spawnArgs struct {
	Name         string                   `json:"name"`
	PlanEntry    string                   `json:"planEntry"`
	Assignment   protocol.AgentAssignment `json:"assignment"`
	Capabilities *struct {
		Files    *string `json:"files"`
		Shell    *string `json:"shell"`
		Network  *bool   `json:"network"`
		SpawnMax *int    `json:"spawnMax"`
	} `json:"capabilities"`
	Model   string `json:"model"`
	Context string `json:"context"`
	Budget  *struct {
		Share float64 `json:"share"`
	} `json:"budget"`
}

func (t *runtimeAgentTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if t.authorize == nil || t.svc == nil {
		return "", errors.New("agent runtime unavailable")
	}
	task, err := t.authorize(ctx)
	if err != nil {
		return "", err
	}
	if task.ID == "" {
		return "", errors.New("admitted task required")
	}
	if t.caller == nil {
		return "", errors.New("agent runtime caller could not be resolved")
	}
	callerID, ok := t.caller(ctx)
	if !ok {
		return "", errors.New("agent runtime caller could not be resolved for this context")
	}
	var result any
	switch t.action {
	case "spawn":
		var a spawnArgs
		if err = decodeStrict(args, &a); err != nil {
			return "", err
		}
		callID, _ := ctx.Value(agentCallKey{}).(string)
		if callID == "" {
			return "", errors.New("stable tool call ID required")
		}
		if strings.TrimSpace(a.PlanEntry) == "" {
			return "", errors.New("planEntry required")
		}
		switch a.Context {
		case "", "none", "summary", "full":
		default:
			return "", fmt.Errorf("unknown context %q; use none, summary or full", a.Context)
		}
		req := agentrt.SpawnRequest{
			TaskID: task.ID, CallID: callID, CallerAgentID: callerID,
			Name: a.Name, PlanEntry: a.PlanEntry, Assignment: a.Assignment,
			Model: a.Model, ContextMode: a.Context,
		}
		if c := a.Capabilities; c != nil {
			switch {
			case c.Files == nil:
				return "", errors.New("capabilities.files required when capabilities is supplied")
			case c.Shell == nil:
				return "", errors.New("capabilities.shell required when capabilities is supplied")
			case c.Network == nil:
				return "", errors.New("capabilities.network required when capabilities is supplied")
			case c.SpawnMax == nil:
				return "", errors.New("capabilities.spawnMax required when capabilities is supplied")
			case *c.SpawnMax < 0:
				return "", errors.New("capabilities.spawnMax must be >= 0")
			}
			req.Capabilities = &protocol.AgentCapabilities{
				Files:    protocol.FileAccess(*c.Files),
				Shell:    protocol.ShellAccess(*c.Shell),
				Network:  *c.Network,
				SpawnMax: *c.SpawnMax,
			}
		}
		if a.Budget != nil {
			sh := a.Budget.Share
			if math.IsNaN(sh) || sh < 0 || sh > 1 {
				return "", errors.New("budget.share must be between 0 and 1")
			}
			req.BudgetShare = sh
		}
		res, err := t.svc.Spawn(ctx, req)
		if err != nil {
			return "", admissionText(err)
		}
		rejected := res.Rejected
		if rejected == nil {
			rejected = []protocol.CapabilityRejection{}
		}
		result = map[string]any{
			"id":           res.Agent.ID,
			"name":         res.Agent.Name,
			"state":        res.Agent.State,
			"queuedReason": res.QueuedReason,
			"granted":      res.Granted,
			"rejected":     rejected,
			"model":        map[string]string{"requested": res.Agent.ModelRequested, "actual": res.Agent.ModelActual},
			"budget":       map[string]float64{"share": res.Agent.BudgetShare},
		}
	case "list":
		var a struct{}
		if len(bytes.TrimSpace(args)) == 0 {
			args = json.RawMessage(`{}`)
		}
		if err = decodeStrict(args, &a); err != nil {
			return "", err
		}
		agents, err := t.svc.List(ctx, task.ID, callerID)
		if err != nil {
			return "", err
		}
		if agents == nil {
			agents = []protocol.RuntimeAgent{}
		}
		result = agents
	case "status", "cancel":
		var a struct {
			ID string `json:"id"`
		}
		if err = decodeStrict(args, &a); err != nil {
			return "", err
		}
		if strings.TrimSpace(a.ID) == "" {
			return "", errors.New("agent ID required")
		}
		if t.action == "status" {
			result, err = t.svc.Status(ctx, task.ID, callerID, a.ID)
			if err != nil {
				return "", err
			}
			break
		}
		if err = t.svc.Cancel(ctx, task.ID, callerID, a.ID); err != nil {
			return "", err
		}
		// Report the stored state: a terminal agent stays as it was.
		stored, err := t.svc.Status(ctx, task.ID, callerID, a.ID)
		if err != nil {
			return "", err
		}
		result = map[string]string{"id": a.ID, "state": string(stored.State)}
	case "wait":
		var a struct {
			IDs  []string `json:"ids"`
			Mode string   `json:"mode"`
		}
		if err = decodeStrict(args, &a); err != nil {
			return "", err
		}
		if len(a.IDs) == 0 || len(a.IDs) > 32 {
			return "", errors.New("provide 1 to 32 agent IDs")
		}
		mode := agentrt.WaitMode(a.Mode)
		switch mode {
		case "":
			mode = agentrt.WaitAny
		case agentrt.WaitAny, agentrt.WaitAll:
		default:
			return "", fmt.Errorf("unknown wait mode %q; use any or all", a.Mode)
		}
		agents, err := t.svc.Wait(ctx, task.ID, callerID, a.IDs, mode)
		if err != nil {
			return "", err
		}
		result = agents
	case "message":
		var a struct {
			To      string `json:"to"`
			Message string `json:"message"`
		}
		if err = decodeStrict(args, &a); err != nil {
			return "", err
		}
		if strings.TrimSpace(a.To) == "" || strings.TrimSpace(a.Message) == "" {
			return "", errors.New("to and message required")
		}
		// The provider call ID makes a retried message idempotent.
		callID, _ := ctx.Value(agentCallKey{}).(string)
		result, err = t.svc.Message(ctx, task.ID, callerID, a.To, a.Message, callID)
		if err != nil {
			return "", err
		}
	}
	out, err := json.Marshal(result)
	return string(out), err
}
