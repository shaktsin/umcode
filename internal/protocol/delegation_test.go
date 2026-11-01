package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentSnapshotRuntimeFieldsOmittedForLegacy(t *testing.T) {
	b, err := json.Marshal(AgentSnapshot{Task: AgentTask{ID: "t"}, Children: []AgentChild{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"agents"`, `"plan"`, `"integration"`} {
		if strings.Contains(string(b), key) {
			t.Fatalf("legacy snapshot carries %s: %s", key, b)
		}
	}
	b, err = json.Marshal(DelegationUpdated{ThreadID: "th", TaskID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"agent"`, `"integration"`} {
		if strings.Contains(string(b), key) {
			t.Fatalf("legacy update carries %s: %s", key, b)
		}
	}
}

func TestAgentSnapshotRuntimeFieldsJSON(t *testing.T) {
	snap := AgentSnapshot{
		Task:   AgentTask{ID: "t"},
		Agents: []RuntimeAgent{{ID: "a1", ModelRequested: "claude/x", ModelActual: "claude/y", RouteTrail: []string{"claude/x: unavailable"}}},
		Plan:   &AgentPlan{ID: "p", State: PlanApproved},
		Integration: &AgentIntegrationStatus{HeadTree: "tree", AppliedCount: 1, Verified: true, Amendments: 1, AmendReason: "fix", Blocked: "stuck",
			Revisions: []AgentRevisionView{{AgentID: "a1", State: "conflict", Conflict: "a.go"}}},
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err = json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["agents"].([]any); !ok {
		t.Fatalf("agents missing: %s", b)
	}
	if plan, ok := raw["plan"].(map[string]any); !ok || plan["state"] != "approved" {
		t.Fatalf("plan = %v", raw["plan"])
	}
	in, ok := raw["integration"].(map[string]any)
	if !ok {
		t.Fatalf("integration missing: %s", b)
	}
	for key, want := range map[string]any{"headTree": "tree", "appliedCount": float64(1), "verified": true, "amendments": float64(1), "amendReason": "fix", "blocked": "stuck"} {
		if in[key] != want {
			t.Fatalf("integration[%s] = %v, want %v (%s)", key, in[key], want, b)
		}
	}
	revs, _ := in["revisions"].([]any)
	if len(revs) != 1 {
		t.Fatalf("revisions = %v", in["revisions"])
	}
	rev := revs[0].(map[string]any)
	if rev["agentId"] != "a1" || rev["state"] != "conflict" || rev["conflict"] != "a.go" {
		t.Fatalf("revision = %v", rev)
	}

	b, err = json.Marshal(DelegationUpdated{ThreadID: "th", TaskID: "t", Agent: &RuntimeAgent{ID: "a1", State: AgentStateCompleted}, Integration: &AgentIntegrationStatus{HeadTree: "tree"}, Sequence: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"agent":{"id":"a1"`) || !strings.Contains(string(b), `"integration":{"headTree":"tree"`) {
		t.Fatalf("update = %s", b)
	}
}
