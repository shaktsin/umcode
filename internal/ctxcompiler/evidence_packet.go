package ctxcompiler

import (
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

// maxToolErrors caps the failed-tool facts in a packet. Evidence rows carry no
// turn id, so recency is by observation time.
const maxToolErrors = 5

// evidencePacket renders what is still true: the failures the model has to fix,
// with a vault reference for the full output, and one line per passing check.
// The third result holds the line indexes that may be dropped first (P2).
func evidencePacket(d protocol.WorkDetail, active []protocol.Evidence) (string, int, []int) {
	rows := activeRows(d, active)
	status := attemptStatusByEvidence(d)

	var lines []string
	var p2 []int
	var toolErrors []protocol.Evidence
	for _, e := range rows {
		switch e.Kind {
		case protocol.EvidenceVerificationOutput:
			if status[e.ID] == protocol.AttemptPassed {
				p2 = append(p2, len(lines))
				lines = append(lines, "- ok "+redact(e.SourceURI))
				continue
			}
			line := "- FAILED " + redact(e.SourceURI) + ": " + redact(strings.TrimSpace(e.Summary))
			switch {
			case e.Availability == protocol.AvailUnavailable:
				line += " [full output not retained]"
			case e.VaultHash != "":
				line += " [full output: vault " + e.VaultHash[:8] + "]"
			}
			lines = append(lines, line)
		case protocol.EvidenceToolError:
			toolErrors = append(toolErrors, e)
		}
	}
	sort.SliceStable(toolErrors, func(i, j int) bool { return toolErrors[i].ObservedAt.After(toolErrors[j].ObservedAt) })
	if len(toolErrors) > maxToolErrors {
		toolErrors = toolErrors[:maxToolErrors]
	}
	for _, e := range toolErrors {
		lines = append(lines, "- tool "+redact(e.SourceURI)+" failed: "+redact(strings.TrimSpace(e.Summary)))
	}
	if len(lines) == 0 {
		return "", 0, nil
	}
	return "## Evidence\n" + strings.Join(lines, "\n") + "\n", len(lines), shift(p2, 1)
}

// shift moves P2 indexes past the section heading, so they index the rendered
// packet's lines.
func shift(idx []int, by int) []int {
	out := make([]int, 0, len(idx))
	for _, i := range idx {
		out = append(out, i+by)
	}
	return out
}

// activeRows returns the evidence to render: what the caller declared active,
// or every row that is not stale when it declared nothing.
func activeRows(d protocol.WorkDetail, active []protocol.Evidence) []protocol.Evidence {
	if active != nil {
		return active
	}
	var out []protocol.Evidence
	for _, e := range d.Evidence {
		if e.StaleAt == nil {
			out = append(out, e)
		}
	}
	return out
}

// attemptStatusByEvidence maps an evidence row to the status of the attempt it
// belongs to, so a passing check can be told from a failing one.
func attemptStatusByEvidence(d protocol.WorkDetail) map[string]string {
	out := map[string]string{}
	for _, a := range d.Attempts {
		if a.EvidenceID != "" {
			out[a.EvidenceID] = a.Status
		}
	}
	return out
}
