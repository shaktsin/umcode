package agentrt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
)

// SpawnResult is the outcome of a successful Spawn. A replay of an
// already-spawned (TaskID, CallID) returns the stored agent with its stored
// granted capabilities and stored Rejected list.
// QueuedReason is set only while the agent waits in the scheduler queue.
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
//
// callerID (fromID for Message) names the calling agent; "" is the lead. A
// caller may target only itself or its descendants; the lead may target any
// agent of the task. Out-of-scope targets fail with ErrOutOfScope. List is
// task-wide for the lead and returns the caller's descendants otherwise.
type Service interface {
	Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error)
	List(ctx context.Context, taskID, callerID string) ([]protocol.RuntimeAgent, error)
	Status(ctx context.Context, taskID, callerID, id string) (protocol.RuntimeAgent, error)
	Wait(ctx context.Context, taskID, callerID string, ids []string, mode WaitMode) ([]protocol.RuntimeAgent, error)
	Message(ctx context.Context, taskID, fromID, toID, body, callID string) (protocol.AgentMessage, error)
	Cancel(ctx context.Context, taskID, callerID, id string) error
}

// ErrOutOfScope reports a target that is neither the caller nor one of its
// descendants.
var ErrOutOfScope = errors.New("agent is outside the caller's subtree")

// ErrClosed reports a spawn after the runtime was closed.
var ErrClosed = errors.New("agent runtime is closed")

// ErrSpawnReplayMismatch reports a spawn whose (TaskID, CallID) was already
// used by a different request (caller, plan entry or assignment differ).
var ErrSpawnReplayMismatch = errors.New("spawn call ID was already used for a different request")

// checkReplay verifies that a replayed spawn matches the stored agent, so an
// idempotency-key collision never silently returns an unrelated agent.
func checkReplay(existing protocol.RuntimeAgent, req SpawnRequest) error {
	if existing.ParentAgentID != req.CallerAgentID {
		return fmt.Errorf("%w: agent %s was spawned by caller %q, not %q", ErrSpawnReplayMismatch, existing.ID, existing.ParentAgentID, req.CallerAgentID)
	}
	if existing.PlanEntry != req.PlanEntry {
		return fmt.Errorf("%w: agent %s was spawned from plan entry %q, not %q", ErrSpawnReplayMismatch, existing.ID, existing.PlanEntry, req.PlanEntry)
	}
	stored, err := assignmentHash(existing.Assignment)
	if err != nil {
		return err
	}
	asked, err := assignmentHash(req.Assignment)
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, asked) {
		return fmt.Errorf("%w: agent %s was spawned with a different assignment", ErrSpawnReplayMismatch, existing.ID)
	}
	return nil
}

