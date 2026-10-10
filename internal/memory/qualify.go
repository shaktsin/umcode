// Package memory qualifies durable project knowledge without performing I/O.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/work"
)

type QualifyInput struct {
	Detail                     protocol.WorkDetail
	Candidate                  protocol.WorkNode
	ProjectRoot, FinalRevision string
	ActiveMemories             []protocol.ProjectMemory
}

// Proposal contains canonical guidance and provenance IDs, never evidence bodies.
// Its slices are owned by the result and do not alias the input snapshot.
type Proposal struct {
	WorkID, CandidateNodeID, SemanticKey, Category, Text, SourceRevision string
	ScopePaths, EvidenceIDs                                              []string
	ReplacesMemory                                                       string
}

type Outcome struct{ Status, Reason string }

// Reasons are metadata-only enums. Never interpolate candidate or evidence text.
const (
	ReasonWorkNotCompleted    = "work_not_completed"
	ReasonProjectRequired     = "project_required"
	ReasonCandidateInvalid    = "candidate_invalid"
	ReasonCandidateInactive   = "candidate_inactive"
	ReasonSourceStale         = "source_stale"
	ReasonSourceAmbiguous     = "source_ambiguous"
	ReasonSourceUnsupported   = "source_unsupported"
	ReasonEvidenceStale       = "evidence_stale"
	ReasonVerificationStale   = "verification_stale"
	ReasonRevisionMismatch    = "revision_mismatch"
	ReasonContentInvalid      = "content_invalid"
	ReasonContentGeneric      = "content_generic"
	ReasonContentUnsupported  = "content_unsupported"
	ReasonContentTemporary    = "content_temporary"
	ReasonContentBranchLocal  = "content_branch_local"
	ReasonAlreadyCurrent      = "already_current"
	ReasonSemanticConflict    = "semantic_conflict"
	ReasonReplacementConflict = "replacement_conflict"
)

func outcome(status, reason string) (Proposal, Outcome) {
	return Proposal{}, Outcome{Status: status, Reason: reason}
}

