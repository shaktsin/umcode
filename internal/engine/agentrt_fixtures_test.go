package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// Shared fixtures of the agent runtime tests.

type childEvent struct {
	method string
	params any
}
type childSubscriber struct{ events chan childEvent }

func (*childSubscriber) ID() string                         { return "child-execution" }
func (*childSubscriber) IsAdmin() bool                      { return true }
func (*childSubscriber) Wants(string) bool                  { return true }
func (s *childSubscriber) Notify(method string, params any) { s.events <- childEvent{method, params} }

func childExecutionTask(t *testing.T, e *Engine, th protocol.Thread) protocol.AgentTask {
	t.Helper()
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	params := protocol.TurnStartParams{ThreadID: th.ID, Text: "parent private unrelated transcript", DelegationActivationToken: token.ActivationToken}
	var tr protocol.Turn
	if e.AgentRuntime != nil {
		// The agent runtime closes a task whose admitted turn ends without a
		// plan: the test's plan and agents belong to the live admitted turn.
		tr, _ = liveAdmittedTurn(t, e, params)
	} else {
		tr = delegationTurn(t, e, params)
	}
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// planKeeper wraps a test provider: on every model call of an admitted lead
// turn whose task has no approved plan yet it approves one (approvePlanFor),
// as the user's plan approval would. Tests of the shared task budget, cancel
// and compaction then reuse the task across follow-up turns: the runtime
// closes a task whose admitted turn ends without a plan.
type planKeeper struct {
	llm.Provider
	t *testing.T
	e *Engine
}

func keepTasksOpen(t *testing.T, e *Engine, p llm.Provider) llm.Provider {
	return &planKeeper{Provider: p, t: t, e: e}
}

func (k *planKeeper) Stream(ctx context.Context, cred llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if task, ok := DelegationTaskFromContext(ctx); ok && task.ID != "" && childExecutionFrom(ctx) == nil {
		sctx := context.WithoutCancel(ctx)
		if _, err := k.e.Store.ApprovedAgentPlanForTask(sctx, task.ID); errors.Is(err, store.ErrNotFound) {
			if err = approvePlan(sctx, k.e, task); err != nil {
				k.t.Errorf("approve plan for %s: %v", task.ID, err)
			}
		}
	}
	return k.Provider.Stream(ctx, cred, req)
}
