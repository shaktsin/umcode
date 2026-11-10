package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// Outcomes of a plan card decision. A declined or revised card resolves its
// approval as denied; revise carries the user's feedback back to the lead.
const (
	planApproved = "approved"
	planDeclined = "declined"
	planRevise   = "revise"
)

// planDeclinedMessage is what agent.propose_plan tells the lead on decline.
const planDeclinedMessage = "The user declined agents. Continue without them."

// errPlanSuperseded cancels the wait of a plan card that a newer proposal of
// the same task replaced.
var errPlanSuperseded = errors.New("plan was superseded by a newer proposal")

// agentPlanWait is the decision wait of one plan card. outcome and feedback
// are set (under agentPlanMu) by decideAgentPlan before the approval is
// resolved; an approval resolved directly (approval/respond) has no outcome:
// approved stays approved, denied means declined.
type agentPlanWait struct {
	approvalID string
	taskID     string
	outcome    string
	feedback   string
	cancel     context.CancelCauseFunc
}

// proposeAgentPlan is agent.propose_plan: validate and normalise the lead's
// proposal, fill empty models (previous approved assignment of the same name,
// else the chat's model), clamp every entry's capabilities exactly as
// admission will, save it as the task's proposed plan (superseding any
// earlier proposal and its card), then show the plan card as an agent_plan
// approval and block until the user decides. The stored entry capabilities
// are the clamped grant, so approving never grants more than the card shows.
func (e *Engine) proposeAgentPlan(ctx context.Context, p tools.PlanProposal) (string, error) {
	if id, ok := runtimeCaller(ctx); !ok || id != "" {
		return "", errors.New("agent.propose_plan is lead-only: agents and child tasks cannot propose plans, whatever their capabilities")
	}
	if e.AgentRuntime == nil {
		return "", errors.New("agent.propose_plan refused: it needs the agent runtime (agents.runtime_v2)")
	}
	if err := tools.RequireWritable(ctx); err != nil {
		return "", fmt.Errorf("agent.propose_plan needs writable tools (not available in read-only or plan mode): %w", err)
	}
	task, err := e.authorizeDelegationTool(ctx)
	if err != nil {
		return "", fmt.Errorf("agent.propose_plan needs a turn admitted for delegation: %w", err)
	}
	if !unfinishedDelegation(task.State) {
		return "", fmt.Errorf("agent.propose_plan refused: delegation task %s is no longer active", task.ID)
	}
	turn, ok := ctx.Value(executionTurnKey{}).(protocol.Turn)
	if !ok || turn.ID == "" || turn.ThreadID != task.ThreadID {
		return "", errors.New("agent.propose_plan refused: it needs a running turn of the task's chat")
	}
	if len(p.Agents) < 1 || len(p.Agents) > tools.MaxPlanAgents {
		return "", fmt.Errorf("a plan has 1 to %d agents, got %d", tools.MaxPlanAgents, len(p.Agents))
	}
	p.Limits = p.Limits.Normalized()
	if p.Limits.Network {
		scope, err := e.projectShellScope(ctx, task.ProjectID)
		if err != nil {
			return "", err
		}
		if !scope.AllowNet {
			return "", errors.New("limits.network refused: " + noNetworkReason + ", so no agent can have network")
		}
	}

	pool, modelScope, err := e.planModelPool(ctx, task.ID)
	if err != nil {
		return "", err
	}
	prev, err := e.Store.LatestApprovedAgentPlan(ctx, task.ThreadID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	defaultModel := ""
	if th, err := e.Store.GetThread(ctx, task.ThreadID); err == nil && th.Settings.Model != "" {
		defaultModel = poolModelRef(pool, th.Settings.Provider+"/"+th.Settings.Model, th.Settings.Model)
	}
	if defaultModel == "" && modelScope.Model != "" {
		defaultModel = poolModelRef(pool, modelScope.Provider+"/"+modelScope.Model)
	}
	opts := e.runtimeResolveOptions(ctx, task.ID)
	lead := e.AgentRuntime.LeadCapabilities()
	agents := make([]protocol.PlanAgent, 0, len(p.Agents))
	for _, a := range p.Agents {
		if a.Model != "" {
			ref := poolModelRef(pool, a.Model)
			if ref == "" {
				return "", fmt.Errorf("agent.propose_plan refused: agent %q: model %q is not in this task's approved model pool (offered: %s)", a.Name, a.Model, poolNames(pool))
			}
			a.Model = ref
		} else {
			for _, pa := range prev.Agents {
				if strings.EqualFold(pa.Name, a.Name) && pa.Model != "" {
					a.Model = poolModelRef(pool, pa.Model)
					break
				}
			}
			if a.Model == "" {
				a.Model = defaultModel
			}
		}
		requested := a.Capabilities
		granted, _ := clampPlanCapabilities(lead, requested, p.Limits, opts)
		a.Capabilities, a.Requested = granted, nil
		if granted != requested {
			a.Requested = &requested
		}
		agents = append(agents, a)
	}
	plan := protocol.AgentPlan{ID: store.NewID("plan"), ThreadID: task.ThreadID, TaskID: task.ID, Agents: agents, Limits: p.Limits, State: protocol.PlanProposed, CreatedAt: time.Now().UTC()}
	var previous *protocol.AgentPlan
	if prev.ID != "" {
		previous = &prev
	}
	card := planCardFor(plan, pool, lead, opts, previous)
	payload, err := json.Marshal(card)
	if err != nil {
		return "", err
	}

	timeout := 10 * time.Minute
	if e.Cfg != nil && e.Cfg.Policy.ApprovalTimeoutMinutes > 0 {
		timeout = time.Duration(e.Cfg.Policy.ApprovalTimeoutMinutes) * time.Minute
	}
	now := time.Now().UTC()
	item, _ := ctx.Value(executionItemKey{}).(protocol.Item)
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	approval := protocol.Approval{
		ID: store.NewID("apr"), Kind: "agent_plan", NodeID: plan.ID, ThreadID: task.ThreadID, ProjectID: task.ProjectID,
		TurnID: turn.ID, ItemID: item.ID, Tool: "agent.propose_plan", Args: payload, Risk: "workflow",
		Reason:        "Model-proposed agent plan",
		ActionSummary: fmt.Sprintf("Approve %d agent(s) for this task: %s", len(agents), strings.Join(names, ", ")),
		Status:        "pending", CreatedAt: now, ExpiresAt: now.Add(timeout),
	}
	sctx := context.WithoutCancel(ctx)
	waitCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	wait := &agentPlanWait{approvalID: approval.ID, taskID: task.ID, cancel: cancel}
	if err = e.saveProposedPlan(sctx, plan, wait); err != nil {
		return "", err
	}
	defer e.forgetPlanWait(plan.ID, wait)

	allowed, err := e.persistAndWaitApproval(waitCtx, sctx, approval, timeout, "agent.plan.request")
	if errors.Is(context.Cause(waitCtx), errPlanSuperseded) && !allowed {
		return "", fmt.Errorf("agent.propose_plan: %w; answer the newer plan card", errPlanSuperseded)
	}
	if errors.Is(err, ErrApprovalExpired) {
		return "", errors.New("agent.propose_plan: the plan card expired without a decision; the plan stays unapproved and no agent can be spawned")
	}
	if err != nil {
		return "", err
	}
	if !allowed {
		e.agentPlanMu.Lock()
		outcome, feedback := wait.outcome, wait.feedback
		e.agentPlanMu.Unlock()
		if outcome == planRevise {
			return marshalString(struct {
				State    string `json:"state"`
				Feedback string `json:"feedback"`
			}{planRevise, feedback})
		}
		return marshalString(struct {
			State   string `json:"state"`
			Message string `json:"message"`
		}{planDeclined, planDeclinedMessage})
	}
	// Approved: bind the plan to the task (idempotent if the decision
	// already did). A plan superseded meanwhile is never approved.
	if err = e.Store.ApproveAgentPlan(sctx, plan.ID, task.ID); err != nil {
		if errors.Is(err, store.ErrAgentTransition) {
			return "", fmt.Errorf("agent.propose_plan: %w; answer the newer plan card", errPlanSuperseded)
		}
		return "", err
	}
	stored, err := e.Store.AgentPlan(sctx, plan.ID)
	if err != nil {
		return "", err
	}
	approved := planCardFor(stored, pool, lead, opts, nil)
	type agentOut struct {
		Name     string                         `json:"name"`
		Granted  protocol.AgentCapabilities     `json:"granted"`
		Rejected []protocol.CapabilityRejection `json:"rejected"`
		Model    string                         `json:"model"`
	}
	out := make([]agentOut, len(approved.Agents))
	for i, a := range approved.Agents {
		out[i] = agentOut{Name: a.Name, Granted: a.Granted, Rejected: a.Rejected, Model: a.Model}
	}
	return marshalString(struct {
		PlanID string     `json:"planId"`
		State  string     `json:"state"`
		Agents []agentOut `json:"agents"`
	}{stored.ID, planApproved, out})
}

func marshalString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// saveProposedPlan stores the plan as proposed, supersedes the task's earlier
// proposals and registers the card's wait, as one step under agentPlanMu: a
// superseded card's pending approval is expired and its wait cancelled, so
// no stale card can be approved later.
func (e *Engine) saveProposedPlan(ctx context.Context, plan protocol.AgentPlan, wait *agentPlanWait) error {
	e.agentPlanMu.Lock()
	defer e.agentPlanMu.Unlock()
	if err := e.Store.SaveAgentPlan(ctx, plan); err != nil {
		return err
	}
	superseded, err := e.Store.SupersedeProposedAgentPlans(ctx, plan.TaskID, plan.ID)
	if err != nil {
		return err
	}
	for _, id := range superseded {
		old := e.agentPlanWaits[id]
		if old == nil {
			continue
		}
		if err := e.Store.DecideApproval(ctx, old.approvalID, "expired", "superseded"); err == nil {
			if a, gerr := e.Store.GetApproval(ctx, old.approvalID); gerr == nil {
				e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: a})
			}
		}
		old.cancel(errPlanSuperseded)
	}
	if e.agentPlanWaits == nil {
		e.agentPlanWaits = map[string]*agentPlanWait{}
	}
	e.agentPlanWaits[plan.ID] = wait
	return nil
}

