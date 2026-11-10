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
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
)

// AgentAuthorize resolves the admitted delegation task of the calling turn.
type AgentAuthorize func(context.Context) (protocol.AgentTask, error)

type agentCallKey struct{}

// WithAgentCallID carries the provider's stable call identity, never model args.
func WithAgentCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentCallKey{}, id)
}

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

// RuntimeIntegrateArgs are the arguments of agent.integrate.
type RuntimeIntegrateArgs struct {
	Action string `json:"action"`
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}

// RuntimeIntegrateCall runs one validated agent.integrate action for the
// lead's admitted task and returns the result to marshal.
type RuntimeIntegrateCall func(ctx context.Context, task protocol.AgentTask, args RuntimeIntegrateArgs) (any, error)

type runtimeIntegrateTool struct {
	authorize AgentAuthorize
	caller    func(context.Context) (string, bool)
	call      RuntimeIntegrateCall
}

// NewRuntimeIntegrateTool returns agent.integrate, the lead's integration of
// runtime agents' revisions. It is lead-only: any context that caller does
// not resolve as the lead ("", true) is refused before anything else, so no
// runtime agent can use it whatever its capabilities (and capabilities never
// list it). It also requires writable tools (tools.RequireWritable).
func NewRuntimeIntegrateTool(authorize AgentAuthorize, caller func(context.Context) (string, bool), call RuntimeIntegrateCall) Tool {
	return &runtimeIntegrateTool{authorize: authorize, caller: caller, call: call}
}

func (t *runtimeIntegrateTool) SelectionMetadata() (string, string) { return "core", "engine" }
func (t *runtimeIntegrateTool) Name() string                        { return "agent.integrate" }
func (t *runtimeIntegrateTool) Description() string {
	return "Lead only. Integrate agents' committed revisions into the project checkout, one at a time in submission order. " +
		"accept applies an agent's revision onto the integrated state (staged; HEAD never moves) and plans the checks to run with verification.run; " +
		"the next accept needs those checks passed. A conflict applies nothing and lists the options. " +
		"drop discards a revision with a reason; resolved records your own staged fix of a conflict as the new state; " +
		"status reports the integration; complete finishes the task once every agent stopped, every revision is applied or dropped and the final state's checks passed. " +
		"Agents can be spawned at any time, also after accepts, except while an accept is being applied: a new read-only agent sees the integrated state (use it to review the integrated result); a new writer works on the task base and its revision is integrated onto the current state like any other, so it cannot redo changes to lines already integrated: for a conflict, drop or resolve it, and redo that work in a new delegated task. " +
		"Applied revisions stay applied: they cannot be dropped, and resolved only records a conflict's fix. " +
		"amend fixes an integrated state whose checks fail or are blocked: fix the files in the project checkout, stage them, then agent.integrate {action:\"amend\"} with a reason " +
		"(HEAD must stay the task base, with nothing unstaged or untracked and no conflict stages); the staged state becomes the integrated state, no revision changes, and its checks are planned again. " +
		"If it still cannot pass its checks, report it to the user, who can cancel the task; the applied work stays staged in the project checkout for them."
}

func (t *runtimeIntegrateTool) Schema() json.RawMessage {
	return schema(`{"type":"object","additionalProperties":false,"required":["action"],"properties":{` +
		`"action":{"type":"string","enum":["accept","drop","resolved","amend","status","complete"]},` +
		`"agent":{"type":"string","description":"Agent ID; required for accept, drop and resolved."},` +
		`"reason":{"type":"string","description":"Why the revision is dropped or the integrated state amended; required for drop and amend."}}}`)
}

func (t *runtimeIntegrateTool) Assess(args json.RawMessage) (Risk, string) {
	var a RuntimeIntegrateArgs
	_ = json.Unmarshal(args, &a)
	if a.Action == "accept" {
		return RiskYellow, "Apply agent " + a.Agent + "'s revision to the project checkout"
	}
	if a.Action == "amend" {
		return RiskYellow, "Record the staged project checkout as the integrated state"
	}
	return RiskGreen, "Agent integration: " + a.Action
}

