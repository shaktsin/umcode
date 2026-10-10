package ctxcompiler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/vault"
	"github.com/shaktsin/umcode/internal/workflowgraph"
)

// redact masks secrets on their way into a packet. Rows written before the
// vault's redaction shipped still hold them unmasked.
func redact(s string) string {
	out, _ := vault.Redact([]byte(s))
	return string(out)
}

type graphPacket struct {
	p0IDs, p1IDs               map[string]bool
	p0, p1, evidence           string
	criteria, supporting, rows int
}

// designedPacket projects only live semantic state. Content is deliberately
// read through a small typed vocabulary; opaque prose and evidence bodies are
// never copied. Identity ordering makes timestamps and transcript order inert.
func designedPacket(d protocol.WorkDetail, stale map[string]bool, activeEvidence []protocol.Evidence) (graphPacket, error) {
	p := graphPacket{p0IDs: map[string]bool{}, p1IDs: map[string]bool{}}
	nodes := append([]protocol.WorkNode(nil), d.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	edges := append([]protocol.WorkEdge(nil), d.Edges...)
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.FromNodeID != b.FromNodeID {
			return a.FromNodeID < b.FromNodeID
		}
		if a.Relation != b.Relation {
			return a.Relation < b.Relation
		}
		return a.ToNodeID < b.ToNodeID
	})
	type metadata struct {
		Required *bool  `json:"required"`
		Blocking bool   `json:"blocking"`
		Gate     string `json:"gate_kind"`
		Rung     int    `json:"solution_rung"`
		Command  string `json:"command"`
	}
	byID := map[string]protocol.WorkNode{}
	meta := map[string]metadata{}
	live := func(n protocol.WorkNode) bool {
		return n.Status != protocol.StatusRejected && n.Status != protocol.StatusSuperseded && n.ValidUntil == nil && n.SupersededBy == ""
	}
	for _, n := range nodes {
		if n.ID == "" {
			return p, fmt.Errorf("missing node identity")
		}
		if _, exists := byID[n.ID]; exists {
			return p, fmt.Errorf("duplicate node identity")
		}
		byID[n.ID] = n
		if !live(n) || n.Kind == protocol.NodeMemoryCandidate {
			continue
		}
		var m metadata
		if len(n.Content) > 0 && (string(n.Content) == "null" || json.Unmarshal(n.Content, &m) != nil) {
			return p, fmt.Errorf("invalid node content")
		}
		meta[n.ID] = m
	}
	for _, e := range edges {
		if _, ok := byID[e.FromNodeID]; !ok {
			return p, fmt.Errorf("missing edge source")
		}
		if _, ok := byID[e.ToNodeID]; !ok {
			return p, fmt.Errorf("missing edge target")
		}
	}
	required := func(n protocol.WorkNode) bool {
		if r := meta[n.ID].Required; r != nil {
			return *r
		}
		return n.Kind == protocol.NodeCriterion
	}
	included, selected := map[string]bool{}, map[string]bool{}
	for _, n := range nodes {
		if !live(n) {
			continue
		}
		if n.Kind == protocol.NodeDecision && (n.Status == protocol.StatusApproved || n.Status == protocol.StatusProposed && required(n) && workflowgraph.ValidGate(meta[n.ID].Gate)) {
			included[n.ID] = true
			for _, e := range edges {
				if e.FromNodeID == n.ID && e.Relation == protocol.RelSelects {
					opt := byID[e.ToNodeID]
					if opt.Kind != protocol.NodeOption || !live(opt) || meta[opt.ID].Rung < 1 || meta[opt.ID].Rung > 6 {
						return p, fmt.Errorf("invalid selected option")
					}
					selected[opt.ID] = true
				}
			}
		}
		if n.Kind == protocol.NodeTask && n.Status != protocol.StatusCompleted || n.Kind == protocol.NodeCriterion || n.Kind == protocol.NodeUnknown && meta[n.ID].Blocking && n.Status != protocol.StatusResolved {
			included[n.ID] = true
		}
	}
	// Discover applicability before rendering. Requirements can depend on other
	// requirements, so a single identity-ordered pass cannot establish membership.
	supporting, applicable := map[string]bool{}, map[string]bool{}
	for id := range included {
		applicable[id] = true
	}
	for id := range selected {
		supporting[id], applicable[id] = true, true
	}
	for changed := true; changed; {
		changed = false
		for _, n := range nodes {
			if !live(n) || n.Kind != protocol.NodeRequirement || supporting[n.ID] {
				continue
			}
			linked, applies := false, required(n)
			for _, edge := range edges {
				if edge.FromNodeID == n.ID || edge.ToNodeID == n.ID {
					linked = true
					applies = applies || applicable[edge.FromNodeID] || applicable[edge.ToNodeID]
				}
			}
			if applies || !linked {
				supporting[n.ID], applicable[n.ID], changed = true, true, true
			}
		}
	}
	latest := latestAttempts(d)
	var p0, p1 strings.Builder
	fmt.Fprintf(&p0, "## Work\nGoal: %s\nWorkflow: depth=%s revision=%d\n", redact(d.Work.Goal), d.Work.WorkflowDepth, d.Work.Revision)
	var completion []string
	for _, n := range nodes {
		if !live(n) {
			continue
		}
		m := meta[n.ID]
		status := n.Status
		if n.Kind == protocol.NodeCriterion {
			// not_run deliberately leaves the persisted node status unchanged;
			// the latest attempt still governs criterion satisfaction.
			if a, ok := latest[n.ID]; ok {
				status = a.Status
			}
			if (status == protocol.AttemptPassed || status == protocol.StatusStale) && (stale[n.ID] || n.Status == protocol.StatusStale) {
				status = "needs re-run"
			}
		}
		if required(n) && (n.Kind == protocol.NodeCriterion || n.Kind == protocol.NodeTask || n.Kind == protocol.NodeDecision) {
			completion = append(completion, n.ID+"="+status)
		}
		if included[n.ID] {
			p.p0IDs["node:"+n.ID] = true
			fmt.Fprintf(&p0, "- %s %s revision=%d status=%s required=%t: %s", n.ID, n.Kind, n.Revision, status, required(n), redact(n.Title))
			switch n.Kind {
			case protocol.NodeDecision:
				if n.Status == protocol.StatusProposed {
					fmt.Fprintf(&p0, " gate=%s unresolved", m.Gate)
				}
			case protocol.NodeUnknown:
				p0.WriteString(" blocking=true")
			case protocol.NodeCriterion:
				p.criteria++
				fmt.Fprintf(&p0, " (`%s`)", redact(m.Command))
			}
			p0.WriteByte('\n')
		}
		if supporting[n.ID] {
			p.p1IDs["node:"+n.ID] = true
			fmt.Fprintf(&p1, "- %s %s: %s", n.ID, n.Kind, redact(n.Title))
			if selected[n.ID] {
				fmt.Fprintf(&p1, " rung=%d", m.Rung)
			}
			p1.WriteByte('\n')
			p.supporting++
		}
	}
	for id := range supporting {
		included[id] = true
	}
	for _, e := range edges {
		from, to := byID[e.FromNodeID], byID[e.ToNodeID]
		if !included[from.ID] || !live(to) {
			continue
		}
		keep := e.Relation == protocol.RelDependsOn && from.Kind == protocol.NodeTask && to.Kind == protocol.NodeTask && to.Status != protocol.StatusCompleted || e.Relation == protocol.RelSelects && from.Kind == protocol.NodeDecision || e.Relation == protocol.RelImplements && from.Kind == protocol.NodeTask || e.Relation == protocol.RelVerifies && from.Kind == protocol.NodeCriterion
		if keep {
			fmt.Fprintf(&p0, "- %s %s %s\n", e.FromNodeID, e.Relation, e.ToNodeID)
		}
	}
	p0.WriteString("Completion: all required criteria need fresh passes; all required tasks must be completed; required decisions need approved selected solutions with active supporting evidence; blocking unknowns must be resolved or explicitly accepted_risk.")
	if d.Work.WorkflowDepth != protocol.DepthDirect {
		p0.WriteString(" An approved supported solution and a task graph are required.")
	}
	if len(completion) > 0 {
		p0.WriteString(" State: " + strings.Join(completion, ", "))
	}
	p0.WriteByte('\n')
	p.p0 = p0.String()
	if p1.Len() > 0 {
		p.p1 = "Supporting graph (P1):\n" + p1.String()
	}
	refs := map[string]bool{}
	// Supports edges may attach a fact's evidence to a projected decision.
	// Follow provenance without carrying supporting fact or memory prose.
	evidenceNodes := map[string]bool{}
	for id := range included {
		evidenceNodes[id] = true
	}
	for changed := true; changed; {
		changed = false
		for _, edge := range edges {
			source := byID[edge.FromNodeID]
			if edge.Relation == protocol.RelSupports && evidenceNodes[edge.ToNodeID] && !evidenceNodes[source.ID] && live(source) && source.Kind != protocol.NodeMemoryCandidate {
				evidenceNodes[source.ID] = true
				changed = true
			}
		}
	}
	for _, n := range nodes {
		if evidenceNodes[n.ID] {
			for _, id := range n.EvidenceIDs {
				refs[id] = true
			}
		}
	}
	for _, a := range latest {
		if included[a.CriterionNodeID] && a.EvidenceID != "" {
			refs[a.EvidenceID] = true
		}
	}
	older := map[string]bool{}
	for _, a := range d.Attempts {
		if a.CriterionNodeID != "" && a.EvidenceID != "" && latest[a.CriterionNodeID].ID != a.ID {
			older[a.EvidenceID] = true
		}
	}
	allowed := map[string]protocol.Evidence{}
	if activeEvidence == nil {
		activeEvidence = d.Evidence
	}
	for _, e := range activeEvidence {
		if e.StaleAt == nil && !older[e.ID] {
			allowed[e.ID] = e
			if evidenceNodes[e.NodeID] {
				refs[e.ID] = true
			}
		}
	}
	ids := make([]string, 0, len(refs))
	for id := range refs {
		if _, ok := allowed[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var evidence strings.Builder
	for _, id := range ids {
		p.p1IDs["evidence:"+id] = true
		fmt.Fprintf(&evidence, "- %s", id)
		if uri := allowed[id].SourceURI; uri != "" {
			fmt.Fprintf(&evidence, " uri=%s", redact(uri))
		}
		evidence.WriteByte('\n')
	}
	p.rows = len(ids)
	if p.rows > 0 {
		p.evidence = "Evidence references (P1):\n" + evidence.String()
	}
	return p, nil
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
// status, the unresolved failures, and the files this work changed. stale marks
// criteria whose stored status still reads as a pass but no longer holds.
func workPacket(d protocol.WorkDetail, stale map[string]bool) (string, int) {
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
		if n.Kind != protocol.NodeCriterion || n.Status == protocol.StatusSuperseded {
			continue
		}
		title, command := redact(n.Title), redact(criterionCommand(n))
		status := n.Status
		isStale := status == protocol.StatusStale || stale[n.ID]
		if isStale {
			status = "needs re-run, a change landed after it last succeeded"
		}
		criteria = append(criteria, "- "+title+" — "+status+" (`"+command+"`)")
		// Only a criterion whose latest attempt did not pass has a failure to
		// report. A stale pass needs re-running, which the status line already
		// says; its output was a success and belongs nowhere near Unresolved.
		a, ok := latest[n.ID]
		if ok && a.Status != protocol.AttemptPassed {
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