func (e *Engine) forgetPlanWait(planID string, wait *agentPlanWait) {
	e.agentPlanMu.Lock()
	if e.agentPlanWaits[planID] == wait {
		delete(e.agentPlanWaits, planID)
	}
	e.agentPlanMu.Unlock()
}

// decideAgentPlan resolves the card of a plan still blocking
// agent.propose_plan with approved, declined or revise (feedback goes back to
// the lead). It only answers the card's approval: the waiting tool binds an
// approved plan to its task. The plan-respond RPC builds on it.
func (e *Engine) decideAgentPlan(ctx context.Context, planID, outcome, feedback, by string) (protocol.Approval, error) {
	switch outcome {
	case planApproved, planDeclined, planRevise:
	default:
		return protocol.Approval{}, protocol.Errorf(protocol.CodeInvalidParams, "plan decision must be %q, %q or %q", planApproved, planDeclined, planRevise)
	}
	e.agentPlanMu.Lock()
	w := e.agentPlanWaits[planID]
	if w == nil {
		e.agentPlanMu.Unlock()
		return protocol.Approval{}, protocol.Errorf(protocol.CodeConflict, "plan %s has no pending plan card", planID)
	}
	if w.outcome != "" {
		e.agentPlanMu.Unlock()
		return protocol.Approval{}, protocol.Errorf(protocol.CodeConflict, "plan %s was already decided (%s)", planID, w.outcome)
	}
	w.outcome, w.feedback = outcome, strings.TrimSpace(feedback)
	approvalID := w.approvalID
	e.agentPlanMu.Unlock()
	a, err := e.RespondApproval(ctx, approvalID, outcome == planApproved, false, by)
	if err != nil {
		e.agentPlanMu.Lock()
		if w.outcome == outcome {
			w.outcome, w.feedback = "", ""
		}
		e.agentPlanMu.Unlock()
	}
	return a, err
}

