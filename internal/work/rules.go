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
	case "shell.run":
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
	var lastChange *protocol.Evidence
	for i := range d.Evidence {
		e := &d.Evidence[i]
		if e.Kind == protocol.EvidenceFileChange && (lastChange == nil || e.ObservedAt.After(lastChange.ObservedAt)) {
			lastChange = e
		}
	}
	latest := map[string]protocol.VerificationAttempt{}
	for _, a := range d.Attempts {
		if a.CriterionNodeID == "" {
			continue
		}
		if prev, ok := latest[a.CriterionNodeID]; !ok || !a.StartedAt.Before(prev.StartedAt) {
			latest[a.CriterionNodeID] = a
		}
	}
	var out []string
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeCriterion || n.Status == StatusSuperseded {
			continue
		}
		a, ok := latest[n.ID]
		switch {
		case !ok, a.Status != protocol.AttemptPassed:
			out = append(out, n.Title)
		case lastChange != nil && lastChange.ObservedAt.After(a.FinishedAt):
			out = append(out, n.Title)
		}
	}
	return out
}
