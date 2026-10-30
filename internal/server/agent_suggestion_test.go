package server_test

import (
	"testing"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func TestSuggestionRespondAdminOnly(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Agents.RuntimeV2 = true })
	commitDelegationFixture(t, h)
	h.addKey("claude", "test", "fake")
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "suggest"}, &th)
	sug, err := h.eng.Store.CreateAgentSuggestion(h.ctx, store.AgentSuggestion{ThreadID: th.ID, TurnID: "turn-1", Reason: "parallel work"})
	if err != nil {
		t.Fatal(err)
	}
	nonadmin, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "tool-client", false)
	if err != nil {
		t.Fatal(err)
	}
	defer nonadmin.Close()
	for _, d := range []string{protocol.SuggestionEnable, protocol.SuggestionDecline} {
		if err = nonadmin.Call(h.ctx, protocol.MethodAgentSuggestionRespond, protocol.AgentSuggestionRespondParams{SuggestionID: sug.ID, Decision: d}, nil); err == nil {
			t.Fatalf("non-app %s accepted", d)
		}
	}
	got, err := h.eng.Store.GetAgentSuggestion(h.ctx, sug.ID)
	if err != nil || got.State != store.SuggestionPending {
		t.Fatalf("non-admin changed state: %+v %v", got, err)
	}
	var res protocol.AgentSuggestionRespondResult
	h.call(protocol.MethodAgentSuggestionRespond, protocol.AgentSuggestionRespondParams{SuggestionID: sug.ID, Decision: protocol.SuggestionEnable}, &res)
	if res.Suggestion.State != store.SuggestionEnabled || res.ActivationToken == "" || res.ExpiresAt == nil {
		t.Fatalf("admin enable: %+v", res)
	}
	h.fake.push(textReply("done"))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "go", DelegationActivationToken: res.ActivationToken}, &started)
	h.waitTurn(started.Turn.ID, nil)
	if _, err = h.eng.Store.AgentTaskForRootTurn(h.ctx, th.ID, started.Turn.ID); err != nil {
		t.Fatalf("follow-up turn not admitted: %v", err)
	}
}