func assignmentHash(a protocol.AgentAssignment) ([]byte, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("spawn: encode assignment: %w", err)
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

// Option configures a Runtime.
type Option func(*Runtime)

// WithResolveOptions sets the capability resolution options used by Admit.
func WithResolveOptions(o ResolveOptions) Option {
	return func(r *Runtime) { r.resolve = o }
}

// WithResolveOptionsFunc computes the resolution options per task at each
// admission, such as the shell confinement the task's project would use. It
// takes precedence over WithResolveOptions. The function runs under the
// runtime's spawn lock, so it must not call back into the runtime.
func WithResolveOptionsFunc(fn func(ctx context.Context, taskID string) ResolveOptions) Option {
	return func(r *Runtime) { r.resolveFn = fn }
}

// WithLeadCapabilities sets the lead's authority used when the lead (caller
// "") spawns. The default is RootCapabilities().
func WithLeadCapabilities(c protocol.AgentCapabilities) Option {
	return func(r *Runtime) { r.lead = c }
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
	// resolveFn, when set, supplies the options per task instead of resolve.
	resolveFn func(ctx context.Context, taskID string) ResolveOptions
	events    func(protocol.RuntimeAgent)
	lead      protocol.AgentCapabilities

	// lifecycle serialises Spawn so admission, reservation and creation are
	// atomic with respect to each other.
	lifecycle sync.Mutex
	closed    bool // guarded by lifecycle
}

// NewRuntime returns a Runtime whose agents run under contexts derived from
// base (see NewScheduler).
func NewRuntime(s AgentStore, r Runner, base context.Context, opts ...Option) *Runtime {
	rt := &Runtime{store: s, lead: RootCapabilities()}
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

// Close stops admitting spawns (later Spawns fail with ErrClosed), cancels
// every queued and running agent and waits for their runners to return until
// ctx ends, in which case it returns ctx.Err(). It is idempotent: a second
// call has nothing left to cancel and returns once the runners are gone.
func (r *Runtime) Close(ctx context.Context) error {
	// Taking the lifecycle lock makes the closed flag atomic with any Spawn
	// already past admission, so its agent is submitted before we cancel.
	r.lifecycle.Lock()
	r.closed = true
	r.lifecycle.Unlock()
	return r.sched.Close(ctx)
}

// Update applies mutate to an agent from outside the scheduler, such as the
// runner recording its model, workspace or usage. Unlike a direct store write
// it wakes Wait and reports the change through WithEvents. mutate returns
// false to skip the write.
func (r *Runtime) Update(ctx context.Context, taskID, id string, mutate func(*protocol.RuntimeAgent) bool) error {
	return r.sched.Update(ctx, taskID, id, mutate)
}

// Spawn admits and starts (or queues) an agent. It is idempotent by
// (TaskID, CallID): a repeated call returns the stored agent without
// scheduling it again, provided caller, plan entry and assignment match the
// stored agent (else ErrSpawnReplayMismatch). CallID must be unique per
// distinct spawn within the task; the engine derives it from the turn, the
// provider call ID and its per-turn occurrence.
//
// Spawn holds the lifecycle lock while it submits to the scheduler, so the
// events callback (WithEvents) and the scheduler's onChange must not call
// back into Spawn synchronously.
func (r *Runtime) Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error) {
	if req.TaskID == "" || req.CallID == "" {
		return SpawnResult{}, errors.New("spawn: task ID and call ID are required")
	}
	res, created, err := r.spawnLocked(ctx, req)
	if err != nil {
		return SpawnResult{}, err
	}
	// Events go out after the lock is released and the agent is submitted.
	if created {
		r.emit(res.Agent)
	}
	return res, nil
}

// spawnLocked runs Spawn under the lifecycle lock; created reports whether a
// new agent was stored and submitted (false for a replay).
func (r *Runtime) spawnLocked(ctx context.Context, req SpawnRequest) (SpawnResult, bool, error) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if r.closed {
		return SpawnResult{}, false, ErrClosed
	}

	// Only protocol.ErrNotFound means "not spawned yet"; any other store
	// error is returned rather than risking a second admission.
	existing, err := r.store.RuntimeAgentForCall(ctx, req.TaskID, req.CallID)
	if err == nil {
		if err := checkReplay(existing, req); err != nil {
			return SpawnResult{}, false, err
		}
		return SpawnResult{Agent: existing, Granted: existing.Capabilities, Rejected: existing.Rejected}, false, nil
	}
	if !errors.Is(err, protocol.ErrNotFound) {
		return SpawnResult{}, false, fmt.Errorf("spawn: look up call %s: %w", req.CallID, err)
	}

	plan, err := r.store.ApprovedAgentPlanForTask(ctx, req.TaskID)
	if errors.Is(err, protocol.ErrNotFound) {
		return SpawnResult{}, false, &AdmissionError{Field: "plan", Message: "the task has no approved plan; propose a plan and get it approved before spawning agents"}
	}
	if err != nil {
		return SpawnResult{}, false, fmt.Errorf("spawn: load approved plan: %w", err)
	}

	all, err := r.store.RuntimeAgents(ctx, req.TaskID)
	if err != nil {
		return SpawnResult{}, false, fmt.Errorf("spawn: list agents: %w", err)
	}
	ledger := RebuildLedger(all)

	caller := protocol.RuntimeAgent{Capabilities: r.lead, Depth: 0, BudgetShare: 1.0}
	if req.CallerAgentID != "" {
		caller, err = r.store.RuntimeAgent(ctx, req.TaskID, req.CallerAgentID)
		if errors.Is(err, protocol.ErrNotFound) {
			return SpawnResult{}, false, &AdmissionError{Field: "caller", Message: fmt.Sprintf("caller agent %q does not exist in this task", req.CallerAgentID)}
		}
		if err != nil {
			return SpawnResult{}, false, fmt.Errorf("spawn: load caller %s: %w", req.CallerAgentID, err)
		}
		if isTerminal(caller.State) {
			return SpawnResult{}, false, &AdmissionError{Field: "caller", Message: fmt.Sprintf("caller agent %q is %s and may not spawn", caller.ID, caller.State)}
		}
	}

	options := r.resolve
	if r.resolveFn != nil {
		options = r.resolveFn(ctx, req.TaskID)
	}
	adm, err := Admit(AdmissionInput{
		Plan:            plan,
		Caller:          caller,
		All:             all,
		ParentShareFree: ledger.Free(caller.ID, caller.BudgetShare),
		Options:         options,
		Request:         req,
	})
	if err != nil {
		return SpawnResult{}, false, err
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
		PlanEntry:      adm.Entry.Name,
		Assignment:     req.Assignment,
		Capabilities:   adm.Granted,
		Rejected:       adm.Rejected,
		ModelRequested: adm.Model,
		BudgetShare:    adm.Share,
		Depth:          adm.Depth,
		ContextMode:    req.ContextMode,
		State:          protocol.AgentStateQueued,
	}, req.CallID)
	if err != nil {
		return SpawnResult{}, false, fmt.Errorf("spawn: create agent: %w", err)
	}

	maxConc := plan.Limits.Normalized().MaxConcurrent
	// The agent is stored; a dying caller ctx must not abandon its scheduling.
	persist := context.WithoutCancel(ctx)
	r.sched.Submit(persist, created, maxConc)

	res := SpawnResult{Agent: created, Granted: adm.Granted, Rejected: adm.Rejected}
	if cur, err := r.store.RuntimeAgent(persist, req.TaskID, created.ID); err == nil {
		res.Agent = cur
	}
	if r.sched.Queued(req.TaskID, created.ID) {
		res.QueuedReason = fmt.Sprintf("capacity: %d/%d running", r.sched.Active(req.TaskID), maxConc)
	}
	return res, true, nil
}

