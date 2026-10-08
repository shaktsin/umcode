package ctxcompiler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

var base = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func crit(id, title, command, status string) protocol.WorkNode {
	content, _ := json.Marshal(map[string]string{"command": command})
	return protocol.WorkNode{ID: id, Kind: protocol.NodeCriterion, Title: title, Content: content, Status: status}
}

func designedInput(d protocol.WorkDetail) Input {
	return Input{Detail: d, DesignedWorkflow: true}
}

func designedDetail() protocol.WorkDetail {
	node := func(id, kind, title, status, content string) protocol.WorkNode {
		return protocol.WorkNode{ID: id, Kind: kind, Title: title, Status: status, Revision: 2, Content: json.RawMessage(content)}
	}
	d := protocol.WorkDetail{Work: protocol.Work{ID: "w", Goal: "ship parser", WorkflowDepth: "designed", Revision: 9}}
	d.Nodes = []protocol.WorkNode{
		node("z-task", "task", "build parser", "in_progress", `{"required":true}`),
		node("a-dep", "task", "prepare input", "pending", `{"required":true}`),
		node("b-decision", "decision", "approved design", "approved", `{"required":true}`),
		node("c-gate", "decision", "public contract review", "proposed", `{"required":true,"gate_kind":"public_contract"}`),
		node("d-unknown", "unknown", "blocking ambiguity", "open", `{"blocking":true}`),
		node("e-criterion", "criterion", "parser suite", "passed", `{"required":true,"command":"go test ./parser"}`),
		node("f-option", "option", "selected design", "proposed", `{"solution_rung":3}`),
		node("g-requirement", "requirement", "preserve input bytes", "proposed", `{"required":true}`),
		node("rejected", "option", "REJECTED_DETAIL", "rejected", `{"solution_rung":6}`),
		node("superseded", "decision", "SUPERSEDED_DETAIL", "superseded", `{}`),
		node("completed", "task", "COMPLETED_DETAIL", "completed", `{"required":true}`),
		node("resolved", "unknown", "RESOLVED_DETAIL", "resolved", `{"blocking":true}`),
		node("memory", "memory_candidate", "MEMORY_DETAIL", "superseded", `{"text":"MEMORY_BODY"}`),
	}
	d.Edges = []protocol.WorkEdge{
		{FromNodeID: "z-task", Relation: "depends_on", ToNodeID: "a-dep"},
		{FromNodeID: "z-task", Relation: "implements", ToNodeID: "b-decision"},
		{FromNodeID: "b-decision", Relation: "selects", ToNodeID: "f-option"},
		{FromNodeID: "e-criterion", Relation: "verifies", ToNodeID: "z-task"},
		{FromNodeID: "f-option", Relation: "serves", ToNodeID: "g-requirement"},
	}
	d.Nodes[2].EvidenceIDs = []string{"ev-b", "ev-a"}
	d.Evidence = []protocol.Evidence{
		{ID: "ev-b", NodeID: "b-decision", SourceURI: "vault://b", Summary: "EVIDENCE_BODY_B"},
		{ID: "ev-a", NodeID: "b-decision", SourceURI: "vault://a", Summary: "EVIDENCE_BODY_A"},
		{ID: "unused", NodeID: "rejected", SourceURI: "vault://UNUSED", Summary: "UNUSED_BODY"},
	}
	return d
}

