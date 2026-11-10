package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// fillRuntimeSnapshot adds a task's agent runtime state to its read model:
// the approved plan, and for a task with runtime agents the agents (creation
// order, without workspace IDs) and the integration status. Legacy tasks have
// neither, so their snapshot is unchanged.
func (e *Engine) fillRuntimeSnapshot(ctx context.Context, task protocol.AgentTask, snap *protocol.AgentSnapshot) error {
	plan, err := e.Store.ApprovedAgentPlanForTask(ctx, task.ID)
	switch {
	case err == nil:
		snap.Plan = &plan
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	agents, err := e.Store.RuntimeAgents(ctx, task.ID)
	if err != nil || len(agents) == 0 {
		return err
	}
	snap.Integration, err = e.runtimeIntegrationView(ctx, task, agents)
	if err != nil {
		return err
	}
	for i := range agents {
		agents[i].WorkspaceID = ""
	}
	snap.Agents = agents
	return nil
}

// runtimeIntegrationView is the read model of the task's integration (what
// agent.integrate status reports), or nil when nothing was submitted or
// applied yet. A revision left applying in a task that is no longer active
// can never be settled by an accept: it is reported as Blocked.
func (e *Engine) runtimeIntegrationView(ctx context.Context, task protocol.AgentTask, agents []protocol.RuntimeAgent) (*protocol.AgentIntegrationStatus, error) {
	svc := e.DelegationWorkspaceService
	if svc == nil {
		return nil, nil
	}
	st, h, ok, err := svc.ReadRuntimeIntegration(ctx, task.ID, agents)
	if err != nil || !ok {
		return nil, err
	}
	view := &protocol.AgentIntegrationStatus{HeadTree: st.HeadTree, AppliedCount: st.AppliedCount, Verified: st.Verified, Amendments: st.Amendments, AmendReason: h.AmendReason}
	var blocked []string
	for _, r := range st.Revisions {
		view.Revisions = append(view.Revisions, protocol.AgentRevisionView{AgentID: r.AgentID, State: r.State, Conflict: strings.Join(r.Conflict, ", ")})
		if r.State == store.RevisionApplying && !unfinishedDelegation(task.State) {
			blocked = append(blocked, r.AgentID)
		}
	}
	if len(blocked) > 0 {
		view.Blocked = fmt.Sprintf("the apply of agent %s was interrupted and could not be settled; the project checkout may hold part of that work", strings.Join(blocked, ", "))
	}
	return view, nil
}

// delegationSequence is the next delegation/updated sequence number, from the
// counter legacy child events use.
func (e *Engine) delegationSequence() int64 {
	if e.DelegationCoordinator == nil {
		return 0
	}
	return e.DelegationCoordinator.NextSequence()
}

// publishRuntimeAgent announces a runtime agent's change to its task's chat.
// It publishes the stored agent, read under runtimePublishMu together with
// the sequence number, so an event that arrives late (the runtime's events
// may race across goroutines) never publishes an older state after a newer
// one: per agent, sequence and Version grow together.
func (e *Engine) publishRuntimeAgent(taskID, agentID string) {
	if e.Store == nil || e.Bus == nil {
		return
	}
	ctx := context.Background()
	task, err := e.Store.AgentTask(ctx, taskID)
	if err != nil {
		return
	}
	e.runtimePublishMu.Lock()
	defer e.runtimePublishMu.Unlock()
	a, err := e.Store.RuntimeAgent(ctx, taskID, agentID)
	if err != nil {
		return
	}
	a.WorkspaceID = ""
	e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: taskID, Agent: &a, Sequence: e.delegationSequence()})
}

// publishRuntimeIntegration announces the task's current integration status
// to its chat; nothing when there is none to show yet.
func (e *Engine) publishRuntimeIntegration(taskID string) {
	if e.Store == nil || e.Bus == nil {
		return
	}
	ctx := context.Background()
	e.runtimePublishMu.Lock()
	defer e.runtimePublishMu.Unlock()
	task, err := e.Store.AgentTask(ctx, taskID)
	if err != nil {
		return
	}
	agents, err := e.Store.RuntimeAgents(ctx, taskID)
	if err != nil {
		return
	}
	view, err := e.runtimeIntegrationView(ctx, task, agents)
	if err != nil || view == nil {
		return
	}
	e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: taskID, Integration: view, Sequence: e.delegationSequence()})
}

// delegationRuntimeEvent receives the delegation service's runtime changes
// (delegation.WorkspaceService.WithEvents). Agent events may come with
// service locks held: publishing only reads the store.
func (e *Engine) delegationRuntimeEvent(ev delegation.AgentEvent) {
	if ev.Agent != nil {
		e.publishRuntimeAgent(ev.TaskID, ev.Agent.ID)
	}
	if ev.IntegrationChanged {
		e.publishRuntimeIntegration(ev.TaskID)
	}
}