// Qualify returns a proposal and zero outcome when eligible. A nonzero outcome
// always has a zero proposal. The caller supplies a canonical completed snapshot;
// the function reads no store, files, environment, or clock.
func Qualify(in QualifyInput) (Proposal, Outcome) {
	d, candidate := in.Detail, in.Candidate
	if d.Work.Status != protocol.WorkCompleted {
		return outcome(protocol.MemoryOutcomeStale, ReasonWorkNotCompleted)
	}
	if d.Work.ProjectID == "" || !filepath.IsAbs(in.ProjectRoot) {
		return outcome(protocol.MemoryOutcomeRejected, ReasonProjectRequired)
	}
	if candidate.Kind != protocol.NodeMemoryCandidate || candidate.ID == "" || candidate.WorkID != d.Work.ID {
		return outcome(protocol.MemoryOutcomeRejected, ReasonCandidateInvalid)
	}
	if candidate.Status != protocol.StatusPending || !active(candidate) {
		return outcome(protocol.MemoryOutcomeStale, ReasonCandidateInactive)
	}
	nodes := map[string]protocol.WorkNode{}
	for _, n := range d.Nodes {
		if _, exists := nodes[n.ID]; exists {
			return outcome(protocol.MemoryOutcomeRejected, ReasonCandidateInvalid)
		}
		nodes[n.ID] = n
	}
	if canonical, ok := nodes[candidate.ID]; !ok || !reflect.DeepEqual(canonical, candidate) {
		return outcome(protocol.MemoryOutcomeStale, ReasonCandidateInactive)
	}
	c, err := work.DecodeMemoryCandidate(candidate)
	if err != nil {
		return outcome(protocol.MemoryOutcomeRejected, ReasonContentInvalid)
	}
	if c.SourceRevision == "" || in.FinalRevision == "" || c.SourceRevision != in.FinalRevision {
		return outcome(protocol.MemoryOutcomeStale, ReasonRevisionMismatch)
	}
	if !safeAbsolutePaths(c.Text, in.ProjectRoot) {
		return outcome(protocol.MemoryOutcomeRejected, ReasonContentInvalid)
	}
	if reason := durabilityReason(c); reason != "" {
		return outcome(protocol.MemoryOutcomeRejected, reason)
	}
	var source protocol.WorkNode
	count := 0
	for _, edge := range d.Edges {
		if edge.FromNodeID != candidate.ID || edge.Relation != protocol.RelCandidateFor {
			continue
		}
		n, ok := nodes[edge.ToNodeID]
		if edge.WorkID != d.Work.ID || !ok || n.WorkID != d.Work.ID {
			return outcome(protocol.MemoryOutcomeStale, ReasonSourceStale)
		}
		if active(n) {
			source = n
			count++
		}
	}
	if count > 1 {
		return outcome(protocol.MemoryOutcomeConflicted, ReasonSourceAmbiguous)
	}
	if count == 0 {
		return outcome(protocol.MemoryOutcomeStale, ReasonSourceStale)
	}
	// work.update cannot create NodeFact, so this kind is engine-owned.
	if !(source.Kind == protocol.NodeFact && source.Status == "active" || source.Kind == protocol.NodeDecision && source.Status == protocol.StatusApproved) || c.Category == protocol.MemoryCategoryApprovedDecision && source.Kind != protocol.NodeDecision {
		return outcome(protocol.MemoryOutcomeRejected, ReasonSourceUnsupported)
	}
	if source.Kind == protocol.NodeFact && !durableFact(source, d) {
		return outcome(protocol.MemoryOutcomeRejected, ReasonSourceUnsupported)
	}
	for _, edge := range d.Edges {
		if edge.WorkID != d.Work.ID || edge.Relation != protocol.RelContradicts {
			continue
		}
		if edge.ToNodeID == candidate.ID || edge.ToNodeID == source.ID {
			if n, ok := nodes[edge.FromNodeID]; ok && n.WorkID == d.Work.ID && active(n) {
				return outcome(protocol.MemoryOutcomeRejected, ReasonSourceUnsupported)
			}
		}
		if edge.FromNodeID == candidate.ID || edge.FromNodeID == source.ID {
			if n, ok := nodes[edge.ToNodeID]; ok && n.WorkID == d.Work.ID && active(n) {
				return outcome(protocol.MemoryOutcomeRejected, ReasonSourceUnsupported)
			}
		}
	}
	evidence := map[string]protocol.Evidence{}
	for _, e := range work.ActiveEvidence(d) {
		if e.WorkID == d.Work.ID && (e.VaultHash == "" || e.Availability == protocol.AvailAvailable) && evidenceRevisionCurrent(e, in, nodes) {
			evidence[e.ID] = e
		}
	}
	if len(candidate.EvidenceIDs) == 0 || len(candidate.EvidenceIDs) > work.MaxNodeEvidenceIDs {
		return outcome(protocol.MemoryOutcomeStale, ReasonEvidenceStale)
	}
	seen := map[string]bool{}
	for _, id := range candidate.EvidenceIDs {
		if _, ok := evidence[id]; !ok || seen[id] {
			return outcome(protocol.MemoryOutcomeStale, ReasonEvidenceStale)
		}
		seen[id] = true
	}
	if !verified(in, nodes, evidence, source.ID) {
		return outcome(protocol.MemoryOutcomeStale, ReasonVerificationStale)
	}
	var current []protocol.ProjectMemory
	for _, row := range in.ActiveMemories {
		if row.ProjectID == d.Work.ProjectID && row.SemanticKey == c.SemanticKey && row.Status == protocol.MemoryStatusActive {
			current = append(current, row)
		}
	}
	if len(current) > 1 {
		return outcome(protocol.MemoryOutcomeConflicted, ReasonSemanticConflict)
	}
	if c.ReplacesMemory != "" {
		if len(current) != 1 || current[0].ID != c.ReplacesMemory || !owned(current[0]) {
			return outcome(protocol.MemoryOutcomeConflicted, ReasonReplacementConflict)
		}
	}
	if len(current) == 1 {
		row := current[0]
		if validMemory(row) && row.Category == c.Category && row.Text == c.Text {
			return outcome(protocol.MemoryOutcomePromoted, ReasonAlreadyCurrent)
		}
		if c.ReplacesMemory == "" {
			return outcome(protocol.MemoryOutcomeConflicted, ReasonSemanticConflict)
		}
	}
	evidenceIDs := append([]string(nil), candidate.EvidenceIDs...)
	sort.Strings(evidenceIDs)
	return Proposal{WorkID: d.Work.ID, CandidateNodeID: candidate.ID, SemanticKey: c.SemanticKey, Category: c.Category, Text: c.Text, SourceRevision: c.SourceRevision, ScopePaths: append([]string(nil), c.ScopePaths...), EvidenceIDs: evidenceIDs, ReplacesMemory: c.ReplacesMemory}, Outcome{}
}

