package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
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
	if e.planCardTimeout > 0 {
		timeout = e.planCardTimeout
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

	// The proposal is announced once its card exists.
	allowed, err := e.persistAndWaitApprovalNotify(waitCtx, sctx, approval, timeout, "agent.plan.request", func() { e.publishAgentPlan(plan, "pending") })
	if errors.Is(context.Cause(waitCtx), errPlanSuperseded) && !allowed {
		return "", fmt.Errorf("agent.propose_plan: %w; answer the newer plan card", errPlanSuperseded)
	}
	if errors.Is(err, ErrApprovalExpired) {
		e.forgetPlanWait(plan.ID, wait)
		closed := e.closeEmptyRuntimeTask(sctx, task.ID)
		msg := "agent.propose_plan: the plan card expired without a decision; the plan stays unapproved and no agent can be spawned"
		if closed {
			msg += "; the delegation task had no agents and was closed, so continue without agents"
		}
		return "", errors.New(msg)
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
		// A declined card of a task with no approved plan and no agents ends
		// that empty task, so agents can be enabled again later in the chat.
		e.forgetPlanWait(plan.ID, wait)
		e.closeEmptyRuntimeTask(sctx, task.ID)
		return marshalString(struct {
			State   string `json:"state"`
			Message string `json:"message"`
		}{planDeclined, planDeclinedMessage})
	}
	// Approved: bind the plan to the task (idempotent if the decision
	// already did). A plan superseded meanwhile is never approved.
	if err = e.bindApprovedPlan(sctx, plan.ID, task.ID); err != nil {
		if errors.Is(err, errPlanSuperseded) {
			return "", fmt.Errorf("agent.propose_plan: %w; answer the newer plan card", errPlanSuperseded)
		}
		return "", fmt.Errorf("agent.propose_plan: %w", err)
	}
	stored, err := e.Store.AgentPlan(sctx, plan.ID)
	if err != nil {
		return "", err
	}
	e.publishAgentPlan(stored, "approved")
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

// closeEmptyRuntimeTask completes an active runtime task that has no
// approved plan, no agents, no revisions and no plan card still waiting for
// a decision: nothing was delegated, so the task only blocks enabling agents
// again in the chat. It reports whether the task was closed; a failure is
// logged and leaves the task as it was (the user can still cancel it).
func (e *Engine) closeEmptyRuntimeTask(ctx context.Context, taskID string) bool {
	e.delegationWorkflowMu.Lock()
	defer e.delegationWorkflowMu.Unlock()
	// Held across the completion: no new card can be registered meanwhile
	// (saveProposedPlan re-checks the task under this lock).
	e.agentPlanMu.Lock()
	defer e.agentPlanMu.Unlock()
	for _, w := range e.agentPlanWaits {
		if w.taskID == taskID {
			return false
		}
	}
	task, err := e.Store.AgentTask(ctx, taskID)
	if err != nil || !unfinishedDelegation(task.State) {
		return false
	}
	if _, err = e.Store.ApprovedAgentPlanForTask(ctx, taskID); !errors.Is(err, store.ErrNotFound) {
		return false
	}
	if agents, err := e.Store.RuntimeAgents(ctx, taskID); err != nil || len(agents) > 0 {
		return false
	}
	if revs, err := e.Store.AgentRevisions(ctx, taskID); err != nil || len(revs) > 0 {
		return false
	}
	if _, err = e.completeRuntimeTask(ctx, task); err != nil {
		e.Log.Warn("could not close the empty delegation task", "task", taskID, "err", err)
		return false
	}
	return true
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
	// An empty task may have been closed after its card was declined.
	if task, err := e.Store.AgentTask(ctx, plan.TaskID); err != nil {
		return err
	} else if !unfinishedDelegation(task.State) {
		return fmt.Errorf("agent.propose_plan refused: delegation task %s is no longer active (%s)", task.ID, task.State)
	}
	if err := e.Store.SaveAgentPlan(ctx, plan); err != nil {
		return err
	}
	superseded, err := e.Store.SupersedeProposedAgentPlans(ctx, plan.TaskID, plan.ID)
	if err != nil {
		return err
	}
	for _, id := range superseded {
		if old := e.agentPlanWaits[id]; old != nil {
			if err := e.Store.DecideApproval(ctx, old.approvalID, "expired", "superseded"); err == nil {
				if a, gerr := e.Store.GetApproval(ctx, old.approvalID); gerr == nil {
					e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: a})
				}
			}
			old.cancel(errPlanSuperseded)
		}
		// Announce the superseded plan with its card's real status.
		if sp, err := e.Store.AgentPlan(ctx, id); err == nil {
			status := ""
			if a, err := e.Store.LatestApprovalForNode(ctx, "agent_plan", id); err == nil {
				status = a.Status
			}
			e.publishAgentPlan(sp, status)
		}
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

// decideAgentPlan answers the card of a plan with approved, declined or
// revise (feedback goes back to the lead) and no edits, through the same path
// as agent/plan/respond, and returns the card's approval.
func (e *Engine) decideAgentPlan(ctx context.Context, planID, outcome, feedback, by string) (protocol.Approval, error) {
	decision := map[string]string{planApproved: protocol.PlanDecisionApprove, planDeclined: protocol.PlanDecisionDecline, planRevise: protocol.PlanDecisionRevise}[outcome]
	if decision == "" {
		return protocol.Approval{}, protocol.Errorf(protocol.CodeInvalidParams, "plan decision must be %q, %q or %q", planApproved, planDeclined, planRevise)
	}
	_, a, err := e.respondAgentPlan(ctx, protocol.AgentPlanRespondParams{PlanID: planID, Decision: decision, Feedback: feedback}, by)
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
	pool := e.runtimeModelPool(ctx, enabledModelScope(scope), agentNeeds{})
	if e.planModelPoolHook != nil {
		pool = e.planModelPoolHook(pool)
	}
	return pool, scope, nil
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

// planDecidedText and planExpiredText are the refusals for a card that can
// no longer be answered differently.
const (
	planDecidedText = "plan already decided"
	planExpiredText = "the plan card expired without a decision (the engine restarted or the card timed out); the plan stays unapproved and no agent can be spawned; ask the lead to propose it again"
)

// RespondAgentPlan is agent/plan/respond: the user's answer to a plan card.
// Approve validates every edit (dropped agents, per-agent models from the
// task's current pool, limits clamped to the ceilings) and re-runs the model
// and capability checks against the current pool and sandbox before it
// changes anything; only then is the edited plan saved, the card approved
// and the plan bound to its task. Any failure leaves the plan proposed and
// the card pending. Decline and revise resolve the card denied (revise
// carries the feedback to the lead). A stale card is refused ("plan was
// superseded", "plan already decided"); repeating the recorded decision
// returns the plan unchanged.
func (e *Engine) RespondAgentPlan(ctx context.Context, p protocol.AgentPlanRespondParams) (protocol.AgentPlanRespondResult, error) {
	return e.RespondAgentPlanAs(ctx, "app", p)
}

// RespondAgentPlanAs is RespondAgentPlan recording clientID (the answering
// connection) as the card's decided_by, as approval/respond does.
func (e *Engine) RespondAgentPlanAs(ctx context.Context, clientID string, p protocol.AgentPlanRespondParams) (protocol.AgentPlanRespondResult, error) {
	res, _, err := e.respondAgentPlan(ctx, p, clientID)
	return res, err
}

func (e *Engine) respondAgentPlan(ctx context.Context, p protocol.AgentPlanRespondParams, by string) (protocol.AgentPlanRespondResult, protocol.Approval, error) {
	var res protocol.AgentPlanRespondResult
	var none protocol.Approval
	if delegationChild(ctx) || childExecutionFrom(ctx) != nil {
		return res, none, protocol.Errorf(protocol.CodeInvalidRequest, "agents and child tasks cannot answer plan cards")
	}
	if p.PlanID == "" {
		return res, none, protocol.Errorf(protocol.CodeInvalidParams, "planId required")
	}
	outcome := map[string]string{protocol.PlanDecisionApprove: planApproved, protocol.PlanDecisionDecline: planDeclined, protocol.PlanDecisionRevise: planRevise}[p.Decision]
	if outcome == "" {
		return res, none, protocol.Errorf(protocol.CodeInvalidParams, "decision must be %q, %q or %q", protocol.PlanDecisionApprove, protocol.PlanDecisionDecline, protocol.PlanDecisionRevise)
	}
	edits := len(p.Models) > 0 || len(p.Drop) > 0 || p.Limits != nil
	if outcome != planApproved && edits {
		return res, none, protocol.Errorf(protocol.CodeInvalidParams, "models, drop and limits apply only to approve")
	}
	feedback := strings.TrimSpace(p.Feedback)
	if outcome == planRevise && feedback == "" {
		return res, none, protocol.Errorf(protocol.CodeInvalidParams, "revise needs feedback for the lead")
	}
	// Once validation passes, the decision runs to completion even if the
	// request is cancelled: a deadline must never leave a half state.
	sctx := context.WithoutCancel(ctx)

	e.agentPlanMu.Lock()
	defer e.agentPlanMu.Unlock()
	plan, err := e.Store.AgentPlan(ctx, p.PlanID)
	if errors.Is(err, store.ErrNotFound) {
		return res, none, protocol.Errorf(protocol.CodeNotFound, "agent plan %s not found", p.PlanID)
	}
	if err != nil {
		return res, none, err
	}
	w := e.agentPlanWaits[plan.ID]
	var approval protocol.Approval
	var aerr error
	if w != nil {
		approval, aerr = e.Store.GetApproval(ctx, w.approvalID)
	} else {
		approval, aerr = e.Store.LatestApprovalForNode(ctx, "agent_plan", plan.ID)
	}
	if aerr != nil && !errors.Is(aerr, store.ErrNotFound) {
		return res, none, aerr
	}
	done := func(pl protocol.AgentPlan, a protocol.Approval) (protocol.AgentPlanRespondResult, protocol.Approval, error) {
		return protocol.AgentPlanRespondResult{Plan: pl, ApprovalStatus: a.Status}, a, nil
	}
	rec, recorded := e.agentPlanDecided[plan.ID]
	// sameApprove reports whether a repeat approve carries the same edits as
	// the recorded one (without a record only an edit-free repeat is).
	sameApprove := func() error {
		if recorded && rec.outcome == planApproved {
			pool, _, err := e.planModelPool(ctx, plan.TaskID)
			if err != nil {
				return err
			}
			if _, err := foldedModelEdits(p.Models); err != nil {
				return err
			}
			if planEditsKey(pool, p) == rec.edits {
				return nil
			}
		} else if !edits {
			return nil
		} else if _, err := foldedModelEdits(p.Models); err != nil {
			return err
		}
		return protocol.Errorf(protocol.CodeConflict, "plan %s: %s with different edits; the approved plan is unchanged", plan.ID, planDecidedText)
	}

	switch plan.State {
	case protocol.PlanSuperseded:
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %v; answer the newer plan card", plan.ID, errPlanSuperseded)
	case protocol.PlanApproved:
		if outcome != planApproved {
			return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %s (approved)", plan.ID, planDecidedText)
		}
		if err := sameApprove(); err != nil {
			return res, none, err
		}
		return done(plan, approval)
	case protocol.PlanProposed:
	default:
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s is %s", plan.ID, plan.State)
	}
	if aerr != nil {
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s has no plan card to answer", plan.ID)
	}
	switch approval.Status {
	case "pending":
	case "approved":
		// The card is approved and the plan is being bound to its task.
		if outcome != planApproved {
			return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %s (approved)", plan.ID, planDecidedText)
		}
		if err := sameApprove(); err != nil {
			return res, none, err
		}
		if err := e.bindApprovedPlan(sctx, plan.ID, plan.TaskID); err != nil {
			return res, none, planRefusal(plan.ID, err)
		}
		if plan, err = e.Store.AgentPlan(sctx, plan.ID); err != nil {
			return res, none, err
		}
		return done(plan, approval)
	case "denied":
		// Declined and revised plans stay proposed; the card says denied.
		// Only the recorded decision may be repeated.
		prev := ""
		switch {
		case w != nil && w.outcome != "":
			prev = w.outcome
		case recorded:
			prev = rec.outcome
		}
		if outcome != planApproved && outcome == prev {
			return done(plan, approval)
		}
		if prev == "" {
			prev = "declined or revised"
		}
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %s (%s)", plan.ID, planDecidedText, prev)
	case "expired":
		if approval.DecidedBy == "superseded" {
			return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %v; answer the newer plan card", plan.ID, errPlanSuperseded)
		}
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %s", plan.ID, planExpiredText)
	default:
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %s (%s)", plan.ID, planDecidedText, approval.Status)
	}
	// Pending: the card must still be blocking agent.propose_plan.
	e.mu.Lock()
	_, waiting := e.approvals[approval.ID]
	e.mu.Unlock()
	if w == nil || w.approvalID != approval.ID || !waiting {
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: its card is not waiting for a decision; try again or ask the lead to propose it again", plan.ID)
	}
	if w.outcome != "" {
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s: %s (%s)", plan.ID, planDecidedText, w.outcome)
	}

	if outcome != planApproved {
		w.outcome, w.feedback = outcome, feedback
		decided, committed, err := e.respondApprovalCommitted(sctx, approval.ID, false, false, by)
		if err != nil {
			if !committed {
				w.outcome, w.feedback = "", ""
				return res, none, e.planCardRefusal(sctx, plan.ID, approval.ID, err)
			}
			decided = committedApproval(e, sctx, approval, "denied")
		}
		e.recordPlanDecision(plan.ID, planDecision{outcome: outcome})
		e.publishAgentPlan(plan, decided.Status)
		return done(plan, decided)
	}

	// Approve: validate everything first; nothing changes on a refusal.
	task, err := e.Store.AgentTask(ctx, plan.TaskID)
	if err != nil {
		return res, none, err
	}
	if !unfinishedDelegation(task.State) {
		return res, none, protocol.Errorf(protocol.CodeConflict, "plan %s cannot be approved: delegation task %s is no longer active", plan.ID, task.ID)
	}
	edited, err := e.validatePlanApproval(ctx, plan, task, p)
	if err != nil {
		return res, none, err
	}
	pool, _, err := e.planModelPool(ctx, task.ID)
	if err != nil {
		return res, none, err
	}
	key := planEditsKey(pool, p)
	before, _ := json.Marshal(plan)
	after, _ := json.Marshal(edited)
	changed := string(before) != string(after)
	if changed {
		if err := e.Store.SaveAgentPlan(sctx, edited); err != nil {
			return res, none, err
		}
	}
	w.outcome, w.feedback = planApproved, ""
	decided, committed, err := e.respondApprovalCommitted(sctx, approval.ID, true, false, by)
	if err != nil {
		// Restore only when the decision is known not to be stored: a
		// stored approval has woken the waiter, which binds the stored
		// (edited) plan, so the edited plan is what ends approved.
		if !committed {
			w.outcome = ""
			if changed {
				if rerr := e.Store.SaveAgentPlan(sctx, plan); rerr != nil {
					e.Log.Warn("restore plan after failed approval", "plan", plan.ID, "err", rerr)
				}
			}
			return res, none, e.planCardRefusal(sctx, plan.ID, approval.ID, err)
		}
		decided = committedApproval(e, sctx, approval, "approved")
	}
	e.recordPlanDecision(plan.ID, planDecision{outcome: planApproved, edits: key})
	if err := e.bindApprovedPlan(sctx, plan.ID, task.ID); err != nil {
		return res, none, planRefusal(plan.ID, err)
	}
	stored, err := e.Store.AgentPlan(sctx, plan.ID)
	if err != nil {
		return res, none, err
	}
	if changed {
		// The stored card shows what was approved, so a restarted client
		// rebuilds the approved shape.
		if card, err := e.agentPlanCard(sctx, stored); err == nil {
			if payload, err := json.Marshal(card); err == nil {
				if err := e.Store.UpdateApprovalArgs(sctx, approval.ID, payload); err != nil {
					e.Log.Warn("refresh plan card", "plan", plan.ID, "err", err)
				} else if a, err := e.Store.GetApproval(sctx, approval.ID); err == nil {
					decided = a
				}
			}
		}
	}
	e.publishAgentPlan(stored, decided.Status)
	return done(stored, decided)
}

// planDecision is the recorded answer to a plan card: the outcome and, for
// approve, the canonical edits (planEditsKey), so a repeat is recognised.
type planDecision struct {
	outcome string
	edits   string
}

// recordPlanDecision remembers a card's decision for the engine's lifetime
// (caller holds agentPlanMu).
func (e *Engine) recordPlanDecision(planID string, d planDecision) {
	if e.agentPlanDecided == nil {
		e.agentPlanDecided = map[string]planDecision{}
	}
	e.agentPlanDecided[planID] = d
}

// planEditsKey is a canonical form of an approve's edits: agent names
// case-folded, models resolved to their pool reference, drops sorted and
// limits clamped as validation does.
func planEditsKey(pool []modelRoute, p protocol.AgentPlanRespondParams) string {
	var k struct {
		Models map[string]string    `json:"m,omitempty"`
		Drop   []string             `json:"d,omitempty"`
		Limits *protocol.PlanLimits `json:"l,omitempty"`
	}
	folded, _ := foldedModelEdits(p.Models)
	for name, ref := range folded {
		if k.Models == nil {
			k.Models = map[string]string{}
		}
		m := poolModelRef(pool, ref)
		if m == "" {
			m = "?" + ref
		}
		k.Models[name] = m
	}
	for _, name := range p.Drop {
		n := strings.ToLower(strings.TrimSpace(name))
		if !slices.Contains(k.Drop, n) {
			k.Drop = append(k.Drop, n)
		}
	}
	slices.Sort(k.Drop)
	if p.Limits != nil {
		l, err := clampPlanLimits(*p.Limits)
		if err != nil {
			l = *p.Limits
		}
		k.Limits = &l
	}
	b, _ := json.Marshal(k)
	return string(b)
}

// clampPlanLimits refuses negative or non-finite limits, clamps them to the
// ceilings and fills zeros from the defaults.
func clampPlanLimits(l protocol.PlanLimits) (protocol.PlanLimits, error) {
	if l.MaxConcurrent < 0 || l.MaxDepth < 0 || l.MaxTotal < 0 || l.SliceShare < 0 || math.IsNaN(l.SliceShare) || math.IsInf(l.SliceShare, 0) {
		return l, protocol.Errorf(protocol.CodeInvalidParams, "plan limits cannot be negative or not a number")
	}
	l.MaxConcurrent = min(l.MaxConcurrent, tools.MaxPlanConcurrent)
	l.MaxDepth = min(l.MaxDepth, tools.MaxPlanDepth)
	l.MaxTotal = min(l.MaxTotal, tools.MaxPlanTotal)
	l.SliceShare = min(l.SliceShare, tools.MaxPlanSliceShare)
	return l.Normalized(), nil
}

// validatePlanApproval applies an approve's edits to a proposed plan and
// re-checks it against the current state: dropped agents (never all of
// them), per-agent models (each in the task's current pool), limits (clamped
// to the ceilings; network only if the project allows it), the capability
// clamp (from the stored grant, so it can only narrow) and each assigned
// model (in the pool, able to use tools when the agent needs them, and
// available or with an available fallback in the pool).
func (e *Engine) validatePlanApproval(ctx context.Context, plan protocol.AgentPlan, task protocol.AgentTask, p protocol.AgentPlanRespondParams) (protocol.AgentPlan, error) {
	if e.AgentRuntime == nil {
		return plan, errors.New("plan approval needs the agent runtime (agents.runtime_v2)")
	}
	agents := append([]protocol.PlanAgent(nil), plan.Agents...)
	if len(p.Drop) > 0 {
		drop := map[int]bool{}
		for _, name := range p.Drop {
			i := planEntryIndex(agents, name)
			if i < 0 {
				return plan, protocol.Errorf(protocol.CodeInvalidParams, "plan has no agent %q to drop", name)
			}
			drop[i] = true
		}
		if len(drop) == len(agents) {
			return plan, protocol.Errorf(protocol.CodeInvalidParams, "cannot drop every agent of the plan; decline it instead")
		}
		kept := agents[:0:0]
		for i, a := range agents {
			if !drop[i] {
				kept = append(kept, a)
			}
		}
		agents = kept
	}
	limits := plan.Limits
	if p.Limits != nil {
		l, err := clampPlanLimits(*p.Limits)
		if err != nil {
			return plan, err
		}
		limits = l
	}
	if limits.Network {
		scope, err := e.projectShellScope(ctx, task.ProjectID)
		if err != nil {
			return plan, err
		}
		if !scope.AllowNet {
			return plan, protocol.Errorf(protocol.CodeConflict, "limits.network refused: %s, so no agent can have network; turn limits.network off or decline", noNetworkReason)
		}
	}
	pool, _, err := e.planModelPool(ctx, task.ID)
	if err != nil {
		return plan, err
	}
	if _, err := foldedModelEdits(p.Models); err != nil {
		return plan, err
	}
	names := make([]string, 0, len(p.Models))
	for name := range p.Models {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		ref := p.Models[name]
		i := planEntryIndex(agents, name)
		if i < 0 {
			return plan, protocol.Errorf(protocol.CodeInvalidParams, "plan has no agent %q to assign a model to (dropped agents cannot get one)", name)
		}
		m := poolModelRef(pool, ref)
		if m == "" {
			return plan, protocol.Errorf(protocol.CodeInvalidParams, "agent %q: model %q is not in this task's approved model pool (offered: %s)", agents[i].Name, ref, poolNames(pool))
		}
		agents[i].Model = m
	}
	opts := e.runtimeResolveOptions(ctx, task.ID)
	lead := e.AgentRuntime.LeadCapabilities()
	for i := range agents {
		a := &agents[i]
		granted, _ := clampPlanCapabilities(lead, a.Capabilities, limits, opts)
		granted = agentrt.Intersect(granted, a.Capabilities)
		if granted != a.Capabilities {
			if a.Requested == nil {
				orig := a.Capabilities
				a.Requested = &orig
			}
			a.Capabilities = granted
		}
		if a.Model == "" {
			continue
		}
		var route *modelRoute
		for j := range pool {
			if pool[j].matches(a.Model) {
				route = &pool[j]
				break
			}
		}
		needs := agentNeedsFor(granted)
		switch {
		case route == nil:
			return plan, protocol.Errorf(protocol.CodeConflict, "agent %q: model %s is no longer in this task's approved model pool (offered: %s); choose another model", a.Name, a.Model, poolNames(pool))
		case needs.Tools && !route.Tools:
			return plan, protocol.Errorf(protocol.CodeConflict, "agent %q: model %s cannot use tools, which this agent needs; choose another model", a.Name, route.name())
		case route.Unavailable != "" && !poolCanRun(pool, needs):
			return plan, protocol.Errorf(protocol.CodeConflict, "agent %q: model %s is unavailable (%s) and no other model of the pool can run this agent now", a.Name, route.name(), route.Unavailable)
		}
	}
	plan.Agents, plan.Limits = agents, limits
	return plan, nil
}

// planEntryIndex finds a plan entry by name (case-insensitive), or -1.
func planEntryIndex(agents []protocol.PlanAgent, name string) int {
	name = strings.TrimSpace(name)
	for i, a := range agents {
		if strings.EqualFold(a.Name, name) {
			return i
		}
	}
	return -1
}

// poolCanRun reports whether some available pool model can run an agent with
// these needs (the router's fallback target).
func poolCanRun(pool []modelRoute, needs agentNeeds) bool {
	for _, m := range pool {
		if m.Unavailable == "" && (m.Tools || !needs.Tools) {
			return true
		}
	}
	return false
}

// bindApprovedPlan approves a plan whose card was approved and binds it to
// its task; idempotent. A plan superseded meanwhile is refused with
// errPlanSuperseded, one approved for another task with "plan already
// decided".
func (e *Engine) bindApprovedPlan(ctx context.Context, planID, taskID string) error {
	err := e.Store.ApproveAgentPlan(ctx, planID, taskID)
	if err == nil || !(errors.Is(err, store.ErrAgentConflict) || errors.Is(err, store.ErrAgentTransition)) {
		return err
	}
	cur, gerr := e.Store.AgentPlan(ctx, planID)
	if gerr != nil {
		return err
	}
	switch {
	case cur.State == protocol.PlanApproved && cur.TaskID == taskID:
		return nil
	case cur.State == protocol.PlanSuperseded:
		return errPlanSuperseded
	case cur.State == protocol.PlanApproved:
		return fmt.Errorf("%s (approved for another task)", planDecidedText)
	}
	return err
}

// planRefusal maps a bind failure to the user-facing refusal.
func planRefusal(planID string, err error) error {
	if errors.Is(err, errPlanSuperseded) {
		return protocol.Errorf(protocol.CodeConflict, "plan %s: %v; answer the newer plan card", planID, errPlanSuperseded)
	}
	return err
}

// planCardRefusal explains why answering a card failed (it was resolved
// meanwhile, e.g. timed out) from the card's current status.
func (e *Engine) planCardRefusal(ctx context.Context, planID, approvalID string, err error) error {
	a, gerr := e.Store.GetApproval(ctx, approvalID)
	if gerr != nil {
		return err
	}
	switch a.Status {
	case "expired":
		if a.DecidedBy == "superseded" {
			return protocol.Errorf(protocol.CodeConflict, "plan %s: %v; answer the newer plan card", planID, errPlanSuperseded)
		}
		return protocol.Errorf(protocol.CodeConflict, "plan %s: %s", planID, planExpiredText)
	case "approved", "denied":
		return protocol.Errorf(protocol.CodeConflict, "plan %s: %s (%s)", planID, planDecidedText, a.Status)
	}
	return err
}

// publishAgentPlan announces a plan state change on its thread so clients
// refresh the card.
func (e *Engine) publishAgentPlan(plan protocol.AgentPlan, approvalStatus string) {
	e.Bus.Publish(plan.ThreadID, protocol.NotifyAgentPlanUpdated, protocol.AgentPlanUpdated{ThreadID: plan.ThreadID, Plan: plan, ApprovalStatus: approvalStatus})
}

// committedApproval is the card after a stored decision whose respond then
// failed: re-read when possible, else the known decided status; the
// resolution is announced (respondApproval failed before it could).
func committedApproval(e *Engine, ctx context.Context, card protocol.Approval, status string) protocol.Approval {
	if cur, err := e.Store.GetApproval(ctx, card.ID); err == nil {
		card = cur
	} else {
		card.Status = status
	}
	e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: card})
	return card
}

// foldedModelEdits returns model edits keyed by case-folded agent name; two
// names that fold to the same agent are refused, so no edit depends on map
// order.
func foldedModelEdits(models map[string]string) (map[string]string, error) {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make(map[string]string, len(models))
	for _, name := range names {
		k := strings.ToLower(strings.TrimSpace(name))
		if _, dup := out[k]; dup {
			return nil, protocol.Errorf(protocol.CodeInvalidParams, "models names agent %q more than once (names differ only by case or spaces); give one model per agent", strings.TrimSpace(name))
		}
		out[k] = models[name]
	}
	return out, nil
}
