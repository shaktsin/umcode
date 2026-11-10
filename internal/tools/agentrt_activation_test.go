package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
)

func suggestTool(t *testing.T, suggest func(context.Context, string) (string, error), caller func(context.Context) (string, bool)) Tool {
	t.Helper()
	return activationTool(t, RuntimeActivation{Suggest: suggest, Caller: caller}, "agent.suggest")
}

// activationTool returns one tool of the single activation constructor,
// which returns exactly agent.suggest and agent.propose_plan.
func activationTool(t *testing.T, a RuntimeActivation, name string) Tool {
	t.Helper()
	ts := NewRuntimeActivationTools(a)
	var names []string
	var found Tool
	for _, tool := range ts {
		names = append(names, tool.Name())
		if tool.Name() == name {
			found = tool
		}
	}
	if !slices.Equal(names, []string{"agent.suggest", "agent.propose_plan"}) || found == nil {
		t.Fatalf("activation tools = %v", names)
	}
	return found
}

func TestProposePlanToolLeadOnlyAndStrict(t *testing.T) {
	called := false
	propose := func(context.Context, PlanProposal) (string, error) { called = true; return "{}", nil }
	args := json.RawMessage(`{"agents":[{"name":"w","purpose":"p","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`)
	for name, caller := range map[string]func(context.Context) (string, bool){
		"runtime agent": func(context.Context) (string, bool) { return "a1", true },
		"unresolved":    func(context.Context) (string, bool) { return "", false },
		"nil":           nil,
	} {
		tool := activationTool(t, RuntimeActivation{ProposePlan: propose, Caller: caller}, "agent.propose_plan")
		if _, err := tool.Call(t.Context(), args); err == nil || !strings.Contains(err.Error(), "lead-only") {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	lead := func(context.Context) (string, bool) { return "", true }
	tool := activationTool(t, RuntimeActivation{ProposePlan: propose, Caller: lead}, "agent.propose_plan")
	ro := WithScope(t.Context(), &Scope{ReadOnly: true})
	if _, err := tool.Call(ro, args); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("read-only scope: %v", err)
	}
	if called {
		t.Fatal("a refused caller reached the engine")
	}
	var got PlanProposal
	tool = activationTool(t, RuntimeActivation{ProposePlan: func(_ context.Context, p PlanProposal) (string, error) { got = p; return "{}", nil }, Caller: lead}, "agent.propose_plan")
	if _, err := tool.Call(t.Context(), json.RawMessage(`{"agents":[{"name":" w ","purpose":" p ","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":1}}],"limits":{"maxConcurrent":3}}`)); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 || got.Agents[0].Name != "w" || got.Agents[0].Purpose != "p" || got.Agents[0].Capabilities.SpawnMax != 1 || got.Limits != (protocol.PlanLimits{MaxConcurrent: 3, MaxDepth: 3, MaxTotal: 40, SliceShare: 0.25}) {
		t.Fatalf("parsed %+v", got)
	}
	all := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true, SpawnMax: 8}
	if slices.Contains(agentrt.AllowedTools(all), "agent.propose_plan") {
		t.Fatal("agent.propose_plan must never be granted by capabilities")
	}
}

func TestSuggestToolValidatesReason(t *testing.T) {
	var got []string
	tool := suggestTool(t, func(_ context.Context, reason string) (string, error) {
		got = append(got, reason)
		return `{"suggestionId":"s1","state":"pending"}`, nil
	}, func(context.Context) (string, bool) { return "", true })
	for _, bad := range []string{`{}`, `{"reason":""}`, `{"reason":"   "}`, `{"reason":"` + strings.Repeat("é", 281) + `"}`, `{"reason":"x","extra":1}`} {
		if _, err := tool.Call(t.Context(), json.RawMessage(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if len(got) != 0 {
		t.Fatalf("invalid reasons reached the engine: %q", got)
	}
	out, err := tool.Call(t.Context(), json.RawMessage(`{"reason":"`+strings.Repeat("é", 280)+`"}`))
	if err != nil || !strings.Contains(out, "s1") || len(got) != 1 {
		t.Fatalf("valid reason: %q %v", out, err)
	}
}

func TestSuggestToolLeadOnlyCaller(t *testing.T) {
	called := false
	for name, caller := range map[string]func(context.Context) (string, bool){
		"runtime agent": func(context.Context) (string, bool) { return "a1", true },
		"unresolved":    func(context.Context) (string, bool) { return "", false },
		"nil":           nil,
	} {
		tool := suggestTool(t, func(context.Context, string) (string, error) { called = true; return "{}", nil }, caller)
		_, err := tool.Call(t.Context(), json.RawMessage(`{"reason":"split the work"}`))
		if err == nil || !strings.Contains(err.Error(), "lead-only") {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if called {
		t.Fatal("a non-lead caller reached the engine")
	}
	all := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true, SpawnMax: 8}
	if slices.Contains(agentrt.AllowedTools(all), "agent.suggest") {
		t.Fatal("agent.suggest must never be granted by capabilities")
	}
}
