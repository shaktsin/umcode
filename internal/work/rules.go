package work

import (
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

// EscalatesToGuided reports whether a tool call moves a work from direct to
// guided: a workspace mutation, a shell command above green risk, or any
// verification tool (which runs shell commands above green risk).
func EscalatesToGuided(tool, risk string) bool {
	switch tool {
	case "file.write", "file.edit", "verification.plan", "verification.run", "browser.verify":
		return true
	case "shell.run", "exec.start", "exec.write":
		return risk != "" && risk != "green"
	}
	return false
}

// StatusSuperseded marks a criterion that a later verification plan dropped.
const StatusSuperseded = "superseded"

func isVerificationTool(tool string) bool {
	return strings.HasPrefix(tool, "verification.") || tool == "browser.verify"
}

// Unresolved returns the titles of criteria that are not currently satisfied:
// those with no attempt, whose latest attempt did not pass, or that had a file
// change recorded after their latest passing attempt.
func Unresolved(d protocol.WorkDetail) []string {
	lastChange := lastFileChange(d)
	latest := latestAttempts(d)
	var out []string
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeCriterion || n.Status == StatusSuperseded {
			continue
		}
		a, ok := latest[n.ID]
		switch {
		case !ok, a.Status != protocol.AttemptPassed, n.Status == protocol.StatusStale:
			out = append(out, n.Title)
		case lastChange != nil && lastChange.ObservedAt.After(a.FinishedAt):
			out = append(out, n.Title)
		}
	}
	return out
}

func lastFileChange(d protocol.WorkDetail) *protocol.Evidence {
	var last *protocol.Evidence
	for i := range d.Evidence {
		e := &d.Evidence[i]
		if e.Kind == protocol.EvidenceFileChange && (last == nil || e.ObservedAt.After(last.ObservedAt)) {
			last = e
		}
	}
	return last
}

// latestAttempts returns the newest attempt of each criterion; later attempts
// win ties so re-runs in the same instant still supersede.
func latestAttempts(d protocol.WorkDetail) map[string]protocol.VerificationAttempt {
	latest := map[string]protocol.VerificationAttempt{}
	for _, a := range d.Attempts {
		if a.CriterionNodeID == "" {
			continue
		}
		if prev, ok := latest[a.CriterionNodeID]; !ok || !a.StartedAt.Before(prev.StartedAt) {
			latest[a.CriterionNodeID] = a
		}
	}
	return latest
}

// Staleness returns the criteria (by node id) whose latest passing attempt no
// longer holds: a file changed after it, or it ran in a different environment
// than currentEnv. An empty currentEnv skips the environment rule.
func Staleness(d protocol.WorkDetail, currentEnv string) map[string]bool {
	out := map[string]bool{}
	lastChange := lastFileChange(d)
	env := map[string]string{}
	for _, e := range d.Evidence {
		env[e.ID] = e.EnvFingerprint
	}
	for id, a := range latestAttempts(d) {
		if a.Status != protocol.AttemptPassed {
			continue
		}
		switch {
		case lastChange != nil && lastChange.ObservedAt.After(a.FinishedAt):
			out[id] = true
		case currentEnv != "" && env[a.EvidenceID] != "" && env[a.EvidenceID] != currentEnv:
			out[id] = true
		}
	}
	return out
}

// ActiveEvidence returns the evidence that is still true: not stale, not on a
// superseded criterion, not an older attempt of a re-run criterion, and not
// backed by a vault object that is missing or corrupt. Evidence without a
// vault object (summary only) stays active.
func ActiveEvidence(d protocol.WorkDetail) []protocol.Evidence {
	superseded := map[string]bool{}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeCriterion && n.Status == StatusSuperseded {
			superseded[n.ID] = true
		}
	}
	older := map[string]bool{}
	latest := latestAttempts(d)
	for _, a := range d.Attempts {
		if a.CriterionNodeID != "" && a.EvidenceID != "" && latest[a.CriterionNodeID].ID != a.ID {
			older[a.EvidenceID] = true
		}
	}
	var out []protocol.Evidence
	for _, e := range d.Evidence {
		if e.StaleAt != nil || superseded[e.NodeID] || older[e.ID] || e.Availability == protocol.AvailUnavailable {
			continue
		}
		out = append(out, e)
	}
	return out
}
