package ctxcompiler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func verif(id, uri, summary, hash, avail string) protocol.Evidence {
	return protocol.Evidence{ID: id, Kind: protocol.EvidenceVerificationOutput, SourceURI: uri, Summary: summary,
		VaultHash: hash, Availability: avail, ObservedAt: base}
}

const hashA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEvidencePacketIncludesOnlyActiveRows(t *testing.T) {
	stale := base
	all := []protocol.Evidence{
		verif("keep", "go test ./...", "ok", "", protocol.AvailNone),
		verif("drop", "go vet ./...", "ok", "", protocol.AvailNone),
	}
	all[1].StaleAt = &stale
	d := protocol.WorkDetail{Evidence: all}
	text, rows, _ := evidencePacket(d, []protocol.Evidence{all[0]})
	if rows != 1 || !strings.Contains(text, "go test") || strings.Contains(text, "go vet") {
		t.Fatalf("rows = %d: %q", rows, text)
	}
	// A nil active list falls back to every row that is not stale.
	text, rows, _ = evidencePacket(d, nil)
	if rows != 1 || strings.Contains(text, "go vet") {
		t.Fatalf("nil active: rows = %d: %q", rows, text)
	}
}

func TestFailedCheckCarriesVaultReference(t *testing.T) {
	e := verif("e1", "go test ./...", "FAIL: boom", hashA, protocol.AvailAvailable)
	e.Kind = protocol.EvidenceVerificationOutput
	d := protocol.WorkDetail{
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{{CriterionNodeID: "c1", Status: protocol.AttemptFailed, EvidenceID: "e1", StartedAt: base}},
		Evidence: []protocol.Evidence{e},
	}
	text, _, _ := evidencePacket(d, []protocol.Evidence{e})
	want := "- FAILED go test ./...: FAIL: boom [full output: vault 01234567]"
	if !strings.Contains(text, want) {
		t.Fatalf("want %q in %q", want, text)
	}
}

func TestMissingHashYieldsNoReference(t *testing.T) {
	e := verif("e1", "go test ./...", "FAIL: boom", "", protocol.AvailNone)
	d := protocol.WorkDetail{
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{{CriterionNodeID: "c1", Status: protocol.AttemptFailed, EvidenceID: "e1", StartedAt: base}},
		Evidence: []protocol.Evidence{e},
	}
	text, _, _ := evidencePacket(d, []protocol.Evidence{e})
	if strings.Contains(text, "vault") || !strings.Contains(text, "- FAILED go test ./...: FAIL: boom") {
		t.Fatalf("text = %q", text)
	}
}

func TestUnavailableRowIsMarkedNotRetained(t *testing.T) {
	e := verif("e1", "go test ./...", "FAIL: boom", hashA, protocol.AvailUnavailable)
	d := protocol.WorkDetail{
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{{CriterionNodeID: "c1", Status: protocol.AttemptFailed, EvidenceID: "e1", StartedAt: base}},
		Evidence: []protocol.Evidence{e},
	}
	text, _, _ := evidencePacket(d, []protocol.Evidence{e})
	if !strings.Contains(text, "[full output not retained]") || strings.Contains(text, "vault 0123") {
		t.Fatalf("text = %q", text)
	}
}

func TestToolErrorsAreNewestFirstAndCapped(t *testing.T) {
	var rows []protocol.Evidence
	for i := 0; i < 7; i++ {
		rows = append(rows, protocol.Evidence{ID: fmt.Sprintf("t%d", i), Kind: protocol.EvidenceToolError,
			SourceURI: fmt.Sprintf("tool%d", i), Summary: "boom", ObservedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	text, _, _ := evidencePacket(protocol.WorkDetail{Evidence: rows}, rows)
	if n := strings.Count(text, "- tool "); n != maxToolErrors {
		t.Fatalf("tool error lines = %d, want %d: %q", n, maxToolErrors, text)
	}
	if !strings.Contains(text, "tool6") || strings.Contains(text, "tool1") {
		t.Fatalf("not newest-first: %q", text)
	}
	if i6, i5 := strings.Index(text, "tool6"), strings.Index(text, "tool5"); i6 > i5 {
		t.Fatalf("newest must come first: %q", text)
	}
}

func TestPassingLinesAreMarkedP2(t *testing.T) {
	pass := verif("e1", "go test ./...", "ok", "", protocol.AvailNone)
	fail := verif("e2", "go vet ./...", "FAIL", "", protocol.AvailNone)
	d := protocol.WorkDetail{
		Nodes: []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptPassed), crit("c2", "vet", "go vet ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{
			{CriterionNodeID: "c1", Status: protocol.AttemptPassed, EvidenceID: "e1", StartedAt: base},
			{CriterionNodeID: "c2", Status: protocol.AttemptFailed, EvidenceID: "e2", StartedAt: base},
		},
		Evidence: []protocol.Evidence{pass, fail},
	}
	text, _, p2 := evidencePacket(d, []protocol.Evidence{pass, fail})
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(p2) != 1 {
		t.Fatalf("p2 = %v in %q", p2, text)
	}
	if !strings.HasPrefix(lines[p2[0]], "- ok ") {
		t.Fatalf("p2 index %d points at %q", p2[0], lines[p2[0]])
	}
}

func TestEvidencePacketRedactsOldSecrets(t *testing.T) {
	e := verif("e1", "go test ./...", "AWS_SECRET_ACCESS_KEY=abcdef1234567890", "", protocol.AvailNone)
	d := protocol.WorkDetail{
		Nodes:    []protocol.WorkNode{crit("c1", "unit", "go test ./...", protocol.AttemptFailed)},
		Attempts: []protocol.VerificationAttempt{{CriterionNodeID: "c1", Status: protocol.AttemptFailed, EvidenceID: "e1", StartedAt: base}},
		Evidence: []protocol.Evidence{e},
	}
	text, _, _ := evidencePacket(d, []protocol.Evidence{e})
	if strings.Contains(text, "abcdef1234567890") {
		t.Fatalf("secret reached the packet: %q", text)
	}
}
