package server_test

import (
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestDelegationRPCUnavailableRouting(t *testing.T) {
	h := newHarness(t, nil)
	commitDelegationFixture(t, h)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "no keys"}, &th)
	var enabled protocol.DelegationEnableResult
	h.call(protocol.MethodDelegationEnable, protocol.ThreadIDParams{ThreadID: th.ID}, &enabled)
	h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: enabled.ActivationToken})
	var snapshot protocol.DelegationListResult
	h.call("delegation/list", protocol.DelegationListParams{ThreadID: th.ID}, &snapshot)
	h.fake.mu.Lock()
	calls := len(h.fake.calls)
	h.fake.mu.Unlock()
	if calls != 0 || len(snapshot.Tasks) != 0 {
		t.Fatalf("unavailable routing executed/admitted: calls=%d tasks=%+v", calls, snapshot.Tasks)
	}
}