// scope loads the task's agents and checks that every target is callerID or
// one of its descendants; the lead ("") may target any agent of the task.
// An unknown target or caller is a not-found error.
func (r *Runtime) scope(ctx context.Context, taskID, callerID string, targets ...string) error {
	all, err := r.store.RuntimeAgents(ctx, taskID)
	if err != nil {
		return err
	}
	parent := make(map[string]string, len(all))
	for _, a := range all {
		parent[a.ID] = a.ParentAgentID
	}
	if callerID != "" {
		if _, ok := parent[callerID]; !ok {
			return fmt.Errorf("caller agent %s: %w", callerID, protocol.ErrNotFound)
		}
	}
	for _, id := range targets {
		if _, ok := parent[id]; !ok {
			return fmt.Errorf("agent %s: %w", id, protocol.ErrNotFound)
		}
		if callerID != "" && !descendsFrom(parent, id, callerID) {
			return fmt.Errorf("agent %s: %w of %s", id, ErrOutOfScope, callerID)
		}
	}
	return nil
}

// descendsFrom reports whether id is ancestor or one of its descendants.
func descendsFrom(parent map[string]string, id, ancestor string) bool {
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; cur = parent[cur] {
		if cur == ancestor {
			return true
		}
		seen[cur] = true
	}
	return false
}

