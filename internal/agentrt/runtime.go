package agentrt

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
)

// SpawnResult is the outcome of a successful Spawn.
type SpawnResult struct {
	Agent        protocol.RuntimeAgent
	Granted      protocol.AgentCapabilities
	Rejected     []protocol.CapabilityRejection
	QueuedReason string
}

// WaitMode selects whether Wait returns on the first or on every listed agent
// reaching a terminal state.
type WaitMode string

const (
	WaitAny WaitMode = "any"
	WaitAll WaitMode = "all"
)

// Service is the agent runtime as seen by the lead and by runtime agents.
type Service interface {
	Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error)
	List(ctx context.Context, taskID string) ([]protocol.RuntimeAgent, error)
	Status(ctx context.Context, taskID, id string) (protocol.RuntimeAgent, error)
	Wait(ctx context.Context, taskID string, ids []string, mode WaitMode) ([]protocol.RuntimeAgent, error)
	Message(ctx context.Context, taskID, fromID, toID, body string) (protocol.AgentMessage, error)
	Cancel(ctx context.Context, taskID, id string) error
}

// Option configures a Runtime.
type Option func(*Runtime)

// WithResolveOptions sets the capability resolution options used by Admit.
func WithResolveOptions(o ResolveOptions) Option {
	return func(r *Runtime) { r.resolve = o }
}

// WithEvents registers a callback for persisted agent changes, including
// creation. See NewScheduler for ordering guarantees.
func WithEvents(fn func(protocol.RuntimeAgent)) Option {
	return func(r *Runtime) { r.events = fn }
}

// Runtime composes plan-bound admission, the budget-share ledger and the
// scheduler. The store is the source of truth: the ledger is rebuilt from
// stored agents at every Spawn, so cancels and restarts never drift.
type Runtime struct {
	store   AgentStore
	sched   *Scheduler
	resolve ResolveOptions
	events  func(protocol.RuntimeAgent)

	// lifecycle serialises Spawn so admission, reservation and creation are
	// atomic with respect to each other.
	lifecycle sync.Mutex
}

var _ Service = (*Runtime)(nil)

// NewRuntime returns a Runtime whose agents run under contexts derived from
// base (see NewScheduler).
func NewRuntime(s AgentStore, r Runner, base context.Context, opts ...Option) *Runtime {
	rt := &Runtime{store: s}
	for _, o := range opts {
		o(rt)
	}
	rt.sched = NewScheduler(s, r, base, rt.emit)
	return rt
}

func (r *Runtime) emit(a protocol.RuntimeAgent) {
	if r.events != nil {
		r.events(a)
	}
}

// Recover interrupts agents left active by a previous process. It never
// starts a runner.
func (r *Runtime) Recover(ctx context.Context) error {
	return r.store.ReconcileRuntimeAgents(ctx)
}

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Spawn admits and starts (or queues) an agent. It is idempotent by
// (TaskID, CallID): a repeated call returns the stored agent without
// scheduling it again.
func (r *Runtime) Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error) {
	if req.TaskID == "" || req.CallID == "" {
		return SpawnResult{}, errors.New("spawn: task ID and call ID are required")
	}
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()

	// AgentStore reports absence as an error it does not type; any non-context
	// error here means "not yet spawned". CreateRuntimeAgent is itself
	// idempotent per (task, callID), so a misread cannot create a duplicate.
	if existing, err := r.store.RuntimeAgentForCall(ctx, req.TaskID, req.CallID); err == nil {
		return SpawnResult{Agent: existing, Granted: existing.Capabilities}, nil
	} else if isCtxErr(err) {
		return SpawnResult{}, err
	}

	plan, err := r.store.ApprovedAgentPlanForTask(ctx, req.TaskID)
	if err != nil {
		if isCtxErr(err) {
			return SpawnResult{}, err
		}
		return SpawnResult{}, &AdmissionError{Field: "plan", Message: fmt.Sprintf("the task has no approved plan (%v); propose a plan and get it approved before spawning agents", err)}
	}

	all, err := r.store.RuntimeAgents(ctx, req.TaskID)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("spawn: list agents: %w", err)
	}
	ledger := RebuildLedger(all)

	caller := protocol.RuntimeAgent{Capabilities: RootCapabilities(), Depth: 0, BudgetShare: 1.0}
	if req.CallerAgentID != "" {
		caller, err = r.store.RuntimeAgent(ctx, req.TaskID, req.CallerAgentID)
		if err != nil {
			if isCtxErr(err) {
				return SpawnResult{}, err
			}
			return SpawnResult{}, &AdmissionError{Field: "caller", Message: fmt.Sprintf("caller agent %q not found in task: %v", req.CallerAgentID, err)}
		}
		if isTerminal(caller.State) {
			return SpawnResult{}, &AdmissionError{Field: "caller", Message: fmt.Sprintf("caller agent %q is %s and may not spawn", caller.ID, caller.State)}
		}
	}

	adm, err := Admit(AdmissionInput{
		Plan:            plan,
		Caller:          caller,
		All:             all,
		ParentShareFree: ledger.Free(caller.ID, caller.BudgetShare),
		Options:         r.resolve,
		Request:         req,
	})
	if err != nil {
		return SpawnResult{}, err
	}

	name := req.Name
	if name == "" {
		name = adm.Entry.Name
	}
	created, err := r.store.CreateRuntimeAgent(ctx, protocol.RuntimeAgent{
		TaskID:         req.TaskID,
		ParentAgentID:  req.CallerAgentID,
		Name:           name,
		Purpose:        adm.Entry.Purpose,
		PlanID:         plan.ID,
		Assignment:     req.Assignment,
		Capabilities:   adm.Granted,
		ModelRequested: adm.Model,
		BudgetShare:    adm.Share,
		Depth:          adm.Depth,
		ContextMode:    req.ContextMode,
		State:          protocol.AgentStateQueued,
	}, req.CallID)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("spawn: create agent: %w", err)
	}
	// The ledger is rebuilt from the store at the next Spawn; reserving here
	// keeps this call's view consistent.
	ledger.Reserve(created.ID, created.ParentAgentID, created.BudgetShare)
	r.emit(created)

	maxConc := plan.Limits.MaxConcurrent
	if maxConc < 1 {
		maxConc = 1
	}
	r.sched.Submit(ctx, created, maxConc)

	res := SpawnResult{Agent: created, Granted: adm.Granted, Rejected: adm.Rejected}
	if cur, err := r.store.RuntimeAgent(context.WithoutCancel(ctx), req.TaskID, created.ID); err == nil {
		res.Agent = cur
	}
	if res.Agent.State == protocol.AgentStateQueued {
		if active := r.sched.Active(req.TaskID); active >= maxConc {
			res.QueuedReason = fmt.Sprintf("capacity: %d/%d running", active, maxConc)
		}
	}
	return res, nil
}