func (t *runtimeIntegrateTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if t.authorize == nil || t.call == nil || t.caller == nil {
		return "", errors.New("agent runtime unavailable")
	}
	if id, ok := t.caller(ctx); !ok || id != "" {
		return "", errors.New("agent.integrate is lead-only: agents cannot integrate revisions, whatever their capabilities")
	}
	if err := RequireWritable(ctx); err != nil {
		return "", fmt.Errorf("agent.integrate needs writable tools (not available in read-only or plan mode): %w", err)
	}
	task, err := t.authorize(ctx)
	if err != nil {
		return "", err
	}
	if task.ID == "" {
		return "", errors.New("admitted task required")
	}
	var a RuntimeIntegrateArgs
	if err = decodeStrict(args, &a); err != nil {
		return "", err
	}
	a.Agent, a.Reason = strings.TrimSpace(a.Agent), strings.TrimSpace(a.Reason)
	switch a.Action {
	case "accept", "resolved":
		if a.Agent == "" {
			return "", fmt.Errorf("agent required for %s", a.Action)
		}
	case "drop":
		if a.Agent == "" {
			return "", errors.New("agent required for drop")
		}
		if a.Reason == "" {
			return "", errors.New("reason required for drop")
		}
	case "amend":
		if a.Reason == "" {
			return "", errors.New("reason required for amend")
		}
	case "status", "complete":
	default:
		return "", fmt.Errorf("unknown action %q; use accept, drop, resolved, amend, status or complete", a.Action)
	}
	result, err := t.call(ctx, task, a)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(result)
	return string(out), err
}

// RuntimeActivation holds the engine hooks of the lead's activation tools.
// Suggest records an agent.suggest card and returns the result to show the
// model. Caller resolves the calling agent like NewRuntimeAgentTools: only
// ("", true), the lead, may use these tools.
type RuntimeActivation struct {
	Suggest func(ctx context.Context, reason string) (string, error)
	// ProposePlan stores a validated agent.propose_plan proposal, shows the
	// user its plan card and blocks until the user decides; it returns the
	// result to show the model.
	ProposePlan func(ctx context.Context, p PlanProposal) (string, error)
	Caller      func(context.Context) (string, bool)
}

// maxSuggestReason bounds agent.suggest's reason, in characters.
const maxSuggestReason = 280

// NewRuntimeActivationTools returns the lead-only tools that ask the user to
// activate agents. They carry no authority: a suggestion only records a
// request, and delegation is still enabled by the app (delegation/enable or
// agent/suggestion/respond) before a new turn starts. They are never granted
// through capabilities (agentrt.AllowedTools).
func NewRuntimeActivationTools(a RuntimeActivation) []Tool {
	return []Tool{&agentSuggestTool{a: a}, &agentProposePlanTool{a: a}}
}

type agentSuggestTool struct{ a RuntimeActivation }

func (t *agentSuggestTool) SelectionMetadata() (string, string) { return "core", "engine" }
func (t *agentSuggestTool) Name() string                        { return "agent.suggest" }
func (t *agentSuggestTool) Description() string {
	return "Deprecated compatibility tool. New agent work uses agent.propose_plan directly from the user's prompt."
}

func (t *agentSuggestTool) Schema() json.RawMessage {
	return schema(`{"type":"object","additionalProperties":false,"required":["reason"],"properties":{` +
		`"reason":{"type":"string","minLength":1,"maxLength":280,"description":"Why agents would help, shown to the user."}}}`)
}

func (t *agentSuggestTool) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Suggest enabling agents"
}

func (t *agentSuggestTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if t.a.Caller == nil {
		return "", errors.New("agent.suggest is lead-only: the caller could not be resolved")
	}
	if id, ok := t.a.Caller(ctx); !ok || id != "" {
		return "", errors.New("agent.suggest is lead-only: agents and child tasks cannot suggest enabling agents")
	}
	if t.a.Suggest == nil {
		return "", errors.New("agent runtime unavailable")
	}
	var a struct {
		Reason *string `json:"reason"`
	}
	if err := decodeStrict(args, &a); err != nil {
		return "", err
	}
	if a.Reason == nil {
		return "", errors.New("reason required")
	}
	reason := strings.TrimSpace(*a.Reason)
	if reason == "" {
		return "", errors.New("reason required")
	}
	if n := utf8.RuneCountInString(reason); n > maxSuggestReason {
		return "", fmt.Errorf("reason is %d characters; at most %d", n, maxSuggestReason)
	}
	return t.a.Suggest(ctx, reason)
}

// PlanProposal is a validated agent.propose_plan request. Limits left zero
// take protocol.DefaultPlanLimits. Capability strings are passed through as
// given: the engine's capability clamp turns an unknown one into none.
type PlanProposal struct {
	Agents []protocol.PlanAgent
	Limits protocol.PlanLimits
}

// Plan proposal ceilings (agent.propose_plan).
const (
	MaxPlanAgents        = 12
	MaxPlanAgentName     = 40
	MaxPlanConcurrent    = 8
	MaxPlanDepth         = 3
	MaxPlanTotal         = 40
	MaxPlanSliceShare    = 0.5
	maxPlanPurposeRunes  = 500
	maxPlanSummaryRunes  = 2000
	maxPlanModelRefRunes = 200
)

