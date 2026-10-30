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
	ts := NewRuntimeActivationTools(RuntimeActivation{Suggest: suggest, Caller: caller})
	if len(ts) != 1 || ts[0].Name() != "agent.suggest" {
		t.Fatalf("activation tools = %v", ts)
	}
	return ts[0]
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
	if !strings.Contains(tool.Description(), "parallel") {
		t.Fatalf("description must limit suggestions to parallel or independent work: %q", tool.Description())
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
