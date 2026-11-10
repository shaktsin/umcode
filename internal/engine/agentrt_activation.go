package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// delegationLeadPrompt is the system prompt layer of a lead turn admitted for
// delegation. The lead decides whether and how to use agents; authority comes
// only from the plan the user approves, never from what an agent is called.
const delegationLeadPrompt = `Agents are enabled for this task. You decide whether to use them, what each one does, what to check and when the user's request is satisfied. To use agents, propose a plan with agent.propose_plan and wait for the user's decision; each agent gets only the capabilities its approved plan entry grants. Spawn approved entries with agent.spawn, giving each focused requirements and relevant file references; its context is fresh. Accept or reject submitted revisions with agent.integrate, run the planned verification checks in the integrated checkout, and finish with agent.integrate {"action":"complete"} only after the checks actually passed. Agent findings and check results are evidence for your judgment, not engine certification. Workspace isolation, permissions, cancellation and the shared task budget apply. Never claim a blocked check passed.`

// suggestStopMessage is what agent.suggest tells the model: the suggesting
// turn ends, and the user's answer arrives as a new turn.
const suggestStopMessage = "Stop now: the user decides whether to enable agents. Do not call agent.* tools or continue the task in this turn."

// suggestOnlyTool reports agent.suggest. Unlike the other agent.* tools it is
// offered only in turns that are NOT admitted for delegation, so it is exempt
// from the "admitted task required" gate (and gated the other way round in
// permittedToolFor).
func (e *Engine) suggestOnlyTool(name string) bool {
	return e.AgentRuntime != nil && tools.FromWire(name) == "agent.suggest"
}

