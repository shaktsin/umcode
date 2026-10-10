package server_test

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/work"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenOptimizationRPC(t *testing.T) {
	h := newHarness(t, nil)
	var r protocol.TokenOptimizationResult
	h.call(protocol.MethodTokenOptimizationGet, struct{}{}, &r)
	if r.Enabled || r.Source != "default" {
		t.Fatalf("default %+v", r)
	}
	for _, on := range []bool{true, false} {
		h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: on}, &r)
		h.call(protocol.MethodTokenOptimizationGet, struct{}{}, &r)
		if r.Enabled != on || r.Source != "stored" || r.LegacyMixed {
			t.Fatalf("round trip %+v", r)
		}
	}
	if err := h.callErr(protocol.MethodTokenOptimizationSet, map[string]any{"enabled": "yes"}); err == nil {
		t.Fatal("invalid boolean accepted")
	}
	for _, invalid := range []map[string]any{{"enabled": nil}, {}, {"enabled": true, "contextCompiler": false}} {
		if err := h.callErr(protocol.MethodTokenOptimizationSet, invalid); err == nil {
			t.Fatalf("invalid settings accepted %+v", invalid)
		}
	}

}

func TestOptimizationBundleAcrossChats(t *testing.T) {
	h := newHarness(t, nil)
	log := captureWorkflowRequests(h)
	h.addKey("claude", "bundle", "sk-1")
	var saved protocol.TokenOptimizationResult
	h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: true}, &saved)
	if err := os.WriteFile(filepath.Join(h.ws, "go.mod"), []byte("module acceptance\n\ngo 1.24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "notes_test.go"), []byte("package acceptance\nimport(\"os\";\"testing\")\nfunc TestNotes(t *testing.T){b,e:=os.ReadFile(\"notes.txt\");if e!=nil||string(b)!=\"verified notes\\n\"{t.Fatal(\"notes missing\")}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "bundle"}, &th)
	h.call(protocol.MethodThreadSetSettings, protocol.ThreadSetSettingsParams{ThreadID: th.ID, Settings: protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-5", Complexity: "standard"}}, &th)
	h.fake.push(toolReply("verification__plan", `{}`), textReply("Inspected."))
	if turn := runWorkTurn(h, th, "design a new architecture for verified notes"); turn.Status != protocol.TurnCompleted {
		t.Fatal(turn)
	}

	h.fake.push(
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			r := solutionBatch(t, d, true, "")
			r.Nodes[1].ToStatus = "approved"
			return r
		}),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return taskTransition(t, d, "Write notes", "in_progress")
		}),
		toolReply("file__write", `{"path":"notes.txt","content":"verified notes\n"}`),
		toolReply("verification__run", `{"checks":[{"label":"go test","command":"go test ./...","reason":"Acceptance"},{"label":"go vet","command":"go vet ./...","reason":"Static checks"}]}`),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			r := taskTransition(t, d, "Write notes", "completed")
			for _, n := range d.Nodes {
				if n.Kind != "fact" {
					continue
				}
				var fact work.VerifiedCommandFact
				if json.Unmarshal(n.Content, &fact) != nil || fact.Command != "go test ./..." {
					continue
				}
				content, _ := json.Marshal(work.MemoryCandidateContent{Category: "command", SemanticKey: "test-command", Text: "Use go test ./... for this repository", SourceRevision: fact.SourceRevision})
				r.Nodes = append(r.Nodes, protocol.WorkNodeChange{Ref: "candidate", Kind: "memory_candidate", Title: "Verified test command", Content: content, EvidenceIDs: []string{fact.EvidenceID}})
				r.Edges = append(r.Edges, protocol.WorkEdgeChange{From: "candidate", To: n.ID, Relation: "candidate_for"})
				break
			}
			return r
		}), textReply("Verified."),
	)
	actionApprovals := 0
	turn, items := workflowTurn(h, th, func(a protocol.Approval) bool {
		if a.Kind == "workflow" {
			t.Error("optimization added a workflow prompt")
		} else {
			actionApprovals++
		}
		return true
	})
	d := workflowDetail(h, th.ID)
	if turn.Status != protocol.TurnCompleted || d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("turn=%+v work=%+v items=%+v", turn, d.Work, items)
	}
	for _, it := range items {
		if it.Status != protocol.ItemCompleted {
			t.Fatalf("tool failed %+v", it.Tool)
		}
	}
	if actionApprovals != 1 {
		t.Fatalf("normal action approval count=%d", actionApprovals)
	}
	if n := workflowNode(t, d, "decision", ""); n.DecisionActor != "agent" {
		t.Fatalf("decision provenance %+v", n)
	}
	data, err := os.ReadFile(filepath.Join(h.ws, "notes.txt"))
	if err != nil || string(data) != "verified notes\n" {
		t.Fatalf("artifact %q %v", data, err)
	}
	rows, err := h.eng.Store.ListProjectMemories(t.Context(), h.proj.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("automatic memory=%+v %v", rows, err)
	}
	compiled, retrieved, selected := false, false, false
	for _, b := range log.breakdowns(t) {
		compiled = compiled || b.WorkPacketTokens > 0
		retrieved = retrieved || b.RetrievalPacketTokens > 0
		selected = selected || b.ToolSelection != nil
	}
	if !compiled || !retrieved || !selected {
		t.Fatalf("missing active components: compiler=%v retrieval=%v selection=%v", compiled, retrieved, selected)
	}
}

