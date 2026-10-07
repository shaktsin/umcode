package work

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

// MaxDepth enforces monotonic workflow escalation.
func MaxDepth(a, b string) string {
	rank := map[string]int{protocol.DepthDirect: 0, protocol.DepthGuided: 1, protocol.DepthDesigned: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// InitialDepth recognizes explicit design requests without a model call.
func InitialDepth(userText string) string {
	text := strings.TrimSpace(strings.ToLower(userText))
	if explicitDesignRequest.MatchString(text) {
		return protocol.DepthDesigned
	}
	if !explanatoryRequest.MatchString(text) && mutationIntent.MatchString(text) && highRiskScope.MatchString(text) {
		return protocol.DepthDesigned
	}
	return protocol.DepthDirect
}

var explicitDesignRequest = regexp.MustCompile(`^(?:(?:please|can you|could you|help me)\s+)?(?:design|redesign|architect)\b|\b(?:architectural|architecture|design)\s+(?:decision|plan)\b|\b(?:propose|choose|decide|create|build)\b.*\b(?:architecture|architectural)\b`)
var explanatoryRequest = regexp.MustCompile(`^(?:what|why|how|explain|describe|is|are|does|tell me about)\b`)
var mutationIntent = regexp.MustCompile(`\b(?:change|add|update|modify|remove|delete|migrate|implement|replace|introduce|design|architect)\b`)
var highRiskScope = regexp.MustCompile(`\b(?:public api|protocol|schema|file[- ]formats?|database migration|database compatibility|migration|auth|authentication|authorization|security|secrets?|trust[-_ ]boundary|billing|payments?|money movement|destructive|irreversible|recovery)\b`)

// ObservedDepth recognizes mutation risk and affected ownership areas.
func ObservedDepth(current string, d protocol.WorkDetail, o Observation) string {
	if o.Err != "" && !isVerificationTool(o.Tool) {
		return current
	}
	if !EscalatesToGuided(o.Tool, o.Risk) {
		return current
	}
	depth := MaxDepth(current, protocol.DepthGuided)
	if o.Tool != "file.write" && o.Tool != "file.edit" && o.Tool != "shell.run" && o.Tool != "exec.start" && o.Tool != "exec.write" {
		return depth
	}
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(o.Args, &args)
	path, _ := normalizePath(o.Root, args.Path)
	path = strings.ToLower(path)
	for _, signal := range []string{"migrations/", "internal/protocol/", "schema", "public/api", "file_format", "security", "auth", "secret", "trust_boundary", "trust-boundary", "billing", "payment", "destructive", "recovery"} {
		if strings.Contains(path, signal) {
			return protocol.DepthDesigned
		}
	}
	areas := map[string]bool{}
	add := func(p string) {
		parts := strings.Split(strings.TrimPrefix(p, "./"), "/")
		if len(parts) > 1 {
			area := parts[0]
			if area == "internal" || area == "cmd" {
				area += "/" + parts[1]
			}
			areas[area] = true
		}
	}
	for _, e := range d.Evidence {
		if e.Kind == protocol.EvidenceFileChange {
			add(e.SourceURI)
		}
	}
	if args.Path != "" {
		add(path)
	}
	if len(areas) >= 2 {
		return protocol.DepthDesigned
	}
	return depth
}

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