type agentProposePlanTool struct{ a RuntimeActivation }

func (t *agentProposePlanTool) SelectionMetadata() (string, string) { return "core", "engine" }
func (t *agentProposePlanTool) Name() string                        { return "agent.propose_plan" }
func (t *agentProposePlanTool) Description() string {
	return "Lead only. When the user asks for agents, create an agent plan directly from their prompt. Each agent has a name, purpose, short summary, capabilities (files, shell, network, spawnMax) and optionally a model from the offered model pool. " +
		"In an ordinary project turn, a valid plan starts automatically in a private task workspace. The tool ends this turn and an admitted continuation receives agent tools. " +
		"The plan and granted capabilities are visible in the chat."
}

func (t *agentProposePlanTool) Schema() json.RawMessage {
	return schema(`{"type":"object","additionalProperties":false,"required":["agents"],"properties":{` +
		`"agents":{"type":"array","minItems":1,"maxItems":12,"items":{"type":"object","additionalProperties":false,"required":["name","purpose","capabilities"],"properties":{` +
		`"name":{"type":"string","minLength":1,"maxLength":40,"description":"Unique name of the agent in this plan."},` +
		`"purpose":{"type":"string","minLength":1},` +
		`"summary":{"type":"string","description":"One line shown on the plan card."},` +
		`"capabilities":{"type":"object","additionalProperties":false,"required":["files","shell","network","spawnMax"],"properties":{"files":{"type":"string","enum":["none","read","read-write"]},"shell":{"type":"string","enum":["none","read-only","read-write"]},"network":{"type":"boolean"},"spawnMax":{"type":"integer","minimum":0}}},` +
		`"model":{"type":"string","description":"A model of this task's pool (provider/model or its ID)."}}}},` +
		`"limits":{"type":"object","additionalProperties":false,"properties":{"maxConcurrent":{"type":"integer","minimum":0,"maximum":8},"maxDepth":{"type":"integer","minimum":0,"maximum":3},"maxTotal":{"type":"integer","minimum":0,"maximum":40},"sliceShare":{"type":"number","minimum":0,"maximum":0.5},"network":{"type":"boolean"}}}}}`)
}

func (t *agentProposePlanTool) Assess(json.RawMessage) (Risk, string) {
	// The plan card itself is the user's approval.
	return RiskGreen, "Propose agents for this task"
}

type planAgentArgs struct {
	Name         *string `json:"name"`
	Purpose      *string `json:"purpose"`
	Summary      string  `json:"summary"`
	Capabilities *struct {
		Files    *string `json:"files"`
		Shell    *string `json:"shell"`
		Network  *bool   `json:"network"`
		SpawnMax *int    `json:"spawnMax"`
	} `json:"capabilities"`
	Model string `json:"model"`
}

type planArgs struct {
	Agents []planAgentArgs `json:"agents"`
	Limits *struct {
		MaxConcurrent int     `json:"maxConcurrent"`
		MaxDepth      int     `json:"maxDepth"`
		MaxTotal      int     `json:"maxTotal"`
		SliceShare    float64 `json:"sliceShare"`
		Network       bool    `json:"network"`
	} `json:"limits"`
}