func TestOptimizationTurnAdmissionSnapshot(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "snapshot", "sk-1")
	var saved protocol.TokenOptimizationResult
	h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: true}, &saved)
	th := startWorkThread(h)
	started := make(chan struct{})
	release := make(chan struct{})
	hasWork := func(req llm.Request) bool {
		for _, tool := range req.Tools {
			if tool.Name == "work__update" {
				return true
			}
		}
		return false
	}
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		if !hasWork(req) {
			t.Error("master-on missing workflow tool")
		}
		close(started)
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			t.Error("snapshot barrier timeout")
		}
		return toolReply("file__list", `{}`)(req, key)
	}, func(req llm.Request, key string) ([]llm.Event, error) {
		if !hasWork(req) {
			t.Error("active turn changed policy")
		}
		return textReply("Listed.")(req, key)
	})
	var first protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "list files"}, &first)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not start")
	}
	h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: false}, &saved)
	close(release)
	if turn, _, _ := h.waitTurn(first.Turn.ID, nil); turn.Status != protocol.TurnCompleted {
		t.Fatal(turn)
	}
	other := startWorkThread(h)
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		if hasWork(req) {
			t.Error("new off turn retained workflow tool")
		}
		for _, m := range req.Messages {
			for _, part := range m.Parts {
				if strings.Contains(part.Text, "Automatic workflow:") {
					t.Error("off turn retained workflow instructions")
				}
			}
		}
		return textReply("Hello.")(req, key)
	})
	if turn := runWorkTurn(h, other, "hello"); turn.Status != protocol.TurnCompleted {
		t.Fatal(turn)
	}
	var enabled bool
	if err := h.eng.Store.GetSetting(t.Context(), "token_optimization.enabled", &enabled); err != nil || enabled {
		t.Fatalf("saved state %v %v", enabled, err)
	}
}

func TestOptimizationPreservesActionDenial(t *testing.T) {
	h, th, _ := workflowFixture(t, "write notes and verify", true, false)
	var setting protocol.TokenOptimizationResult
	h.call(protocol.MethodTokenOptimizationSet, protocol.TokenOptimizationParams{Enabled: true}, &setting)
	h.fake.push(workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, false, "") }), workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
		return taskTransition(t, d, "Write notes", "in_progress")
	}), toolReply("shell__run", `{"command":"printf denied > denied.txt"}`), textReply("The action was denied."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "save notes"}, &started)
	prompts := 0
	turn, _, items := h.waitTurn(started.Turn.ID, func(a protocol.Approval) bool {
		prompts++
		if a.Kind == "workflow" {
			t.Error("extra internal workflow prompt")
		}
		return false
	})
	if turn.Status != protocol.TurnCompleted || prompts != 1 {
		t.Fatalf("denial turn=%+v prompts=%d", turn, prompts)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied action wrote artifact %v", err)
	}
	found := false
	for _, it := range items {
		if it.Tool != nil && it.Tool.Name == "shell.run" && it.Status == protocol.ItemDenied {
			found = true
		}
	}
	if !found {
		t.Fatal("action denial not recorded")
	}
}
