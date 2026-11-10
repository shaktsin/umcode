package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/router"
	"github.com/shaktsin/umcode/internal/sandbox"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// errBudgetSliceExhausted ends an agent whose budget slice ran out. It is a
// failure, not a cancellation.
var errBudgetSliceExhausted = errors.New("budget slice exhausted")

type runtimeAgentKey struct{}

// withRuntimeAgent records the runtime agent whose turn ctx belongs to.
func withRuntimeAgent(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runtimeAgentKey{}, id)
}

// runtimeAgentFrom returns the runtime agent executing on ctx, if any.
func runtimeAgentFrom(ctx context.Context) (string, bool) {
	id, _ := ctx.Value(runtimeAgentKey{}).(string)
	return id, id != ""
}

// agentNeeds is what an agent's capabilities require from its model.
type agentNeeds struct{ Tools bool }

// agentNeedsFor derives model needs from capabilities alone: any granted
// file, shell or spawn capability is exercised through tools. The always-
// allowed context tools do not count: an agent granted nothing else is sent
// no tools at all (see offerTools in the turn loop).
func agentNeedsFor(c protocol.AgentCapabilities) agentNeeds {
	files := c.Files == protocol.FilesRead || c.Files == protocol.FilesReadWrite
	shell := c.Shell == protocol.ShellReadOnly || c.Shell == protocol.ShellReadWrite
	return agentNeeds{Tools: files || shell || c.SpawnMax > 0}
}

// modelRoute is one model of an agent's approved pool. Unavailable says why
// it cannot be routed right now (no usable or approved key, cooling down...).
type modelRoute struct {
	ID, Provider, Model string
	Tools               bool
	Unavailable         string
}

func (m modelRoute) name() string { return m.Provider + "/" + m.Model }

func (m modelRoute) matches(requested string) bool {
	return requested != "" && (requested == m.name() || requested == m.Model || (m.ID != "" && requested == m.ID))
}

// chooseRuntimeModel picks an agent's model: the requested one when it is in
// the pool and compatible, else the next compatible pool model in pool order.
// trail records every model tried and why it was skipped, ending with the one
// selected. With no compatible model the error names the requested model.
func chooseRuntimeModel(requested string, pool []modelRoute, needs agentNeeds) (actual string, trail []string, err error) {
	order := make([]modelRoute, 0, len(pool))
	found := false
	for _, m := range pool {
		if !found && m.matches(requested) {
			order = append([]modelRoute{m}, order...)
			found = true
			continue
		}
		order = append(order, m)
	}
	if requested != "" && !found {
		trail = append(trail, fmt.Sprintf("%s: not in the approved model pool", requested))
	}
	for _, m := range order {
		switch {
		case m.Unavailable != "":
			trail = append(trail, fmt.Sprintf("%s: %s", m.name(), m.Unavailable))
		case needs.Tools && !m.Tools:
			trail = append(trail, fmt.Sprintf("%s: cannot use tools", m.name()))
		default:
			return m.name(), append(trail, m.name()+": selected"), nil
		}
	}
	want := requested
	if want == "" {
		want = "(none requested)"
	}
	return "", trail, fmt.Errorf("no compatible model for requested model %q (tried: %s)", want, strings.Join(trail, "; "))
}

// runtimeExecution marks a childExecution as a runtime agent's. Its
// authority comes only from agent.Capabilities.
type runtimeExecution struct {
	agent    protocol.RuntimeAgent
	needs    agentNeeds
	modeFn   func() string // the lead chat's live approval mode
	isolated bool          // the workspace is the agent's own checkout outside the parent
}