// List returns the task's agents in creation order: all of them for the lead,
// the caller's descendants (excluding itself) otherwise.
func (r *Runtime) List(ctx context.Context, taskID, callerID string) ([]protocol.RuntimeAgent, error) {
	all, err := r.store.RuntimeAgents(ctx, taskID)
	if err != nil || callerID == "" {
		return all, err
	}
	parent := make(map[string]string, len(all))
	for _, a := range all {
		parent[a.ID] = a.ParentAgentID
	}
	if _, ok := parent[callerID]; !ok {
		return nil, fmt.Errorf("caller agent %s: %w", callerID, protocol.ErrNotFound)
	}
	out := make([]protocol.RuntimeAgent, 0)
	for _, a := range all {
		if a.ID != callerID && descendsFrom(parent, a.ID, callerID) {
			out = append(out, a)
		}
	}
	return out, nil
}

// Status returns one agent of the task within the caller's scope.
func (r *Runtime) Status(ctx context.Context, taskID, callerID, id string) (protocol.RuntimeAgent, error) {
	if err := r.scope(ctx, taskID, callerID, id); err != nil {
		return protocol.RuntimeAgent{}, fmt.Errorf("status: %w", err)
	}
	a, err := r.store.RuntimeAgent(ctx, taskID, id)
	if err != nil {
		return protocol.RuntimeAgent{}, fmt.Errorf("agent %s: %w", id, err)
	}
	return a, nil
}

// Wait blocks until any (WaitAny) or all (WaitAll) of the listed agents are
// terminal, or ctx is done, and returns their current states in ids order.
// Every id must be within the caller's scope.
// Waiters are woken only by scheduler-persisted transitions; writers outside
// the scheduler (such as Recover's reconciliation) do not wake them.
func (r *Runtime) Wait(ctx context.Context, taskID, callerID string, ids []string, mode WaitMode) ([]protocol.RuntimeAgent, error) {
	if len(ids) == 0 {
		return nil, errors.New("wait: no agent ids given")
	}
	if mode != WaitAny && mode != WaitAll {
		return nil, fmt.Errorf("wait: unknown mode %q", mode)
	}
	if err := r.scope(ctx, taskID, callerID, ids...); err != nil {
		return nil, fmt.Errorf("wait: %w", err)
	}
	for {
		// Take the channel before reading state so no change is missed.
		changed, release := r.sched.watch(taskID)
		agents := make([]protocol.RuntimeAgent, len(ids))
		done := 0
		for i, id := range ids {
			a, err := r.store.RuntimeAgent(ctx, taskID, id)
			if err != nil {
				release()
				return nil, fmt.Errorf("wait: agent %s: %w", id, err)
			}
			agents[i] = a
			if isTerminal(a.State) {
				done++
			}
		}
		if (mode == WaitAny && done > 0) || (mode == WaitAll && done == len(ids)) {
			release()
			return agents, nil
		}
		select {
		case <-changed:
			release()
		case <-ctx.Done():
			release()
			return agents, ctx.Err()
		}
	}
}

// Message queues a message for a live agent of the task within the sender's
// scope. fromID "" is the lead. A non-empty callID makes the message
// idempotent per (task, callID): a retry returns the stored message instead
// of queuing a duplicate.
func (r *Runtime) Message(ctx context.Context, taskID, fromID, toID, body, callID string) (protocol.AgentMessage, error) {
	if err := r.scope(ctx, taskID, fromID, toID); err != nil {
		return protocol.AgentMessage{}, fmt.Errorf("message: %w", err)
	}
	to, err := r.store.RuntimeAgent(ctx, taskID, toID)
	if err != nil {
		return protocol.AgentMessage{}, fmt.Errorf("message: recipient %s: %w", toID, err)
	}
	if isTerminal(to.State) {
		return protocol.AgentMessage{}, fmt.Errorf("message: recipient %s is %s", toID, to.State)
	}
	return r.store.EnqueueAgentMessage(ctx, protocol.AgentMessage{TaskID: taskID, FromAgentID: fromID, ToAgentID: toID, Body: body, CallID: callID})
}

// Cancel cancels an agent within the caller's scope and its descendants.
// Their shares are free from the next Spawn on, since the ledger is rebuilt
// from stored agents.
func (r *Runtime) Cancel(ctx context.Context, taskID, callerID, id string) error {
	if err := r.scope(ctx, taskID, callerID, id); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	return r.sched.Cancel(ctx, taskID, id)
}
