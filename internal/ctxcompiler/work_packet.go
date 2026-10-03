package ctxcompiler

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/vault"
)

// statusSuperseded is the criterion status a later verification plan sets. It is
// compared as a literal so this package does not depend on internal/work.
const statusSuperseded = "superseded"

// redact masks secrets on their way into a packet. Rows written before the
// vault's redaction shipped still hold them unmasked.
func redact(s string) string {
	out, _ := vault.Redact([]byte(s))
	return string(out)
}

// criterionCommand reads the planned command stored on a criterion node.
func criterionCommand(n protocol.WorkNode) string {
	var c struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(n.Content, &c)
	return c.Command
}

// latestAttempts returns the newest attempt per criterion; later attempts win
// ties so a re-run in the same instant still supersedes.
func latestAttempts(d protocol.WorkDetail) map[string]protocol.VerificationAttempt {
	out := map[string]protocol.VerificationAttempt{}
	for _, a := range d.Attempts {
		if a.CriterionNodeID == "" {
			continue
		}
		if prev, ok := out[a.CriterionNodeID]; !ok || !a.StartedAt.Before(prev.StartedAt) {
			out[a.CriterionNodeID] = a
		}
	}
	return out
}

// workPacket renders the P0 section: the goal, every live criterion with its
// status, the unresolved failures, and the files this work changed.
func workPacket(d protocol.WorkDetail) (string, int) {
	var b strings.Builder
	b.WriteString("## Work\nGoal: ")
	b.WriteString(redact(d.Work.Goal))
	b.WriteString("\n")

	summaries := map[string]string{}
	for _, e := range d.Evidence {
		summaries[e.ID] = e.Summary
	}
	latest := latestAttempts(d)

	var criteria, unresolved []string
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeCriterion || n.Status == statusSuperseded {
			continue
		}
		title, command := redact(n.Title), redact(criterionCommand(n))
		status := n.Status
		if status == protocol.StatusStale {
			status = "needs re-run, a change landed after it last succeeded"
		}
		criteria = append(criteria, "- "+title+" — "+status+" (`"+command+"`)")
		a, ok := latest[n.ID]
		if ok && a.Status == protocol.AttemptPassed && n.Status != protocol.StatusStale {
			continue
		}
		if ok {
			if s := strings.TrimSpace(summaries[a.EvidenceID]); s != "" {
				unresolved = append(unresolved, "- "+title+": "+redact(s))
			}
		}
	}
	if len(criteria) > 0 {
		b.WriteString("Criteria:\n")
		b.WriteString(strings.Join(criteria, "\n"))
		b.WriteString("\n")
	}
	if len(unresolved) > 0 {
		b.WriteString("Unresolved:\n")
		b.WriteString(strings.Join(unresolved, "\n"))
		b.WriteString("\n")
	}
	if files := changedFiles(d); len(files) > 0 {
		b.WriteString("Changed files: ")
		b.WriteString(strings.Join(files, ", "))
		b.WriteString("\n")
	}
	return b.String(), len(criteria)
}

// changedFiles lists the distinct paths of this work's file_change evidence.
func changedFiles(d protocol.WorkDetail) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range d.Evidence {
		if e.Kind != protocol.EvidenceFileChange || e.SourceURI == "" || seen[e.SourceURI] {
			continue
		}
		seen[e.SourceURI] = true
		out = append(out, redact(e.SourceURI))
	}
	sort.Strings(out)
	return out
}