// RuntimeWorkspaceResolver gives runtime agents their checkouts: an isolated
// writable checkout of the base revision for a write-capable agent, a
// distinct read-only view otherwise (the base revision, or the integrated
// head once revisions were accepted). FinalizeRuntime runs after the agent stops
// and must return the final revision (and changed paths for a writer).
type RuntimeWorkspaceResolver interface {
	ResolveRuntime(ctx context.Context, task protocol.AgentTask, agent protocol.RuntimeAgent, writable bool) (ChildWorkspace, error)
	FinalizeRuntime(ctx context.Context, task protocol.AgentTask, agent protocol.RuntimeAgent, ws ChildWorkspace) (ChildWorkspace, error)
}

func (m managedChildWorkspaces) ResolveRuntime(ctx context.Context, t protocol.AgentTask, a protocol.RuntimeAgent, writable bool) (ChildWorkspace, error) {
	w, err := m.service.PrepareRuntime(ctx, t, a, writable)
	return ChildWorkspace{ID: w.ID, Root: w.Root, Revision: w.BaseRevision}, err
}
func (m managedChildWorkspaces) FinalizeRuntime(ctx context.Context, t protocol.AgentTask, a protocol.RuntimeAgent, w ChildWorkspace) (ChildWorkspace, error) {
	r, err := m.service.SnapshotRuntime(ctx, t, a, delegation.WorkspaceRef{ID: w.ID, Root: w.Root, BaseRevision: w.Revision})
	return ChildWorkspace{ID: r.ID, Root: r.Root, Revision: r.ResultRevision, ChangedPaths: append([]string{}, r.ChangedPaths...)}, err
}

// runtimeRevisionRecorder is implemented by resolvers whose writer
// workspaces can be integrated: after a writer finished successfully its
// committed result is recorded as a submitted revision for agent.integrate.
// It is optional so resolvers without owned checkouts need nothing.
type runtimeRevisionRecorder interface {
	RecordRuntimeRevision(ctx context.Context, task protocol.AgentTask, agent protocol.RuntimeAgent, ws ChildWorkspace) error
}

func (m managedChildWorkspaces) RecordRuntimeRevision(ctx context.Context, t protocol.AgentTask, a protocol.RuntimeAgent, w ChildWorkspace) error {
	ws, err := m.service.Store.AgentWorkspace(ctx, w.ID)
	if err != nil {
		return err
	}
	_, err = m.service.RecordFinishedRuntimeRevision(ctx, t, a, ws)
	return err
}

// runtimeRevisionError explains a failure to record a finished writer's
// revision; the agent fails with it as its blocker.
func runtimeRevisionError(err error) error {
	if errors.Is(err, store.ErrAgentTransition) {
		return fmt.Errorf("revision not recorded: this agent already has a revision (one revision per agent): %w", err)
	}
	return fmt.Errorf("revision not recorded, so its work cannot be integrated (it stays in the agent's workspace): %w", err)
}

// runtimeViewDiscarder is implemented by resolvers that can remove a stopped
// read-only agent's view checkout. It is optional so resolvers without owned
// checkouts need nothing.
type runtimeViewDiscarder interface {
	DiscardRuntimeView(ctx context.Context, taskID, agentID string) error
}

func (m managedChildWorkspaces) DiscardRuntimeView(ctx context.Context, taskID, agentID string) error {
	return m.service.DiscardRuntimeView(ctx, taskID, agentID)
}

var _ RuntimeWorkspaceResolver = managedChildWorkspaces{}
var _ runtimeViewDiscarder = managedChildWorkspaces{}
var _ runtimeRevisionRecorder = managedChildWorkspaces{}

func (e *Engine) runtimeWorkspaces() RuntimeWorkspaceResolver {
	if e.RuntimeWorkspaces != nil {
		return e.RuntimeWorkspaces
	}
	if e.DelegationWorkspaceService != nil {
		return managedChildWorkspaces{service: e.DelegationWorkspaceService}
	}
	return nil
}