// agentPlanCard rebuilds a stored plan's card from the plan, the task's model
// pool and the current capability clamp.
func (e *Engine) agentPlanCard(ctx context.Context, plan protocol.AgentPlan) (protocol.PlanCard, error) {
	if e.AgentRuntime == nil {
		return protocol.PlanCard{}, errors.New("agent runtime unavailable")
	}
	pool, _, err := e.planModelPool(ctx, plan.TaskID)
	if err != nil {
		return protocol.PlanCard{}, err
	}
	var previous *protocol.AgentPlan
	if prev, err := e.Store.LatestApprovedAgentPlan(ctx, plan.ThreadID); err == nil && prev.ID != plan.ID {
		previous = &prev
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return protocol.PlanCard{}, err
	}
	return planCardFor(plan, pool, e.AgentRuntime.LeadCapabilities(), e.runtimeResolveOptions(ctx, plan.TaskID), previous), nil
}

// planModelPool is the task's frozen approved model pool, enabled models
// only, with tool support and current availability (runtimeModelPool).
func (e *Engine) planModelPool(ctx context.Context, taskID string) ([]modelRoute, store.AgentModelScope, error) {
	scope, err := e.Store.AgentModelScope(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, scope, nil
	}
	if err != nil {
		return nil, scope, err
	}
	enabled := scope
	enabled.Candidates = nil
	for _, c := range scope.Candidates {
		if c.Enabled {
			enabled.Candidates = append(enabled.Candidates, c)
		}
	}
	return e.runtimeModelPool(ctx, enabled, agentNeeds{}), scope, nil
}