func active(n protocol.WorkNode) bool {
	return n.ValidUntil == nil && n.SupersededBy == "" && n.Status != protocol.StatusRejected && n.Status != protocol.StatusSuperseded && n.Status != protocol.StatusStale
}

func durableFact(n protocol.WorkNode, d protocol.WorkDetail) bool {
	// Engine-owned includes failure observations; their kind/status alone does
	// not establish durable knowledge, even if later unrelated checks pass.
	var content map[string]json.RawMessage
	if len(n.Content) > 0 && json.Unmarshal(n.Content, &content) != nil {
		return false
	}
	if _, failure := content["error"]; failure {
		return false
	}
	for _, e := range d.Evidence {
		if e.WorkID != d.Work.ID || e.Kind != protocol.EvidenceToolError {
			continue
		}
		if e.NodeID == n.ID {
			return false
		}
		for _, id := range n.EvidenceIDs {
			if e.ID == id {
				return false
			}
		}
	}
	return true
}

func evidenceRevisionCurrent(e protocol.Evidence, in QualifyInput, nodes map[string]protocol.WorkNode) bool {
	switch e.Kind {
	case protocol.EvidenceDiscovery:
		// Successful discovery records the tool argument hash, not a workspace revision.
		return true
	case protocol.EvidenceWorkflowApproval:
		// Approval records the revision before the approval transition increments it.
		for _, edge := range in.Detail.Edges {
			if edge.WorkID != in.Detail.Work.ID || edge.FromNodeID != e.NodeID || edge.Relation != protocol.RelVerifies {
				continue
			}
			n, ok := nodes[edge.ToNodeID]
			if ok && n.WorkID == in.Detail.Work.ID && active(n) && n.Revision > 1 && (n.Kind == protocol.NodeDecision && n.Status == protocol.StatusApproved || n.Kind == protocol.NodeUnknown && n.Status == protocol.StatusAcceptedRisk) && e.SourceRevision == n.ID+":"+strconv.Itoa(n.Revision-1) {
				return true
			}
		}
		return false
	default:
		return e.SourceRevision == "" || e.SourceRevision == in.FinalRevision
	}
}

func verified(in QualifyInput, nodes map[string]protocol.WorkNode, evidence map[string]protocol.Evidence, sourceID string) bool {
	d := in.Detail
	applicable := map[string]bool{}
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeCriterion || n.WorkID != d.Work.ID || n.Status == protocol.StatusSuperseded || n.ValidUntil != nil || n.SupersededBy != "" {
			continue
		}
		var c struct {
			Required *bool `json:"required"`
		}
		_ = json.Unmarshal(n.Content, &c)
		if c.Required == nil || *c.Required {
			applicable[n.ID] = true
		}
	}
	for _, edge := range d.Edges {
		if edge.Relation == protocol.RelVerifies && (edge.ToNodeID == in.Candidate.ID || edge.ToNodeID == sourceID) {
			n, ok := nodes[edge.FromNodeID]
			if edge.WorkID != d.Work.ID || !ok || n.WorkID != d.Work.ID || n.Kind != protocol.NodeCriterion {
				return false
			}
			if n.Status != protocol.StatusSuperseded && n.ValidUntil == nil && n.SupersededBy == "" {
				applicable[n.ID] = true
			}
		}
	}
	latest := map[string]protocol.VerificationAttempt{}
	for _, a := range d.Attempts {
		if old, ok := latest[a.CriterionNodeID]; !ok || !a.StartedAt.Before(old.StartedAt) {
			latest[a.CriterionNodeID] = a
		}
	}
	fingerprints := map[string]protocol.Fingerprint{}
	for _, f := range d.Fingerprints {
		fingerprints[f.ID] = f
	}
	stale := work.Staleness(d, "")
	for id := range applicable {
		n := nodes[id]
		a, ok := latest[id]
		e, hasEvidence := evidence[a.EvidenceID]
		if !active(n) || n.Status != protocol.AttemptPassed || !ok || a.WorkID != d.Work.ID || a.Status != protocol.AttemptPassed || a.FinishedAt.Before(a.StartedAt) || !hasEvidence {
			return false
		}
		if e.Kind == protocol.EvidenceWorkflowApproval {
			var c struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(n.Content, &c)
			if e.NodeID != id || c.Command != "workflow:approval" || a.Command != c.Command || a.CheckType != "workflow_approval" {
				return false
			}
			continue
		}
		if stale[id] {
			return false
		}
		if a.FingerprintID != "" {
			f, ok := fingerprints[a.FingerprintID]
			if !ok || f.WorkID != d.Work.ID || f.Value != in.FinalRevision {
				return false
			}
		} else if e.SourceRevision != in.FinalRevision {
			return false
		}
	}
	return len(applicable) > 0
}

