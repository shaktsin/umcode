package engine

import (
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
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
