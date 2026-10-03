package ctxcompiler

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// scenario is a work with one passing and one failing criterion, plus a short
// transcript.
func scenario() Input {
	pass := verif("e1", "go test ./...", "ok", "", protocol.AvailNone)
	fail := verif("e2", "go vet ./...", strings.Repeat("vet output ", 50)+"FAIL", hashA, protocol.AvailAvailable)
	d := protocol.WorkDetail{
		Work:  protocol.Work{ID: "w", Goal: "ship the parser"},
		Nodes: []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptPassed), crit("c2", "vet", "go vet ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{
			{CriterionNodeID: "c1", Status: protocol.AttemptPassed, EvidenceID: "e1", StartedAt: base},
			{CriterionNodeID: "c2", Status: protocol.AttemptFailed, EvidenceID: "e2", StartedAt: base},
		},
		Evidence: []protocol.Evidence{pass, fail},
	}
	return Input{Detail: d, Active: []protocol.Evidence{pass, fail}, Items: pairs(3), TurnID: "now", Window: 200000}
}

func TestCompileLayoutIsPacketThenTail(t *testing.T) {
	res, ok := Compile(scenario())
	if !ok {
		t.Fatalf("declined: %q", res.Report.Declined)
	}
	if res.Messages[0].Role != llm.RoleUser {
		t.Fatalf("first message role = %s", res.Messages[0].Role)
	}
	head := res.Messages[0].Parts[0].Text
	if !strings.HasPrefix(head, "Current work state") || !strings.Contains(head, "## Work") || !strings.Contains(head, "## Evidence") {
		t.Fatalf("packet message = %q", head)
	}
	tailText := texts(res.Messages[1:])
	if !strings.Contains(tailText, "u3") {
		t.Fatalf("tail missing: %q", tailText)
	}
	if res.Report.TailMessages != len(res.Messages)-1 {
		t.Fatalf("report tail = %d, messages = %d", res.Report.TailMessages, len(res.Messages))
	}
}

func TestCompileDropsP2BeforeP1(t *testing.T) {
	in := scenario()
	// A window whose 15% share fits the work packet with only a few tokens to
	// spare, so the evidence packet must give something up.
	w, _ := workPacket(in.Detail, nil)
	in.Window = int(float64(textTokens(w)+4) / packetFraction)
	res, ok := Compile(in)
	if !ok {
		t.Fatalf("declined: %q", res.Report.Declined)
	}
	out := res.Messages[0].Parts[0].Text
	if strings.Contains(out, "- ok go test") {
		t.Fatalf("passing line should have been dropped first: %q", out)
	}
	if !strings.Contains(out, "FAILED go vet") || !strings.Contains(out, "## Work") {
		t.Fatalf("P0/P1 content was dropped: %q", out)
	}
	var classes []string
	for _, d := range res.Report.Drops {
		classes = append(classes, d.Class)
		if d.Reason != "packet budget" {
			t.Fatalf("drop reason = %q", d.Reason)
		}
	}
	if len(classes) == 0 || classes[0] != "passing evidence" {
		t.Fatalf("drops = %v", res.Report.Drops)
	}
}

func TestCompileDeclinesWhenP0OverBudget(t *testing.T) {
	in := scenario()
	in.Window = 40 // 15% of 40 is 6 tokens: the goal alone does not fit
	res, ok := Compile(in)
	if ok || res.Report.Declined != "P0 over budget" {
		t.Fatalf("ok = %v, declined = %q", ok, res.Report.Declined)
	}
}

func TestCompileDeclinesWhenNotSmallerThanHistory(t *testing.T) {
	in := scenario()
	in.HistoryTokens = 1
	res, ok := Compile(in)
	if ok || res.Report.Declined != "not smaller than history" {
		t.Fatalf("ok = %v, declined = %q", ok, res.Report.Declined)
	}
	in.HistoryTokens = 100000
	if _, ok := Compile(in); !ok {
		t.Fatal("a generous history size must not decline")
	}
}

func TestCompileReportCountsTokensAndRows(t *testing.T) {
	res, ok := Compile(scenario())
	if !ok {
		t.Fatal("declined")
	}
	r := res.Report
	if r.Criteria != 2 || r.Evidence != 2 {
		t.Fatalf("criteria = %d, evidence = %d", r.Criteria, r.Evidence)
	}
	if r.WorkPacketTokens <= 0 || r.EvidencePacketTokens <= 0 || r.TailTokens <= 0 {
		t.Fatalf("token counts = %+v", r)
	}
	if r.Declined != "" {
		t.Fatalf("declined = %q", r.Declined)
	}
}
