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
	text, n := workPacket(d)
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
	text, _ := workPacket(d)
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
	text, _ := workPacket(d)
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
	text, _ := workPacket(d)
	if !strings.Contains(text, "Changed files: a.go, b.go") {
		t.Fatalf("changed files wrong: %q", text)
	}
	empty, _ := workPacket(protocol.WorkDetail{Work: protocol.Work{Goal: "g"}})
	if strings.Contains(empty, "Changed files") {
		t.Fatalf("empty changed-file list must be omitted: %q", empty)
	}
}

func TestWorkPacketRedactsOldSecrets(t *testing.T) {
	d := protocol.WorkDetail{
		Work:  protocol.Work{Goal: "deploy with API_TOKEN=sk-abcdefghijklmnopqrstuvwxyz0123"},
		Nodes: []protocol.WorkNode{crit("c1", "unit", "API_TOKEN=sk-abcdefghijklmnopqrstuvwxyz0123 npm test", "pending")},
	}
	text, _ := workPacket(d)
	if strings.Contains(text, "sk-abcdef") {
		t.Fatalf("secret reached the packet: %q", text)
	}
}
