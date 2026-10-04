package ctxcompiler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// stalePass is a work whose single criterion passed and was then invalidated.
func stalePass() (protocol.WorkDetail, protocol.Evidence) {
	ev := verif("e1", "go test ./...", "PASS\nok  github.com/x  0.3s", "", protocol.AvailNone)
	ev.NodeID = "c1"
	d := protocol.WorkDetail{
		Work:     protocol.Work{Goal: "g"},
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptPassed)},
		Attempts: []protocol.VerificationAttempt{{CriterionNodeID: "c1", Status: protocol.AttemptPassed, EvidenceID: "e1", StartedAt: base, FinishedAt: base}},
		Evidence: []protocol.Evidence{ev},
	}
	return d, ev
}

func TestStaleCriterionEvidenceIsNotPresentedAsCurrent(t *testing.T) {
	d, ev := stalePass()
	res, ok := Compile(Input{Detail: d, Stale: map[string]bool{"c1": true}, Active: []protocol.Evidence{ev}, Window: 200000})
	if !ok {
		t.Fatalf("declined: %q", res.Report.Declined)
	}
	text := res.Messages[0].Parts[0].Text
	if !strings.Contains(text, "needs re-run") {
		t.Fatalf("criterion not marked stale: %q", text)
	}
	if strings.Contains(text, "- ok go test ./...") {
		t.Fatalf("a criterion the packet calls stale must not also appear as a passing check: %q", text)
	}
}

func TestStaleCriterionPassingSummaryNotUnresolved(t *testing.T) {
	d, _ := stalePass()
	text, _ := workPacket(d, map[string]bool{"c1": true})
	if strings.Contains(text, "Unresolved:") {
		t.Fatalf("a stale pass has no failure to report: %q", text)
	}
}

func TestAdHocChecksDedupeNewestWins(t *testing.T) {
	old := verif("e1", "npm test", "FAIL 3 tests", "", protocol.AvailNone)
	old.ObservedAt = base
	newer := verif("e2", "npm test", "ok", "", protocol.AvailNone)
	newer.ObservedAt = base.Add(time.Minute)
	d := protocol.WorkDetail{
		Attempts: []protocol.VerificationAttempt{
			{Status: protocol.AttemptFailed, EvidenceID: "e1", StartedAt: base},
			{Status: protocol.AttemptPassed, EvidenceID: "e2", StartedAt: base.Add(time.Minute)},
		},
		Evidence: []protocol.Evidence{old, newer},
	}
	text, rows, _ := evidencePacket3(d, []protocol.Evidence{old, newer})
	if strings.Contains(text, "FAILED npm test") || !strings.Contains(text, "- ok npm test") || rows != 1 {
		t.Fatalf("ad-hoc runs of one command must collapse to the newest, rows = %d: %q", rows, text)
	}
}

func TestCompileDeclinesWhenPacketsCannotFitBudget(t *testing.T) {
	d := protocol.WorkDetail{Work: protocol.Work{Goal: "g"}}
	var active []protocol.Evidence
	for i := 0; i < 8; i++ {
		e := verif(fmt.Sprintf("e%d", i), fmt.Sprintf("check%d", i), strings.Repeat("failure detail ", 200), "", protocol.AvailNone)
		d.Evidence = append(d.Evidence, e)
		d.Attempts = append(d.Attempts, protocol.VerificationAttempt{Status: protocol.AttemptFailed, EvidenceID: e.ID, StartedAt: base})
		active = append(active, e)
	}
	res, ok := Compile(Input{Detail: d, Active: active, Window: 20000})
	budget := packetBudget(20000)
	if ok {
		head := res.Messages[0].Parts[0].Text
		if got := textTokens(head); got > budget {
			t.Fatalf("packets of %d tokens exceed the %d-token budget with no decline; drops = %+v", got, budget, res.Report.Drops)
		}
	}
	if !ok && res.Report.Declined == "" {
		t.Fatal("a decline must say why")
	}
}