// suggestAgents records the lead's agent.suggest card. It grants nothing:
// delegation is still enabled by the app before a new turn starts. The rules
// are checked in this order and every refusal names its rule: lead only,
// runtime available, turn not admitted, committed Git project, no active delegation
// task in the chat, one per user message, not after a decline.
func (e *Engine) suggestAgents(ctx context.Context, reason string) (string, error) {
	if id, ok := runtimeCaller(ctx); !ok || id != "" {
		return "", errors.New("agent.suggest is lead-only: agents and child tasks cannot suggest enabling agents")
	}
	if e.AgentRuntime == nil {
		return "", errors.New("agent.suggest refused: the agent runtime is unavailable")
	}
	if _, admitted := DelegationTaskFromContext(ctx); admitted {
		return "", errors.New("agent.suggest refused: this turn is already admitted for delegation; agents are enabled, so do not suggest them")
	}
	turn, ok := ctx.Value(executionTurnKey{}).(protocol.Turn)
	if !ok || turn.ID == "" || turn.ThreadID == "" {
		return "", errors.New("agent.suggest refused: it needs a running user turn")
	}
	if scope := tools.ScopeFrom(ctx); scope != nil && scope.ThreadID != "" && scope.ThreadID != turn.ThreadID {
		return "", errors.New("agent.suggest refused: the turn does not belong to this chat")
	}
	base, err := e.delegationBase(ctx, turn.ThreadID)
	if err != nil {
		return "", fmt.Errorf("agent.suggest refused: agents need a committed Git project (%v)", err)
	}
	if task, err := e.Store.ActiveAgentTask(ctx, base.ThreadID, base.ProjectID); err == nil {
		return "", fmt.Errorf("agent.suggest refused: this chat already has an active delegation task (%s); it continues with its task ID", task.ID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if last, err := e.Store.LatestAgentSuggestion(ctx, turn.ThreadID); err == nil && last.TurnID == turn.ID {
		return "", errors.New("agent.suggest refused: only one per user message, and this message already has one; stop and let the user answer it")
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if declined, err := e.Store.ThreadDeclinedSuggestion(ctx, turn.ThreadID); err != nil {
		return "", err
	} else if declined {
		return "", errors.New("agent.suggest refused: the user declined agents in this chat; do not suggest them again unless the user enables agents themselves")
	}
	sug, err := e.Store.CreateAgentSuggestion(ctx, store.AgentSuggestion{ThreadID: turn.ThreadID, TurnID: turn.ID, Reason: reason})
	if errors.Is(err, store.ErrAgentConflict) {
		return "", errors.New("agent.suggest refused: only one per user message, and this message already has one; stop and let the user answer it")
	}
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(struct {
		SuggestionID string `json:"suggestionId"`
		State        string `json:"state"`
		Message      string `json:"message"`
	}{sug.ID, sug.State, suggestStopMessage})
	return string(out), err
}

// closeUnproposedActivation runs when a root turn admitted for delegation
// ends: a runtime task that never got a plan (no plan card waiting, no
// proposed or approved plan, no agents, no revisions) is completed, so an
// activation whose turn never called agent.propose_plan does not block
// delegation/enable or agent.suggest later in the chat. It reports whether
// the task was closed; it shares closeEmptyRuntimeTask's checks and locks.
func (e *Engine) closeUnproposedActivation(ctx context.Context, taskID string) bool {
	return e.closeEmptyRuntimeTaskIf(ctx, taskID, true)
}

// endAdmittedTurn is called by finishTurn for every turn: only a root turn
// (not an agent or child) of a task admitted with the agent runtime on is
// considered for closeUnproposedActivation.
func (e *Engine) endAdmittedTurn(ctx context.Context) {
	if e.AgentRuntime == nil || delegationChild(ctx) || childExecutionFrom(ctx) != nil {
		return
	}
	if id, ok := runtimeAgentFrom(ctx); ok && id != "" {
		return
	}
	task, ok := DelegationTaskFromContext(ctx)
	if !ok || task.ID == "" {
		return
	}
	e.closeUnproposedActivation(context.WithoutCancel(ctx), task.ID)
}

// RespondAgentSuggestion records the user's answer to a suggestion card
// (agent/suggestion/respond; the server allows it for app connections only).
// enable prepares delegation exactly as delegation/enable does and returns
// its one-use activation token in the same call; the client then submits the
// follow-up turn/start carrying it. decline suppresses later suggestions in
// the chat until the user enables agents themselves. Answers are serialized
// (suggestionMu): the open check, the token and the decision are one step, so
// concurrent answers never both act. Repeating the decision already recorded
// returns the suggestion unchanged and never mints a second token. If
// preparing delegation fails, the suggestion stays pending and nothing is
// cleared. Declines are lifted only after the enabled decision is stored,
// with its token kept.
func (e *Engine) RespondAgentSuggestion(ctx context.Context, p protocol.AgentSuggestionRespondParams) (protocol.AgentSuggestionRespondResult, error) {
	var res protocol.AgentSuggestionRespondResult
	if delegationChild(ctx) || childExecutionFrom(ctx) != nil {
		return res, protocol.Errorf(protocol.CodeInvalidRequest, "children cannot answer agent suggestions")
	}
	if p.SuggestionID == "" {
		return res, protocol.Errorf(protocol.CodeInvalidParams, "suggestionId required")
	}
	var state string
	switch p.Decision {
	case protocol.SuggestionEnable:
		state = store.SuggestionEnabled
	case protocol.SuggestionDecline:
		state = store.SuggestionDeclined
	default:
		return res, protocol.Errorf(protocol.CodeInvalidParams, "decision must be %q or %q", protocol.SuggestionEnable, protocol.SuggestionDecline)
	}
	e.suggestionMu.Lock()
	defer e.suggestionMu.Unlock()
	cur, err := e.Store.GetAgentSuggestion(ctx, p.SuggestionID)
	if errors.Is(err, store.ErrNotFound) {
		return res, protocol.Errorf(protocol.CodeNotFound, "agent suggestion %s not found", p.SuggestionID)
	}
	if err != nil {
		return res, err
	}
	if cur.State == state {
		res.Suggestion = suggestionView(cur)
		return res, nil
	}
	if err = suggestionOpen(cur); err != nil {
		return res, err
	}
	if e.suggestionRespondHook != nil {
		e.suggestionRespondHook()
	}
	var activation protocol.DelegationEnableResult
	if state == store.SuggestionEnabled {
		if activation, err = e.prepareDelegationToken(ctx, cur.ThreadID); err != nil {
			return res, err
		}
	}
	decided, err := e.Store.DecideAgentSuggestion(ctx, cur.ID, state)
	if err != nil {
		e.revokeDelegationToken(activation.ActivationToken)
		if errors.Is(err, store.ErrAgentTransition) || errors.Is(err, store.ErrAgentConflict) {
			if now, gerr := e.Store.GetAgentSuggestion(ctx, cur.ID); gerr == nil {
				if oerr := suggestionOpen(now); oerr != nil {
					return res, oerr
				}
			}
			return res, protocol.Errorf(protocol.CodeConflict, "agent suggestion changed while answering it; reload it")
		}
		return res, err
	}
	if state == store.SuggestionEnabled {
		// The explicit enabled response opens a decline-free window. A
		// failure here only keeps suggestions suppressed (fail safe).
		if err = e.Store.ClearThreadDeclines(ctx, cur.ThreadID); err != nil {
			e.Log.Warn("could not lift agent suggestion declines after enable", "thread", cur.ThreadID, "err", err)
		}
	}
	res.Suggestion = suggestionView(decided)
	if activation.ActivationToken != "" {
		res.ActivationToken = activation.ActivationToken
		expires := activation.ExpiresAt
		res.ExpiresAt = &expires
	}
	return res, nil
}

// suggestionOpen refuses answering a suggestion that is no longer pending.
func suggestionOpen(s store.AgentSuggestion) error {
	switch s.State {
	case store.SuggestionPending:
		return nil
	case store.SuggestionExpired:
		return protocol.Errorf(protocol.CodeConflict, "agent suggestion expired (the engine restarted); enable agents from the Agents menu instead")
	default:
		return protocol.Errorf(protocol.CodeConflict, "agent suggestion was already %s", s.State)
	}
}

// GetAgentSuggestion is agent/suggestion/get: the stored state of one
// suggestion card. It never changes anything.
func (e *Engine) GetAgentSuggestion(ctx context.Context, p protocol.AgentSuggestionGetParams) (protocol.AgentSuggestion, error) {
	if delegationChild(ctx) || childExecutionFrom(ctx) != nil {
		return protocol.AgentSuggestion{}, protocol.Errorf(protocol.CodeInvalidRequest, "children cannot read agent suggestions")
	}
	if p.SuggestionID == "" {
		return protocol.AgentSuggestion{}, protocol.Errorf(protocol.CodeInvalidParams, "suggestionId required")
	}
	s, err := e.Store.GetAgentSuggestion(ctx, p.SuggestionID)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.AgentSuggestion{}, protocol.Errorf(protocol.CodeNotFound, "agent suggestion %s not found", p.SuggestionID)
	}
	if err != nil {
		return protocol.AgentSuggestion{}, err
	}
	return suggestionView(s), nil
}

func suggestionView(s store.AgentSuggestion) protocol.AgentSuggestion {
	return protocol.AgentSuggestion{ID: s.ID, ThreadID: s.ThreadID, TurnID: s.TurnID, Reason: s.Reason, State: s.State, CreatedAt: s.CreatedAt, DecidedAt: s.DecidedAt}
}

// revokeDelegationToken drops an unused activation token.
func (e *Engine) revokeDelegationToken(token string) {
	if token == "" {
		return
	}
	e.mu.Lock()
	delete(e.delegationTokens, token)
	e.mu.Unlock()
}

// expireAgentSuggestions runs at startup: a
// suggestion still pending lost its card with the previous engine, so it is
// shown stale (expired) and nothing starts. Failure is logged, never fatal.
func (e *Engine) expireAgentSuggestions(ctx context.Context) {
	n, err := e.Store.ExpirePendingAgentSuggestions(ctx)
	if err != nil {
		e.Log.Warn("could not expire pending agent suggestions", "err", err)
		return
	}
	if n > 0 {
		e.Log.Info("expired agent suggestions left pending by the previous run", "count", n)
	}
}