// isolatedChildRoot reports whether root is an owned checkout outside the
// task's project base and active parent checkout.
func isolatedChildRoot(task protocol.AgentTask, id, root string) bool {
	return !(id == "" || !filepath.IsAbs(root) || pathutil.Within(task.BaseRoot, root) || pathutil.Within(root, task.BaseRoot) || (task.ParentRoot != "" && (pathutil.Within(task.ParentRoot, root) || pathutil.Within(root, task.ParentRoot))))
}

var detectHostSandbox = sync.OnceValue(sandbox.Detect)

// runtimeConfinement resolves the confinement an agent's shell gets in a
// project, from the real signals: the project microVM toggle with an actual
// host availability probe (failing closed to none, never to the OS sandbox),
// else the detected host sandbox.
func (e *Engine) runtimeConfinement(scope *tools.Scope) tools.Confinement {
	conf, _ := e.runtimeConfinementWhy(scope)
	return conf
}

// runtimeAgentScope shapes a runtime agent's turn scope from its capabilities
// (runtimeScope). The shell counts as confined only when it runs in the
// agent's own workspace, and for a write-capable agent only when that
// workspace is its isolated checkout.
func (e *Engine) runtimeAgentScope(base *tools.Scope, x *childExecution) *tools.Scope {
	rt := x.runtime
	caps := rt.agent.Capabilities
	conf := e.runtimeConfinement(base)
	if !pathutil.SameFolder(base.Root, x.workspace.Root) || (caps.Files == protocol.FilesReadWrite && !rt.isolated) {
		conf = tools.ConfNone
	}
	return runtimeScope(base, caps, conf, rt.modeFn)
}

// leadApprovalTimeout bounds one lookup of the lead's approval mode; a slow
// store fails closed to the strictest mode instead of stalling a tool call.
const leadApprovalTimeout = 2 * time.Second

// leadApprovalMode reads the lead chat's approval mode from the task's root
// thread at each call, so a mid-run change applies to the agent's next tool
// call. The agent's own internal thread has no mode and is never consulted.
// An unreadable or foreign thread yields "" (the strictest mode).
func (e *Engine) leadApprovalMode(task protocol.AgentTask) func() string {
	return func() string {
		ctx, cancel := context.WithTimeout(context.Background(), leadApprovalTimeout)
		defer cancel()
		lookup := e.Store.GetThread
		if e.agentThreadLookup != nil {
			lookup = e.agentThreadLookup
		}
		th, err := lookup(ctx, task.ThreadID)
		if err != nil || th.ProjectID != task.ProjectID {
			return ""
		}
		return th.ApprovalMode
	}
}

// updateRuntimeAgent applies mutate under optimistic versioning; mutate
// returns false to skip the write.
func (e *Engine) updateRuntimeAgent(ctx context.Context, taskID, id string, mutate func(*protocol.RuntimeAgent) bool) error {
	if e.AgentRuntime != nil {
		// Through the runtime, so waiters wake and the events callback fires.
		return e.AgentRuntime.Update(ctx, taskID, id, mutate)
	}
	for i := 0; i < 8; i++ {
		a, err := e.Store.RuntimeAgent(ctx, taskID, id)
		if err != nil {
			return err
		}
		if !mutate(&a) {
			return nil
		}
		err = e.Store.UpdateRuntimeAgent(ctx, a, a.Version)
		if !errors.Is(err, store.ErrAgentConflict) {
			return err
		}
	}
	return store.ErrAgentConflict
}