func TestDesignedProjectionActiveStateAndStableIdentity(t *testing.T) {
	d := designedDetail()
	in := designedInput(d)
	in.Stale = map[string]bool{"e-criterion": true}
	res, ok := Compile(in)
	if !ok {
		t.Fatalf("declined: %+v", res.Report)
	}
	text := res.Messages[0].JoinedText()
	for _, want := range []string{"designed", "revision=9", "c-gate", "public_contract", "b-decision", "approved", "z-task", "a-dep", "depends_on", "d-unknown", "blocking", "e-criterion", "needs re-run", "Completion:", "completed", "f-option", "rung=3", "preserve input bytes", "ev-a", "vault://a"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	for _, absent := range []string{"REJECTED_DETAIL", "SUPERSEDED_DETAIL", "COMPLETED_DETAIL", "RESOLVED_DETAIL", "MEMORY_DETAIL", "MEMORY_BODY", "EVIDENCE_BODY", "vault://UNUSED"} {
		if strings.Contains(text, absent) {
			t.Errorf("inactive or evidence text leaked: %q", absent)
		}
	}
	for i, j := 0, len(d.Nodes)-1; i < j; i, j = i+1, j-1 {
		d.Nodes[i], d.Nodes[j] = d.Nodes[j], d.Nodes[i]
	}
	for i, j := 0, len(d.Edges)-1; i < j; i, j = i+1, j-1 {
		d.Edges[i], d.Edges[j] = d.Edges[j], d.Edges[i]
	}
	d.Evidence[0], d.Evidence[1] = d.Evidence[1], d.Evidence[0]
	again := designedInput(d)
	again.Stale = in.Stale
	other, ok := Compile(again)
	if !ok || other.Messages[0].JoinedText() != text {
		t.Fatal("packet depends on insertion order")
	}
	if strings.Index(text, "a-dep") > strings.Index(text, "b-decision") {
		t.Fatal("semantic nodes not sorted by identity")
	}
}

func TestDesignedFlagOffPreservesLegacyPacket(t *testing.T) {
	d := designedDetail()
	const legacy = "## Work\nGoal: ship parser\nCriteria:\n- parser suite — passed (`go test ./parser`)\n"
	res, ok := Compile(Input{Detail: d, Active: []protocol.Evidence{}})
	if !ok || res.Messages[0].JoinedText() != packetHeading+"\n\n"+legacy {
		t.Fatalf("feature-off packet changed: %+v", res)
	}
}

func TestDesignedSupportingFactsCarryReferencesOnly(t *testing.T) {
	d := designedDetail()
	d.Nodes = append(d.Nodes, protocol.WorkNode{ID: "fact", Kind: "fact", Status: "active", Title: "FACT_BODY", Content: json.RawMessage(`{"text":"FACT_CONTENT"}`), EvidenceIDs: []string{"fact-ev"}})
	d.Edges = append(d.Edges, protocol.WorkEdge{FromNodeID: "fact", Relation: "supports", ToNodeID: "b-decision"})
	d.Evidence = append(d.Evidence, protocol.Evidence{ID: "fact-ev", NodeID: "fact", SourceURI: "vault://fact", Summary: "FACT_EVIDENCE_BODY"})
	res, ok := Compile(designedInput(d))
	if !ok {
		t.Fatalf("declined: %+v", res.Report)
	}
	text := res.Messages[0].JoinedText()
	if !strings.Contains(text, "fact-ev") || !strings.Contains(text, "vault://fact") {
		t.Fatalf("supporting evidence omitted: %s", text)
	}
	for _, absent := range []string{"FACT_BODY", "FACT_CONTENT", "FACT_EVIDENCE_BODY"} {
		if strings.Contains(text, absent) {
			t.Fatalf("supporting fact text leaked: %s", text)
		}
	}
}

func attempt(critID, status, summary string, at time.Time) (protocol.VerificationAttempt, protocol.Evidence) {
	ev := protocol.Evidence{ID: "e-" + critID + status, NodeID: critID, Kind: protocol.EvidenceVerificationOutput,
		Summary: summary, ObservedAt: at}
	return protocol.VerificationAttempt{ID: "a-" + critID + status, CriterionNodeID: critID, Status: status,
		EvidenceID: ev.ID, StartedAt: at, FinishedAt: at}, ev
}

func TestWorkPacketRendersCriteriaAndStatuses(t *testing.T) {
	a, ev := attempt("c1", protocol.AttemptPassed, "ok", base)
	d := protocol.WorkDetail{
		Work:     protocol.Work{ID: "w", Goal: "ship the parser"},
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptPassed), crit("c2", "old", "make old", "superseded")},
		Attempts: []protocol.VerificationAttempt{a},
		Evidence: []protocol.Evidence{ev},
	}
	text, n := workPacket(d, nil)
	if !strings.Contains(text, "Goal: ship the parser") {
		t.Fatalf("no goal line: %q", text)
	}
	if !strings.Contains(text, "- unit — passed (`go test ./...`)") {
		t.Fatalf("passed criterion not rendered: %q", text)
	}
	if strings.Contains(text, "old") || n != 1 {
		t.Fatalf("superseded criterion must be omitted, criteria = %d: %q", n, text)
	}
}