// ParsePlanProposal strictly decodes and validates agent.propose_plan
// arguments: 1–12 agents with unique (case-insensitive) names of 1–40
// characters, a purpose, all four capability fields, and limits within their
// ceilings (maxConcurrent ≤ 8, maxDepth ≤ 3, maxTotal ≤ 40, sliceShare in
// (0, 0.5]; zero means the default). Unknown fields are refused.
func ParsePlanProposal(args json.RawMessage) (PlanProposal, error) {
	var a planArgs
	if err := decodeStrict(args, &a); err != nil {
		return PlanProposal{}, err
	}
	if len(a.Agents) < 1 || len(a.Agents) > MaxPlanAgents {
		return PlanProposal{}, fmt.Errorf("a plan has 1 to %d agents, got %d", MaxPlanAgents, len(a.Agents))
	}
	var p PlanProposal
	seen := map[string]bool{}
	for i, ag := range a.Agents {
		field := fmt.Sprintf("agents[%d]", i)
		if ag.Name == nil || strings.TrimSpace(*ag.Name) == "" {
			return PlanProposal{}, fmt.Errorf("%s.name required", field)
		}
		name := strings.TrimSpace(*ag.Name)
		if n := utf8.RuneCountInString(name); n > MaxPlanAgentName {
			return PlanProposal{}, fmt.Errorf("%s.name %q is %d characters; at most %d", field, name, n, MaxPlanAgentName)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return PlanProposal{}, fmt.Errorf("%s.name %q: agent names must be unique (case-insensitive)", field, name)
		}
		seen[key] = true
		if ag.Purpose == nil || strings.TrimSpace(*ag.Purpose) == "" {
			return PlanProposal{}, fmt.Errorf("%s.purpose required", field)
		}
		purpose, summary, model := strings.TrimSpace(*ag.Purpose), strings.TrimSpace(ag.Summary), strings.TrimSpace(ag.Model)
		if utf8.RuneCountInString(purpose) > maxPlanPurposeRunes {
			return PlanProposal{}, fmt.Errorf("%s.purpose is longer than %d characters", field, maxPlanPurposeRunes)
		}
		if utf8.RuneCountInString(summary) > maxPlanSummaryRunes {
			return PlanProposal{}, fmt.Errorf("%s.summary is longer than %d characters", field, maxPlanSummaryRunes)
		}
		if utf8.RuneCountInString(model) > maxPlanModelRefRunes {
			return PlanProposal{}, fmt.Errorf("%s.model is longer than %d characters", field, maxPlanModelRefRunes)
		}
		c := ag.Capabilities
		switch {
		case c == nil:
			return PlanProposal{}, fmt.Errorf("%s.capabilities required (files, shell, network, spawnMax)", field)
		case c.Files == nil:
			return PlanProposal{}, fmt.Errorf("%s.capabilities.files required", field)
		case c.Shell == nil:
			return PlanProposal{}, fmt.Errorf("%s.capabilities.shell required", field)
		case c.Network == nil:
			return PlanProposal{}, fmt.Errorf("%s.capabilities.network required", field)
		case c.SpawnMax == nil:
			return PlanProposal{}, fmt.Errorf("%s.capabilities.spawnMax required", field)
		case *c.SpawnMax < 0:
			return PlanProposal{}, fmt.Errorf("%s.capabilities.spawnMax must be >= 0", field)
		}
		p.Agents = append(p.Agents, protocol.PlanAgent{
			Name: name, Purpose: purpose, Summary: summary, Model: model,
			Capabilities: protocol.AgentCapabilities{Files: protocol.FileAccess(*c.Files), Shell: protocol.ShellAccess(*c.Shell), Network: *c.Network, SpawnMax: *c.SpawnMax},
		})
	}
	if l := a.Limits; l != nil {
		switch {
		case l.MaxConcurrent < 0 || l.MaxConcurrent > MaxPlanConcurrent:
			return PlanProposal{}, fmt.Errorf("limits.maxConcurrent must be between 1 and %d (0 for the default)", MaxPlanConcurrent)
		case l.MaxDepth < 0 || l.MaxDepth > MaxPlanDepth:
			return PlanProposal{}, fmt.Errorf("limits.maxDepth must be between 1 and %d (0 for the default)", MaxPlanDepth)
		case l.MaxTotal < 0 || l.MaxTotal > MaxPlanTotal:
			return PlanProposal{}, fmt.Errorf("limits.maxTotal must be between 1 and %d (0 for the default)", MaxPlanTotal)
		case math.IsNaN(l.SliceShare) || l.SliceShare < 0 || l.SliceShare > MaxPlanSliceShare:
			return PlanProposal{}, fmt.Errorf("limits.sliceShare must be in (0, %g] (0 for the default)", MaxPlanSliceShare)
		}
		p.Limits = protocol.PlanLimits{MaxConcurrent: l.MaxConcurrent, MaxDepth: l.MaxDepth, MaxTotal: l.MaxTotal, SliceShare: l.SliceShare, Network: l.Network}
	}
	p.Limits = p.Limits.Normalized()
	return p, nil
}

func (t *agentProposePlanTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if t.a.Caller == nil {
		return "", errors.New("agent.propose_plan is lead-only: the caller could not be resolved")
	}
	if id, ok := t.a.Caller(ctx); !ok || id != "" {
		return "", errors.New("agent.propose_plan is lead-only: agents and child tasks cannot propose plans, whatever their capabilities")
	}
	if err := RequireWritable(ctx); err != nil {
		return "", fmt.Errorf("agent.propose_plan needs writable tools (not available in read-only or plan mode): %w", err)
	}
	if t.a.ProposePlan == nil {
		return "", errors.New("agent runtime unavailable")
	}
	p, err := ParsePlanProposal(args)
	if err != nil {
		return "", err
	}
	return t.a.ProposePlan(ctx, p)
}
