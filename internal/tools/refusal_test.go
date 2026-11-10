package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The agent tool rule refusals are typed, so callers recognise them by
// identity, never by their wording.
func TestAgentRuleRefusalsAreTyped(t *testing.T) {
	agent := func(context.Context) (string, bool) { return "a1", true }
	args := json.RawMessage(`{"agents":[{"name":"w","purpose":"p","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`)
	_, err1 := activationTool(t, RuntimeActivation{ProposePlan: func(context.Context, PlanProposal) (string, error) { return "", nil }, Caller: agent}, "agent.propose_plan").Call(t.Context(), args)
	_, err2 := suggestTool(t, func(context.Context, string) (string, error) { return "", nil }, agent).Call(t.Context(), json.RawMessage(`{"reason":"r"}`))
	_, err3 := ParsePlanProposal(json.RawMessage(`{"agents":[]}`))
	for i, err := range []error{err1, err2, err3} {
		var r *RuleRefusal
		if !errors.As(err, &r) || r.NextStep == "" || r.Advisory {
			t.Fatalf("refusal %d: %v (%T), want a non-advisory RuleRefusal with a next step", i, err, err)
		}
	}
	var r *RuleRefusal
	if errors.As(err3, &r); !strings.Contains(r.NextStep, "1 to 12") {
		t.Fatalf("plan size next step %q", r.NextStep)
	}
	// Wording alone is not a refusal.
	if errors.As(errors.New(err1.Error()), &r) {
		t.Fatal("an untyped error with refusal wording was recognised")
	}
}