// runtimeModelPool turns the task's frozen approved models into the pool an
// agent may run on, marking models no approved key can route right now.
func (e *Engine) runtimeModelPool(ctx context.Context, scope store.AgentModelScope, needs agentNeeds) []modelRoute {
	candidates := scope.Candidates
	if len(candidates) == 0 && scope.Model != "" {
		candidates = []protocol.ConfiguredModel{{Provider: scope.Provider, Model: scope.Model, Enabled: true}}
	}
	pool := make([]modelRoute, 0, len(candidates))
	for _, c := range candidates {
		m := modelRoute{ID: c.ID, Provider: c.Provider, Model: c.Model, Tools: e.Catalog.Lookup(ctx, c.Provider, c.Model).Tools}
		plan, err := e.Router.Plan(ctx, router.Request{
			Pinned:     protocol.ModelSelection{Provider: c.Provider, Model: c.Model, CredentialID: scope.CredentialID},
			Candidates: []protocol.ConfiguredModel{c},
			Strategy:   protocol.PoolPriority,
			Need:       router.Capabilities{Tools: needs.Tools},
		})
		switch {
		case err != nil:
			m.Unavailable = err.Error()
		default:
			usable := false
			for _, route := range plan.Routes {
				if len(scope.CredentialIDs) == 0 || slices.Contains(scope.CredentialIDs, route.Cred.Record.ID) {
					usable = true
					break
				}
			}
			if !usable {
				m.Unavailable = plan.Reason
				if m.Unavailable == "" && len(plan.Blocked) > 0 {
					m.Unavailable = plan.Blocked[0].Unavailable
				}
				if m.Unavailable == "" {
					m.Unavailable = "no approved key can serve it"
				}
			}
		}
		pool = append(pool, m)
	}
	return pool
}

// sliceExhausted reports whether an agent stopped because its own budget
// slice's limit ran out, not the shared task budget (parent) or an unrelated
// failure.
func sliceExhausted(slice, parent *delegation.Budget, turnErr string) bool {
	if parent.StopReason() != "" {
		return false
	}
	if slice.StopReason() != "" {
		return true
	}
	st, sc := slice.Remaining()
	pt, pc := parent.Remaining()
	if (st <= 0 && pt > 0) || (sc <= 0 && pc > 0) {
		return true
	}
	return strings.Contains(turnErr, "insufficient shared task") && (st < pt || sc < pc)
}

// runtimeRunner is the agentrt.Runner that executes runtime agents as engine
// turns on their internal execution threads.
type runtimeRunner struct{ engine *Engine }