func TestUnavailableRowsSurviveTheActiveFilter(t *testing.T) {
	gone := verif("e1", "go test ./...", "FAIL: boom", hashA, protocol.AvailUnavailable)
	d := protocol.WorkDetail{
		Work:     protocol.Work{Goal: "g"},
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{{CriterionNodeID: "c1", Status: protocol.AttemptFailed, EvidenceID: "e1", StartedAt: base}},
		Evidence: []protocol.Evidence{gone},
	}
	// The engine's active-evidence filter drops unavailable rows, so the packet
	// must recover them rather than lose the failure entirely.
	text, _, _ := evidencePacket3(d, []protocol.Evidence{})
	if !strings.Contains(text, "FAILED go test ./...") || !strings.Contains(text, "[full output not retained]") {
		t.Fatalf("unavailable failure lost: %q", text)
	}
}

func TestNotRunAndBlockedAreNotCalledFailed(t *testing.T) {
	for _, st := range []string{protocol.AttemptNotRun, protocol.AttemptBlocked} {
		e := verif("e1", "go test ./...", "could not run: no network", "", protocol.AvailNone)
		d := protocol.WorkDetail{
			Attempts: []protocol.VerificationAttempt{{Status: st, EvidenceID: "e1", StartedAt: base}},
			Evidence: []protocol.Evidence{e},
		}
		text, _, _ := evidencePacket3(d, []protocol.Evidence{e})
		if strings.Contains(text, "FAILED") {
			t.Fatalf("%s must not read as a failure: %q", st, text)
		}
		if !strings.Contains(text, "could not run") {
			t.Fatalf("%s lost its summary: %q", st, text)
		}
	}
}

func TestAnsweredQuestionComesFromJustOutsideTheWindow(t *testing.T) {
	// An ancient question, then a recent one that falls just outside the window.
	items := []protocol.Item{
		item(protocol.ItemUserMessage, "ancient ask"),
		item(protocol.ItemAgentMessage, "Ancient question?"),
		item(protocol.ItemUserMessage, "ancient answer"),
	}
	items = append(items, pairs(8)...)
	items = append(items,
		item(protocol.ItemUserMessage, "recent ask"),
		item(protocol.ItemAgentMessage, "Recent question?"),
		item(protocol.ItemUserMessage, "the second one"))
	items = append(items, pairs(9)...)
	msgs, _ := tail(items, "now", 0)
	s := texts(msgs)
	if !strings.Contains(s, "Recent question?") {
		t.Fatalf("the question just outside the window must be kept: %s", s)
	}
	if strings.Contains(s, "Ancient question?") {
		t.Fatalf("an ancient question is noise, not context: %s", s)
	}
}

func TestTailOverBudgetIsReported(t *testing.T) {
	one := []protocol.Item{
		item(protocol.ItemUserMessage, strings.Repeat("a very long single request ", 500)),
		item(protocol.ItemAgentMessage, "ok"),
	}
	d := protocol.WorkDetail{Work: protocol.Work{Goal: "g"}}
	res, ok := Compile(Input{Detail: d, Items: one, Window: 4000})
	if !ok {
		t.Fatalf("declined: %q", res.Report.Declined)
	}
	if res.Report.TailTokens <= tailBudget(4000) {
		t.Skip("tail fit the budget; nothing to report")
	}
	var found bool
	for _, dr := range res.Report.Drops {
		if dr.Class == "tail" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a tail that cannot fit its budget must be reported: %+v", res.Report)
	}
}

func TestLongAnsweredQuestionIsNotDraggedAlong(t *testing.T) {
	// A question asked and answered long ago is noise: the current request
	// cannot be answering it.
	items := []protocol.Item{
		item(protocol.ItemUserMessage, "ancient ask"),
		item(protocol.ItemAgentMessage, "Ancient question?"),
		item(protocol.ItemUserMessage, "ancient answer"),
	}
	items = append(items, pairs(14)...)
	msgs, _ := tail(items, "now", 0)
	if s := texts(msgs); strings.Contains(s, "Ancient question?") {
		t.Fatalf("a question answered long ago must not ride along: %s", s)
	}
}