// poolModelRef returns the pool's reference (provider/model) for the first
// ref that names a pool model (provider/model, bare model or configured ID),
// or "" when none does.
func poolModelRef(pool []modelRoute, refs ...string) string {
	for _, ref := range refs {
		for _, m := range pool {
			if m.matches(ref) {
				return m.name()
			}
		}
	}
	return ""
}

func poolNames(pool []modelRoute) string {
	if len(pool) == 0 {
		return "none"
	}
	names := make([]string, len(pool))
	for i, m := range pool {
		names[i] = m.name()
	}
	return strings.Join(names, ", ")
}

// clampPlanCapabilities resolves a plan entry's request against the lead and
// the sandbox exactly as admission will (agentrt.Resolve with the entry as
// its own ceiling), plus the plan's network limit (agentrt.Admit).
func clampPlanCapabilities(lead, requested protocol.AgentCapabilities, limits protocol.PlanLimits, opts agentrt.ResolveOptions) (protocol.AgentCapabilities, []protocol.CapabilityRejection) {
	granted, rejected := agentrt.Resolve(lead, requested, requested, opts)
	if !limits.Network && granted.Network {
		granted.Network = false
	}
	if !limits.Network && requested.Network {
		dup := false
		for _, r := range rejected {
			dup = dup || r.Field == "capabilities.network"
		}
		if !dup {
			rejected = append(rejected, protocol.CapabilityRejection{Field: "capabilities.network", Reason: "network access is disabled by the plan"})
		}
	}
	return granted, rejected
}

// planCardFor builds the plan card of a stored plan: each entry's grant and
// rejections (recomputed from its request), the offered pool models and a
// notice for every rejection and every unavailable or tool-incompatible
// assigned model. It depends only on its inputs, so the respond RPC and a
// restarted client rebuild the same card.
func planCardFor(plan protocol.AgentPlan, pool []modelRoute, lead protocol.AgentCapabilities, opts agentrt.ResolveOptions, previous *protocol.AgentPlan) protocol.PlanCard {
	card := protocol.PlanCard{PlanID: plan.ID, Limits: plan.Limits, Agents: []protocol.PlanCardAgent{}, Models: []protocol.PlanCardModel{}, Notices: []string{}, Previous: []protocol.PreviousAssignment{}}
	for _, m := range pool {
		card.Models = append(card.Models, protocol.PlanCardModel{ID: m.ID, Name: m.name(), Provider: m.Provider, ToolsOK: m.Tools, Available: m.Unavailable == "", Why: m.Unavailable})
	}
	for _, a := range plan.Agents {
		requested := a.Capabilities
		if a.Requested != nil {
			requested = *a.Requested
		}
		granted, rejected := clampPlanCapabilities(lead, requested, plan.Limits, opts)
		// Never more than the stored grant.
		granted = agentrt.Intersect(granted, a.Capabilities)
		if rejected == nil {
			rejected = []protocol.CapabilityRejection{}
		}
		card.Agents = append(card.Agents, protocol.PlanCardAgent{PlanAgent: a, Granted: granted, Rejected: rejected})
		for _, r := range rejected {
			card.Notices = append(card.Notices, rejectionNotice(a.Name, r))
		}
		if a.Model == "" {
			continue
		}
		var model *modelRoute
		for i := range pool {
			if pool[i].matches(a.Model) {
				model = &pool[i]
				break
			}
		}
		switch {
		case model == nil:
			card.Notices = append(card.Notices, fmt.Sprintf("%s: model %s is not in this task's model pool; choose another model before approving", a.Name, a.Model))
		default:
			if !model.Tools && agentNeedsFor(granted).Tools {
				card.Notices = append(card.Notices, fmt.Sprintf("%s: model %s cannot use tools, which this agent needs; choose another model before approving", a.Name, model.name()))
			}
			if model.Unavailable != "" {
				card.Notices = append(card.Notices, fmt.Sprintf("%s: model %s is unavailable right now (%s); the agent falls back to the next compatible pool model", a.Name, model.name(), model.Unavailable))
			}
		}
	}
	if previous != nil {
		for _, a := range previous.Agents {
			if a.Model != "" {
				card.Previous = append(card.Previous, protocol.PreviousAssignment{PlanID: previous.ID, Name: a.Name, Model: a.Model})
			}
		}
	}
	return card
}

// rejectionNotice is the card's plain-words notice for one rejection.
func rejectionNotice(name string, r protocol.CapabilityRejection) string {
	if r.Field == "capabilities.shell" && r.Reason == "shell is not available: "+noHostSandboxReason {
		return name + ": shell was dropped because no sandbox (macOS seatbelt / Linux bubblewrap) is available on this machine"
	}
	return fmt.Sprintf("%s: %s not granted: %s", name, strings.TrimPrefix(r.Field, "capabilities."), r.Reason)
}