// Run executes one runtime agent: execution identity, workspace, budget
// slice, model choice, then the shared child turn loop with the agent's
// capability-derived scope and its ID on the turn context. The result is the
// agent's final assistant text; the workspace is finalized after it stops.
func (r runtimeRunner) Run(ctx context.Context, a protocol.RuntimeAgent) (result protocol.AgentResult, err error) {
	e := r.engine
	task, err := e.Store.AgentTask(ctx, a.TaskID)
	if err != nil {
		return result, err
	}
	if !unfinishedDelegation(task.State) {
		return result, fmt.Errorf("agent %s: task %s is no longer active", a.ID, task.ID)
	}
	resolver := e.runtimeWorkspaces()
	if resolver == nil {
		return result, errors.New("runtime agent workspace resolver unavailable")
	}
	_, proj, err := e.childProjectBase(ctx, task)
	if err != nil {
		return result, err
	}
	shared := e.runtimeTaskBudget(ctx, task.ID)
	if shared == nil {
		return result, fmt.Errorf("agent %s: task budget unavailable", a.ID)
	}
	if owned := shared.Context(); owned != nil {
		// The agent lives no longer than the task that owns the budget.
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(owned, cancel)()
	}
	execution, err := e.Store.EnsureRuntimeAgentExecution(ctx, a)
	if err != nil {
		return result, err
	}
	writable := a.Capabilities.Files == protocol.FilesReadWrite
	ws, err := resolver.ResolveRuntime(ctx, task, a, writable)
	if err != nil {
		return result, err
	}
	// Every agent works in its own checkout; a writer's confinement relies on it.
	if !isolatedChildRoot(task, ws.ID, ws.Root) {
		return result, fmt.Errorf("agent %s: workspace must be an isolated owned checkout outside the parent", a.ID)
	}
	ws.Root = pathutil.Resolved(ws.Root)
	if info, statErr := os.Stat(ws.Root); statErr != nil || !info.IsDir() {
		return result, fmt.Errorf("agent %s: workspace is unavailable", a.ID)
	}
	if err = e.updateRuntimeAgent(ctx, task.ID, a.ID, func(c *protocol.RuntimeAgent) bool {
		changed := c.WorkspaceID != ws.ID || c.Revision != ""
		c.WorkspaceID, c.Revision = ws.ID, ""
		return changed
	}); err != nil {
		return result, err
	}
	defer func() {
		fail := func(cause error) {
			result.Blocker = fmt.Sprintf("workspace finalization failed: %v", cause)
			err = errors.Join(err, cause)
		}
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		latest, readErr := e.Store.RuntimeAgent(finalCtx, task.ID, a.ID)
		if readErr != nil {
			fail(readErr)
			return
		}
		final, finalErr := resolver.FinalizeRuntime(finalCtx, task, latest, ws)
		if finalErr == nil && (final.ID != ws.ID || !pathutil.SameFolder(final.Root, ws.Root) || final.Revision == "") {
			finalErr = errors.New("agent finalizer returned invalid workspace or missing final revision")
		}
		if finalErr != nil {
			fail(finalErr)
			return
		}
		result.ChangedPaths = nil
		if writable && len(final.ChangedPaths) > 0 {
			result.ChangedPaths = append([]string(nil), final.ChangedPaths...)
		}
		if updateErr := e.updateRuntimeAgent(finalCtx, task.ID, a.ID, func(c *protocol.RuntimeAgent) bool {
			c.WorkspaceID, c.Revision = final.ID, final.Revision
			return true
		}); updateErr != nil {
			fail(updateErr)
			return
		}
		// A writer that finished successfully submits its committed result
		// for integration before the scheduler marks it completed, so a
		// failure fails the agent with a blocker instead of losing the work
		// silently. A writer without changes records nothing.
		if rec, ok := resolver.(runtimeRevisionRecorder); ok && writable && err == nil && ctx.Err() == nil {
			finished, readErr := e.Store.RuntimeAgent(finalCtx, task.ID, a.ID)
			if readErr == nil {
				readErr = rec.RecordRuntimeRevision(finalCtx, task, finished, final)
			}
			if readErr != nil {
				cause := runtimeRevisionError(readErr)
				result.Blocker = cause.Error()
				err = cause
				return
			}
		}
		// A read-only agent's view checkout has nothing to integrate. A
		// write-capable agent's workspace is kept for integration.
		if d, ok := resolver.(runtimeViewDiscarder); ok && !writable {
			if discardErr := d.DiscardRuntimeView(finalCtx, task.ID, a.ID); discardErr != nil {
				e.Log.Warn("runtime agent view checkout retained", "agent", a.ID, "err", discardErr)
			}
		}
	}()
	// A lead's agent slices the task budget, a nested agent its parent's
	// slice; the entry is dropped when the runner returns (agent terminal).
	defer e.releaseRuntimeAgentBudget(task.ID, a.ID)
	slice, err := e.runtimeAgentBudget(ctx, a)
	if err != nil {
		result.Blocker = err.Error()
		return result, err
	}
	if ctx.Err() == nil && sliceExhausted(slice, shared, "") {
		result.Blocker = errBudgetSliceExhausted.Error()
		return result, errBudgetSliceExhausted
	}
	modelScope, err := e.Store.AgentModelScope(ctx, task.ID)
	if err != nil {
		return result, err
	}
	needs := agentNeedsFor(a.Capabilities)
	pool := e.runtimeModelPool(ctx, modelScope, needs)
	actual, trail, chooseErr := chooseRuntimeModel(a.ModelRequested, pool, needs)
	if persistErr := e.updateRuntimeAgent(ctx, task.ID, a.ID, func(c *protocol.RuntimeAgent) bool {
		c.ModelActual, c.RouteTrail = actual, trail
		return true
	}); persistErr != nil {
		return result, persistErr
	}
	if chooseErr != nil {
		return result, fmt.Errorf("agent %s: %w", a.ID, chooseErr)
	}
	pick := candidateSet{explicit: true, poolID: modelScope.PoolID, strategy: protocol.PoolPriority}
	for _, m := range pool {
		if m.name() == actual {
			pick.provider, pick.model = m.Provider, m.Model
		}
	}
	for _, c := range modelScope.Candidates {
		if c.Provider+"/"+c.Model == actual {
			pick.candidates = append([]protocol.ConfiguredModel{c}, pick.candidates...)
		}
	}
	// Later compatible pool models stay available to the router's failover.
	for i, m := range pool {
		if m.name() != actual && m.Unavailable == "" && (!needs.Tools || m.Tools) && i < len(modelScope.Candidates) {
			pick.candidates = append(pick.candidates, modelScope.Candidates[i])
		}
	}
	if len(pick.candidates) == 0 {
		pick.candidates = []protocol.ConfiguredModel{{Provider: pick.provider, Model: pick.model, Enabled: true}}
	}
	guidance, err := e.childGuidance(ctx, proj, ws.Root, a.ID, a.Assignment.Prompt)
	if err != nil {
		return result, err
	}
	identity := protocol.AgentChild{ID: a.ID, TaskID: a.TaskID, Assignment: a.Assignment, WorkspaceID: ws.ID}
	supplied, err := delegation.ScopedContextBuilder{Root: ws.Root, Guidance: guidance}.Build(ctx, task, identity)
	if err != nil {
		return result, err
	}
	execution.Context = supplied
	x := &childExecution{task: task, child: identity, workspace: ws, selection: pick, credentialID: modelScope.CredentialID, credentialIDs: modelScope.CredentialIDs,
		runtime: &runtimeExecution{agent: a, needs: needs, modeFn: e.leadApprovalMode(task), isolated: true}}
	ctx, stopCancellation := delegation.WithChildCancellation(ctx)
	defer stopCancellation()
	ctx = withRuntimeAgent(delegation.WithBudget(ctx, slice), a.ID)
	out, err := e.driveChildTurn(ctx, x, execution)
	if err != nil {
		return result, err
	}
	if e.agentTurnDone != nil {
		e.agentTurnDone(a.ID)
	}
	done, sctx := out.done, out.sctx
	err = out.readErr
	result.Summary = out.summary
	if writable {
		result.ChangedPaths = out.changed
	}
	if modelErr := e.Store.UpdateAgentExecutionModel(sctx, task.ID, a.ID, done.Resolved); modelErr != nil {
		err = errors.Join(err, modelErr)
	}
	usage, usageErr := e.Store.UsageTotalsWhere(sctx, "thread_id = ?", execution.ThreadID)
	if usageErr != nil {
		err = errors.Join(err, usageErr)
	} else {
		err = errors.Join(err, e.updateRuntimeAgent(sctx, task.ID, a.ID, func(c *protocol.RuntimeAgent) bool { c.Usage = usage; return true }))
	}
	// Messages that arrived after the last model-call boundary were never
	// read: say so in the result rather than dropping them silently.
	if note, noteErr := e.settleUnreadAgentMessages(sctx, task.ID, a.ID); noteErr != nil {
		err = errors.Join(err, noteErr)
	} else if note != "" {
		result.Summary = strings.TrimSpace(result.Summary + "\n\n" + note)
	}
	if done.Status != protocol.TurnCompleted {
		if ctx.Err() == nil && sliceExhausted(slice, shared, done.Error) {
			result.Blocker = errBudgetSliceExhausted.Error()
			if err != nil {
				e.Log.Warn("runtime agent bookkeeping", "agent", a.ID, "err", err)
			}
			return result, errBudgetSliceExhausted
		}
		err = errors.Join(err, fmt.Errorf("agent turn %s: %s", done.Status, done.Error))
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return result, err
}

// recordRuntimeModel keeps ModelActual on the model that actually answers;
// a router failover mid-turn is appended to the route trail.
func (e *Engine) recordRuntimeModel(ctx context.Context, x *childExecution, model protocol.ModelSelection) error {
	if err := e.Store.UpdateAgentExecutionModel(ctx, x.task.ID, x.child.ID, model); err != nil {
		return err
	}
	if model.Model == "" {
		return nil
	}
	actual := model.Provider + "/" + model.Model
	return e.updateRuntimeAgent(ctx, x.task.ID, x.runtime.agent.ID, func(a *protocol.RuntimeAgent) bool {
		if a.ModelActual == actual {
			return false
		}
		a.RouteTrail = append(a.RouteTrail, fmt.Sprintf("%s: failover from %s", actual, a.ModelActual))
		a.ModelActual = actual
		return true
	})
}

// setRuntimeApprovalState mirrors setChildApprovalState for a runtime agent.
func (e *Engine) setRuntimeApprovalState(ctx context.Context, x *childExecution, waiting bool) error {
	return e.updateRuntimeAgent(ctx, x.task.ID, x.runtime.agent.ID, func(a *protocol.RuntimeAgent) bool {
		if waiting && a.State == protocol.AgentStateRunning {
			a.State = protocol.AgentStateWaitingApproval
			return true
		}
		if !waiting && a.State == protocol.AgentStateWaitingApproval {
			a.State = protocol.AgentStateRunning
			return true
		}
		return false
	})
}

// agentMessageText is how a mailbox message appears in an agent's input.
func agentMessageText(m protocol.AgentMessage) string {
	from := "the lead"
	if m.FromAgentID != "" {
		from = "agent " + m.FromAgentID
	}
	return fmt.Sprintf("Message from %s:\n%s", from, m.Body)
}

// consumeAgentMessages delivers a runtime agent's pending mailbox messages
// into its input at a model-call boundary. Each is persisted under a
// deterministic item ID before it is acknowledged, so a failed
// acknowledgement never delivers it twice; each is acknowledged once.
func (e *Engine) consumeAgentMessages(ctx, sctx context.Context, turn protocol.Turn, msgs []llm.Message, x *childExecution) ([]llm.Message, bool, error) {
	pending, err := e.Store.PendingAgentMessages(ctx, x.task.ID, x.runtime.agent.ID)
	if err != nil {
		return msgs, false, err
	}
	added := false
	for _, m := range pending {
		text := agentMessageText(m)
		if len(text) > delegation.AssignmentLimit {
			return msgs, added, errors.New("agent message exceeds assignment context limit")
		}
		id := fmt.Sprintf("agent-message-%x", sha256.Sum256([]byte(turn.ThreadID+"\x00"+m.ID)))
		existing, readErr := e.Store.GetItem(sctx, id)
		if errors.Is(readErr, store.ErrNotFound) {
			item, itemErr := e.newItem(sctx, turn, protocol.ItemUserMessage)
			if itemErr != nil {
				return msgs, added, itemErr
			}
			item.ID = id
			item.Text = text
			item.Status = protocol.ItemCompleted
			if err = e.Store.SaveItem(sctx, item); err != nil {
				return msgs, added, err
			}
			msgs = appendText(msgs, llm.RoleUser, text)
			added = true
		} else if readErr != nil {
			return msgs, added, readErr
		} else if existing.TurnID == turn.ID {
			return msgs, added, errors.New("agent message already recorded in current turn without acknowledgement")
		}
		if err = e.Store.AcknowledgeAgentMessage(ctx, x.task.ID, m.ID); err != nil {
			return msgs, added, err
		}
	}
	return msgs, added, nil
}

// unreadExcerptLimit bounds how much of an unread message's body is quoted.
const unreadExcerptLimit = 200

// settleUnreadAgentMessages acknowledges the agent's messages still pending
// when it finished (they arrived after its last model-call boundary) and
// returns a note for its result naming each one, or "" when there are none.
// A message enqueued after this check and before the agent is persisted
// terminal is still possible; the window is the runner's final bookkeeping.
func (e *Engine) settleUnreadAgentMessages(ctx context.Context, taskID, agentID string) (string, error) {
	pending, err := e.Store.PendingAgentMessages(ctx, taskID, agentID)
	if err != nil || len(pending) == 0 {
		return "", err
	}
	parts := make([]string, 0, len(pending))
	for _, m := range pending {
		from := "the lead"
		if m.FromAgentID != "" {
			from = "agent " + m.FromAgentID
		}
		body := m.Body
		if r := []rune(body); len(r) > unreadExcerptLimit {
			body = string(r[:unreadExcerptLimit]) + "..."
		}
		parts = append(parts, fmt.Sprintf("from %s: %q", from, body))
		if err = e.Store.AcknowledgeAgentMessage(ctx, taskID, m.ID); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Note: %d message(s) arrived after this agent's last step and were not read (%s).", len(pending), strings.Join(parts, "; ")), nil
}

// runtimeAgentPrompt is a runtime agent's system prompt: its purpose, its
// assignment and a plain-words summary of what its capabilities allow.
func runtimeAgentPrompt(a protocol.RuntimeAgent, root string) string {
	var b strings.Builder
	b.WriteString("You are a UMCode agent working on one part of a larger task. Complete only your assignment, then stop.\n")
	if p := strings.TrimSpace(a.Purpose); p != "" {
		fmt.Fprintf(&b, "\nPurpose:\n%s\n", p)
	}
	fmt.Fprintf(&b, "\nAssignment:\n%s\n", a.Assignment.Prompt)
	if len(a.Assignment.AcceptanceChecks) > 0 {
		b.WriteString("\nDone when:\n")
		for _, c := range a.Assignment.AcceptanceChecks {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	b.WriteString("\nWhat you can do:\n")
	for _, line := range capabilitySummary(a.Capabilities) {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	fmt.Fprintf(&b, "\nYour working folder is %s; all file access stays inside it. Project guidance (UMCODE.md) and any supplied references are in your first message. Messages from the lead or other agents arrive as user messages. Finish with a short final message: what you did, what you found, which files you changed and the actual results of any checks you ran. Never claim a check passed that you did not run. If something blocks you, say what it is.\n", root)
	return b.String()
}

// capabilitySummary states an agent's capabilities in plain words.
func capabilitySummary(c protocol.AgentCapabilities) []string {
	var out []string
	switch c.Files {
	case protocol.FilesReadWrite:
		out = append(out, "You can read and change files in your own copy of the project.")
	case protocol.FilesRead:
		out = append(out, "You can read files but not change them.")
	default:
		out = append(out, "You cannot read or change files.")
	}
	switch c.Shell {
	case protocol.ShellReadWrite:
		out = append(out, "You can run commands in a sandbox; they may change files in your own copy of the project.")
	case protocol.ShellReadOnly:
		out = append(out, "You can run commands in a read-only sandbox.")
	default:
		out = append(out, "You cannot run commands.")
	}
	if c.Network {
		out = append(out, "You have network access.")
	} else {
		out = append(out, "You have no network access.")
	}
	if c.SpawnMax > 0 {
		out = append(out, fmt.Sprintf("You may start up to %d helper agents.", c.SpawnMax))
	}
	return out
}

// childNeedsTools is the router's tool requirement for a turn: always for
// the lead and legacy children; per capabilities for a runtime agent.
func childNeedsTools(ctx context.Context) bool {
	if x := childExecutionFrom(ctx); x != nil && x.runtime != nil {
		return x.runtime.needs.Tools
	}
	return true
}