func TestStaleCriterionNeverRendersAsPassed(t *testing.T) {
	a, ev := attempt("c1", protocol.AttemptPassed, "ok", base)
	d := protocol.WorkDetail{
		Work:     protocol.Work{Goal: "g"},
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.StatusStale)},
		Attempts: []protocol.VerificationAttempt{a},
		Evidence: []protocol.Evidence{ev},
	}
	text, _ := workPacket(d, nil)
	if !strings.Contains(text, "needs re-run") || strings.Contains(text, "passed") {
		t.Fatalf("stale rendering wrong: %q", text)
	}
}

func TestUnresolvedListsLatestFailedAttemptSummary(t *testing.T) {
	old, oldEv := attempt("c1", protocol.AttemptPassed, "was fine", base)
	newer, newerEv := attempt("c1", protocol.AttemptFailed, "FAIL: parser panics", base.Add(time.Minute))
	d := protocol.WorkDetail{
		Work:     protocol.Work{Goal: "g"},
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{old, newer},
		Evidence: []protocol.Evidence{oldEv, newerEv},
	}
	text, _ := workPacket(d, nil)
	if !strings.Contains(text, "Unresolved:") || !strings.Contains(text, "- unit: FAIL: parser panics") {
		t.Fatalf("unresolved section wrong: %q", text)
	}
	if strings.Contains(text, "was fine") {
		t.Fatalf("older attempt leaked into the packet: %q", text)
	}
}

func TestChangedFilesAreDistinctAndSorted(t *testing.T) {
	d := protocol.WorkDetail{
		Work: protocol.Work{Goal: "g"},
		Evidence: []protocol.Evidence{
			{Kind: protocol.EvidenceFileChange, SourceURI: "b.go", ObservedAt: base},
			{Kind: protocol.EvidenceFileChange, SourceURI: "a.go", ObservedAt: base},
			{Kind: protocol.EvidenceFileChange, SourceURI: "b.go", ObservedAt: base},
		},
	}
	text, _ := workPacket(d, nil)
	if !strings.Contains(text, "Changed files: a.go, b.go") {
		t.Fatalf("changed files wrong: %q", text)
	}
	empty, _ := workPacket(protocol.WorkDetail{Work: protocol.Work{Goal: "g"}}, nil)
	if strings.Contains(empty, "Changed files") {
		t.Fatalf("empty changed-file list must be omitted: %q", empty)
	}
}

func TestWorkPacketRedactsOldSecrets(t *testing.T) {
	d := protocol.WorkDetail{
		Work:  protocol.Work{Goal: "deploy with API_TOKEN=sk-abcdefghijklmnopqrstuvwxyz0123"},
		Nodes: []protocol.WorkNode{crit("c1", "unit", "API_TOKEN=sk-abcdefghijklmnopqrstuvwxyz0123 npm test", "pending")},
	}
	text, _ := workPacket(d, nil)
	if strings.Contains(text, "sk-abcdef") {
		t.Fatalf("secret reached the packet: %q", text)
	}
}

func TestCallerSuppliedStalenessOverridesStoredStatus(t *testing.T) {
	a, ev := attempt("c1", protocol.AttemptPassed, "ok", base)
	d := protocol.WorkDetail{
		Work:     protocol.Work{Goal: "g"},
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptPassed)},
		Attempts: []protocol.VerificationAttempt{a},
		Evidence: []protocol.Evidence{ev},
	}
	text, _ := workPacket(d, map[string]bool{"c1": true})
	if !strings.Contains(text, "needs re-run") || strings.Contains(text, "— passed") {
		t.Fatalf("a criterion the caller calls stale must not read as passed: %q", text)
	}
}
