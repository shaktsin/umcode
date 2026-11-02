package engine

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// A read-only runtime agent (files:read) keeps the tools its capabilities
// grant that change no files: its confined read-only shell and the agent.*
// control tools. The lead chat's active task must not refuse them as writes;
// file writes stay refused.
func TestRuntimeReadOnlyAgentKeepsGrantedNonWriteTools(t *testing.T) {
	limits := protocol.DefaultPlanLimits()
	reader := protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadOnly, SpawnMax: 1}
	x := newRuntimeE2EOpts(t, runtimeE2EOpts{limits: &limits, entries: []protocol.PlanAgent{
		{Name: "reader", Purpose: "look around", Capabilities: reader},
	}},
		fakeStep{Agent: "reader", Tool: "agent.list", Args: `{}`},
		fakeStep{Agent: "reader", Tool: "shell.run", Args: `{"command":"ls"}`},
		fakeStep{Agent: "reader", Tool: "file.write", Args: `{"path":"x.go","content":"package x\n"}`},
		fakeStep{Agent: "reader", Text: "looked"},
	)
	id := x.spawn(t, "reader")
	if a := waitAgentState(t, x.e, x.task.ID, id, protocol.AgentStateCompleted); a.Capabilities.Shell != protocol.ShellReadOnly || a.Capabilities.SpawnMax != 1 {
		t.Fatalf("reader granted %+v", a.Capabilities)
	}
	results := map[string]string{}
	reqs, ids := x.p.snapshot()
	for i, r := range reqs {
		if ids[i] != id {
			continue
		}
		for _, m := range r.Messages {
			if m.Role == llm.RoleTool {
				results[m.ToolCallID] = m.Result
			}
		}
	}
	for _, call := range []string{"reader-0", "reader-1"} {
		if got, ok := results[call]; !ok || strings.Contains(got, "read-only") {
			t.Fatalf("%s result %q (seen %v): a granted non-write tool was refused", call, got, ok)
		}
	}
	if got := results["reader-2"]; !strings.Contains(got, "not granted") {
		t.Fatalf("file.write result %q, want refused", got)
	}
}