// List returns the task's agents in creation order.
func (r *Runtime) List(ctx context.Context, taskID string) ([]protocol.RuntimeAgent, error) {
	return r.store.RuntimeAgents(ctx, taskID)
}

// Status returns one agent of the task.
func (r *Runtime) Status(ctx context.Context, taskID, id string) (protocol.RuntimeAgent, error) {
	a, err := r.store.RuntimeAgent(ctx, taskID, id)
	if err != nil {
		return protocol.RuntimeAgent{}, fmt.Errorf("agent %s: %w", id, err)
	}
	return a, nil
}

// Wait blocks until any (WaitAny) or all (WaitAll) of the listed agents are
// terminal, or ctx is done, and returns their current states in ids order.
func (r *Runtime) Wait(ctx context.Context, taskID string, ids []string, mode WaitMode) ([]protocol.RuntimeAgent, error) {
	if len(ids) == 0 {
		return nil, errors.New("wait: no agent ids given")
	}
	if mode != WaitAny && mode != WaitAll {
		return nil, fmt.Errorf("wait: unknown mode %q", mode)
	}
	for {
		// Take the channel before reading state so no change is missed.
		changed := r.sched.Changed(taskID)
		agents := make([]protocol.RuntimeAgent, len(ids))
		done := 0
		for i, id := range ids {
			a, err := r.store.RuntimeAgent(ctx, taskID, id)
			if err != nil {
				if isCtxErr(err) {
					return nil, err
				}
				return nil, fmt.Errorf("wait: agent %s: %w", id, err)
			}
			agents[i] = a
			if isTerminal(a.State) {
				done++
			}
		}
		if (mode == WaitAny && done > 0) || (mode == WaitAll && done == len(ids)) {
			return agents, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return agents, ctx.Err()
		}
	}
}

// Message queues a message for a live agent of the task. fromID "" is the lead.
func (r *Runtime) Message(ctx context.Context, taskID, fromID, toID, body string) (protocol.AgentMessage, error) {
	to, err := r.store.RuntimeAgent(ctx, taskID, toID)
	if err != nil {
		return protocol.AgentMessage{}, fmt.Errorf("message: recipient %s: %w", toID, err)
	}
	if isTerminal(to.State) {
		return protocol.AgentMessage{}, fmt.Errorf("message: recipient %s is %s", toID, to.State)
	}
	if fromID != "" {
		if _, err := r.store.RuntimeAgent(ctx, taskID, fromID); err != nil {
			return protocol.AgentMessage{}, fmt.Errorf("message: sender %s: %w", fromID, err)
		}
	}
	return r.store.EnqueueAgentMessage(ctx, protocol.AgentMessage{TaskID: taskID, FromAgentID: fromID, ToAgentID: toID, Body: body})
}

// Cancel cancels an agent and its descendants. Their shares are free from the
// next Spawn on, since the ledger is rebuilt from stored agents.
func (r *Runtime) Cancel(ctx context.Context, taskID, id string) error {
	return r.sched.Cancel(ctx, taskID, id)
}