func owned(row protocol.ProjectMemory) bool { return !row.UserOwned && validMemory(row) }

func validMemory(row protocol.ProjectMemory) bool {
	sum := sha256.Sum256([]byte(row.Text))
	target := path.Clean(row.TargetPath)
	return row.ID != "" && row.SupersededBy == "" && row.Status == protocol.MemoryStatusActive && row.TextHash == hex.EncodeToString(sum[:]) && !path.IsAbs(target) && target != ".." && !strings.HasPrefix(target, "../") && !strings.Contains(target, `\`) && path.Base(target) == "UMCODE.md" && strings.HasPrefix(row.Text, "- ") && !strings.ContainsAny(row.Text, "\r\n")
}

var (
	genericContent     = regexp.MustCompile(`(?i)\b(best practices|clean code|always (write|add) tests|follow (the )?guidelines|be careful)\b`)
	unsupportedContent = regexp.MustCompile(`(?i)\b(probably|perhaps|maybe|guess|i think|user prefers|user likes|inferred preference)\b|^(PASS|FAIL|panic|error):|^ok\s+\S+\s+\d+(\.\d+)?s\b`)
	temporaryContent   = regexp.MustCompile(`(?i)\b(currently|temporarily|for now|today|yesterday|this task|this turn|task (is )?(completed|done)|tests? (currently )?(fail|failed|failing))\b`)
	branchContent      = regexp.MustCompile(`(?i)\b(on (the )?branch|branch[- ]local|feature/|this branch|current branch|pull request|pr #[0-9])`)
	absoluteTextPath   = regexp.MustCompile("(?:^|[\\s`\"'(\\[=])(/[^\\s`\"'<>)]*)")
)

func durabilityReason(c work.MemoryCandidateContent) string {
	text := strings.TrimPrefix(c.Text, "- ")
	switch {
	case genericContent.MatchString(text):
		return ReasonContentGeneric
	case branchContent.MatchString(text):
		return ReasonContentBranchLocal
	case temporaryContent.MatchString(text):
		return ReasonContentTemporary
	case unsupportedContent.MatchString(text):
		return ReasonContentUnsupported
	}
	// A typed category alone cannot make unscoped general prose project-specific.
	lower := strings.ToLower(text)
	if len(c.ScopePaths) == 0 && !strings.Contains(lower, "project") && !strings.Contains(lower, "repository") && !strings.Contains(text, "/") && !strings.Contains(text, "`") {
		return ReasonContentGeneric
	}
	return ""
}

func safeAbsolutePaths(text, root string) bool {
	for _, match := range absoluteTextPath.FindAllStringSubmatch(text, -1) {
		target := strings.TrimRight(match[1], ",;:")
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}
	for _, token := range strings.Fields(text) {
		token = strings.Trim(token, "`\"'()[]{},;:")
		if strings.HasPrefix(token, "~/") || len(token) > 2 && token[1] == ':' && (token[2] == '/' || token[2] == '\\') {
			return false
		}
	}
	return true
}
