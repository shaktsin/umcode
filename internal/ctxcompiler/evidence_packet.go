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
func evidencePacket(d protocol.WorkDetail, active []protocol.Evidence, stale map[string]bool) (string, int, []int) {
	text, n, p2, _ := evidencePacketTracked(d, active, stale)
	return text, n, p2
}

func evidencePacketTracked(d protocol.WorkDetail, active []protocol.Evidence, stale map[string]bool) (string, int, []int, map[int]string) {
	rows := newestPerCheck(withUnavailable(d, activeRows(d, active)))
	status := attemptStatusByEvidence(d)
	criterionOf := criterionByEvidence(d)

	var lines []string
	ids := map[int]string{}
	add := func(id, text string) { ids[len(lines)+1] = "evidence:" + id; lines = append(lines, text) }
	var p2 []int
	var toolErrors []protocol.Evidence
	for _, e := range rows {
		switch e.Kind {
		case protocol.EvidenceVerificationOutput:
			switch status[e.ID] {
			case protocol.AttemptNotRun, protocol.AttemptBlocked:
				// Never run is not the same as failed, and saying so would send
				// the model chasing a failure that does not exist.
				add(e.ID, "- not completed "+redact(e.SourceURI)+": "+redact(strings.TrimSpace(e.Summary)))
				continue
			}
			if stale[e.NodeID] || stale[criterionOf[e.ID]] {
				// The work packet already says this criterion needs re-running;
				// repeating its result here would contradict that.
				continue
			}
			if status[e.ID] == protocol.AttemptPassed {
				p2 = append(p2, len(lines))
				add(e.ID, "- ok "+redact(e.SourceURI))
				continue
			}
			line := "- FAILED " + redact(e.SourceURI) + ": " + redact(strings.TrimSpace(e.Summary))
			switch {
			case e.Availability == protocol.AvailUnavailable:
				line += " [full output not retained]"
			case e.VaultHash != "":
				line += " [full output: vault " + e.VaultHash[:8] + "]"
			}
			add(e.ID, line)
		case protocol.EvidenceToolError:
			toolErrors = append(toolErrors, e)
		}
	}
	sort.SliceStable(toolErrors, func(i, j int) bool { return toolErrors[i].ObservedAt.After(toolErrors[j].ObservedAt) })
	if len(toolErrors) > maxToolErrors {
		toolErrors = toolErrors[:maxToolErrors]
	}
	for _, e := range toolErrors {
		add(e.ID, "- tool "+redact(e.SourceURI)+" failed: "+redact(strings.TrimSpace(e.Summary)))
	}
	if len(lines) == 0 {
		return "", 0, nil, ids
	}
	return "## Evidence\n" + strings.Join(lines, "\n") + "\n", len(lines), shift(p2, 1), ids
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

// withUnavailable adds back the evidence whose vault object is gone. The
// engine's active-evidence filter drops those rows, but a failure the model
// must still fix should read as summary-only, not disappear.
func withUnavailable(d protocol.WorkDetail, rows []protocol.Evidence) []protocol.Evidence {
	have := map[string]bool{}
	for _, e := range rows {
		have[e.ID] = true
	}
	superseded := map[string]bool{}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeCriterion && n.Status == protocol.StatusSuperseded {
			superseded[n.ID] = true
		}
	}
	for _, e := range d.Evidence {
		if have[e.ID] || e.Availability != protocol.AvailUnavailable || e.StaleAt != nil || superseded[e.NodeID] {
			continue
		}
		rows = append(rows, e)
	}
	return rows
}

// newestPerCheck keeps one verification row per command. Ad-hoc runs carry no
// criterion, so nothing upstream supersedes them, and an old failure would
// otherwise sit beside the pass that replaced it.
func newestPerCheck(rows []protocol.Evidence) []protocol.Evidence {
	newest := map[string]protocol.Evidence{}
	for _, e := range rows {
		if e.Kind != protocol.EvidenceVerificationOutput {
			continue
		}
		if prev, ok := newest[e.SourceURI]; !ok || !e.ObservedAt.Before(prev.ObservedAt) {
			newest[e.SourceURI] = e
		}
	}
	var out []protocol.Evidence
	for _, e := range rows {
		if e.Kind != protocol.EvidenceVerificationOutput {
			out = append(out, e)
			continue
		}
		if newest[e.SourceURI].ID == e.ID {
			out = append(out, e)
		}
	}
	return out
}

// criterionByEvidence maps an evidence row to the criterion of the attempt it
// belongs to, which is where staleness is decided.
func criterionByEvidence(d protocol.WorkDetail) map[string]string {
	out := map[string]string{}
	for _, a := range d.Attempts {
		if a.EvidenceID != "" && a.CriterionNodeID != "" {
			out[a.EvidenceID] = a.CriterionNodeID
		}
	}
	return out
}
