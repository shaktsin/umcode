package work

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/vault"
	"github.com/shaktsin/umcode/internal/workflowgraph"
)

// Graph batch bounds are byte limits, shared by validation and tool schemas.
const (
	MaxNodeChanges         = 32
	MaxEdgeChanges         = 64
	MaxNodeEvidenceIDs     = 16
	MaxClientRefBytes      = 64
	MaxNodeTitleBytes      = 512
	MaxRationaleBytes      = 2048
	MaxNodeContentBytes    = 8192
	MaxWorkUpdateBytes     = 65536
	MaxCandidateKeyBytes   = 128
	MaxCandidateTextBytes  = 512
	MaxCandidateScopePaths = 16
	MaxCandidateScopeBytes = 512
)

const (
	GatePublicContract     = "public_contract"
	GatePersistedSchema    = "persisted_schema"
	GateSecurity           = "security"
	GateDestructive        = "destructive"
	GateBilling            = "billing"
	GateArchitectureChoice = "architecture_choice"
	GateAcceptedRisk       = "accepted_risk"
)

// Semantic content is extensible; these fields have deterministic meaning.
type OptionContent struct {
	SolutionRung int `json:"solution_rung"`
}
type DecisionContent struct {
	Required bool   `json:"required"`
	GateKind string `json:"gate_kind,omitempty"`
}
type RequirementContent struct {
	Required bool `json:"required"`
}
type TaskContent struct {
	Required bool `json:"required"`
}
type UnknownContent struct {
	Blocking bool `json:"blocking"`
}
type MemoryCandidateContent struct {
	Category       string   `json:"category"`
	SemanticKey    string   `json:"semantic_key"`
	Text           string   `json:"text"`
	ScopePaths     []string `json:"scope_paths,omitempty"`
	SourceRevision string   `json:"source_revision"`
	ReplacesMemory string   `json:"replaces_memory,omitempty"`
	Scope          string   `json:"scope,omitempty"` // Legacy input only.
}

// DecodeMemoryCandidate validates and canonicalizes without filesystem I/O.
// Filesystem existence and symlink containment are placement predicates.
func DecodeMemoryCandidate(n protocol.WorkNode) (MemoryCandidateContent, error) {
	var c MemoryCandidateContent
	fields, err := decodeContent(n)
	if err != nil {
		return c, err
	}
	if _, legacy := fields["scope"]; legacy {
		if _, current := fields["scope_paths"]; current {
			return c, invalid("nodes.candidate.scope_paths", "invalid")
		}
	}
	for key, value := range fields {
		switch key {
		case "category", "semantic_key", "text", "scope_paths", "source_revision", "replaces_memory", "scope":
		default:
			return c, invalid("nodes.candidate", "invalid")
		}
		if string(value) == "null" {
			return c, invalid("nodes.candidate", "invalid")
		}
		if key == "scope_paths" {
			var paths []string
			if json.Unmarshal(value, &paths) != nil {
				return c, invalid("nodes.candidate.scope_paths", "invalid")
			}
			for _, item := range paths {
				if item == "" {
					return c, invalid("nodes.candidate.scope_paths", "invalid")
				}
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(n.Content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return c, invalid("nodes.candidate", "invalid")
	}
	switch c.Category {
	case protocol.MemoryCategoryCapability, protocol.MemoryCategoryCommand, protocol.MemoryCategoryBoundary, protocol.MemoryCategoryInvariant, protocol.MemoryCategoryConvention, protocol.MemoryCategoryPath, protocol.MemoryCategoryApprovedDecision:
	default:
		return c, invalid("nodes.candidate.category", "invalid")
	}
	if len(c.SemanticKey) > MaxCandidateKeyBytes || !validID(c.SemanticKey) || len(c.Text) > MaxCandidateTextBytes || !candidateString(c.Text) || !candidateString(c.SourceRevision) || c.ReplacesMemory != "" && !validID(c.ReplacesMemory) {
		return c, invalid("nodes.candidate", "invalid")
	}
	if containsSecret(n.Content) || containsSecret(n.Title) {
		return c, invalid("nodes.candidate.text", "invalid")
	}
	text := strings.TrimSpace(c.Text)
	if strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "* ") || strings.HasPrefix(text, "+ ") {
		text = strings.TrimSpace(text[2:])
	}
	if text == "" || strings.ContainsAny(text, "<>") || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") || strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "* ") || strings.HasPrefix(text, "+ ") || numberedBullet.MatchString(text) {
		return c, invalid("nodes.candidate.text", "invalid")
	}
	c.Text = "- " + text
	if _, legacy := fields["scope"]; legacy {
		if strings.TrimSpace(c.Scope) == "" {
			return c, invalid("nodes.candidate.scope", "invalid")
		}
		c.ScopePaths = []string{c.Scope}
	}
	if len(c.ScopePaths) > MaxCandidateScopePaths {
		return c, invalid("nodes.candidate.scope_paths", "limit")
	}
	paths := make([]string, 0, len(c.ScopePaths))
	seen := map[string]bool{}
	for _, scope := range c.ScopePaths {
		if len(scope) > MaxCandidateScopeBytes || !candidateString(scope) {
			return c, invalid("nodes.candidate.scope_paths", "invalid")
		}
		scope = strings.TrimSpace(scope)
		if scope == "" || path.IsAbs(scope) || strings.HasPrefix(scope, "~") || strings.Contains(scope, `\`) || len(scope) > 1 && scope[1] == ':' {
			return c, invalid("nodes.candidate.scope_paths", "invalid")
		}
		scope = path.Clean(scope)
		if scope == ".." || strings.HasPrefix(scope, "../") || seen[scope] {
			return c, invalid("nodes.candidate.scope_paths", "invalid")
		}
		seen[scope] = true
		paths = append(paths, scope)
	}
	sort.Strings(paths)
	c.ScopePaths = paths
	c.Scope = ""
	return c, nil
}

func candidateString(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// ValidationError gives consumers a compact field/class without graph contents.
type ValidationError struct {
	Field string
	Code  string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Code }
func invalid(field, code string) error   { return &ValidationError{Field: field, Code: code} }

var graphID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
var numberedBullet = regexp.MustCompile(`^\d+[.)]\s`)

func validID(id string) bool { return len(id) > 0 && len(id) <= 128 && graphID.MatchString(id) }
func validGate(kind string) bool {
	return workflowgraph.ValidGate(kind)
}
func active(n protocol.WorkNode) bool {
	return n.Status != protocol.StatusRejected && n.Status != protocol.StatusSuperseded && n.ValidUntil == nil && n.SupersededBy == ""
}

func decodeContent(n protocol.WorkNode) (map[string]json.RawMessage, error) {
	if len(n.Content) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(n.Content, &fields); err != nil || fields == nil {
		return nil, invalid("nodes.content", "invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(n.Content))
	_, _ = decoder.Token()
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, invalid("nodes.content", "invalid")
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, invalid("nodes.content", "invalid")
		}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Unmarshal the known fields with their concrete types; unknown prose remains extensible.
	for _, key := range keys {
		value := fields[key]
		lower := strings.ToLower(key)
		if lower != key && (lower == "required" || lower == "blocking" || lower == "gate_kind" || lower == "solution_rung" || lower == "category" || lower == "semantic_key" || lower == "text" || lower == "scope") {
			return nil, invalid("nodes.content", "invalid")
		}
		switch key {
		case "required":
			var b bool
			if n.Kind != protocol.NodeRequirement && n.Kind != protocol.NodeDecision && n.Kind != protocol.NodeTask && n.Kind != protocol.NodeCriterion || string(value) == "null" || json.Unmarshal(value, &b) != nil {
				return nil, invalid("nodes.content.required", "invalid")
			}
		case "blocking":
			var b bool
			if n.Kind != protocol.NodeUnknown || string(value) == "null" || json.Unmarshal(value, &b) != nil {
				return nil, invalid("nodes.content.blocking", "invalid")
			}
		case "gate_kind":
			var s string
			if n.Kind != protocol.NodeDecision || string(value) == "null" || json.Unmarshal(value, &s) != nil || s != "" && !validGate(s) {
				return nil, invalid("nodes.content.gate_kind", "invalid")
			}
		case "solution_rung":
			var rung int
			if n.Kind != protocol.NodeOption || json.Unmarshal(value, &rung) != nil {
				return nil, invalid("nodes.content.solution_rung", "invalid")
			}
		}
	}
	return fields, nil
}
func required(n protocol.WorkNode) bool {
	var c struct {
		Required *bool `json:"required"`
	}
	_ = json.Unmarshal(n.Content, &c)
	if c.Required != nil {
		return *c.Required
	}
	return n.Kind == protocol.NodeCriterion
}
func blocking(n protocol.WorkNode) bool {
	var c UnknownContent
	_ = json.Unmarshal(n.Content, &c)
	return c.Blocking
}
func gateKind(n protocol.WorkNode) string {
	var c DecisionContent
	_ = json.Unmarshal(n.Content, &c)
	return c.GateKind
}
func rung(n protocol.WorkNode) int {
	var c OptionContent
	_ = json.Unmarshal(n.Content, &c)
	return c.SolutionRung
}

func initialStatus(c protocol.WorkNodeChange) (string, error) {
	status := c.ToStatus
	switch c.Kind {
	case protocol.NodeRequirement, protocol.NodeNonGoal, protocol.NodeOption:
		if status == "" {
			status = "active"
		}
		if status != "active" {
			return "", invalid("nodes.to_status", "transition")
		}
	case protocol.NodeDecision:
		if status == "" {
			status = protocol.StatusProposed
		}
		if status != protocol.StatusProposed && status != protocol.StatusApproved {
			return "", invalid("nodes.to_status", "transition")
		}
	case protocol.NodeTask:
		if status == "" || status == protocol.StatusReady {
			status = protocol.StatusPending
		}
		if status != protocol.StatusPending {
			return "", invalid("nodes.to_status", "transition")
		}
	case protocol.NodeUnknown:
		if status == "" {
			status = protocol.StatusOpen
		}
		if status != protocol.StatusOpen {
			return "", invalid("nodes.to_status", "transition")
		}
	case protocol.NodeMemoryCandidate:
		if status == "" {
			status = protocol.StatusPending
		}
		if status != protocol.StatusPending {
			return "", invalid("nodes.to_status", "transition")
		}
	default:
		return "", invalid("nodes.kind", "invalid")
	}
	return status, nil
}

func validateTransition(n protocol.WorkNode, to string) error {
	if n.Kind == protocol.NodeDecision && gateKind(n) != "" && n.Status == protocol.StatusProposed && (to == protocol.StatusApproved || to == protocol.StatusRejected) || n.Kind == protocol.NodeUnknown && to == protocol.StatusAcceptedRisk {
		return invalid("nodes.to_status", "approval_required")
	}
	allowed := false
	switch n.Kind {
	case protocol.NodeDecision:
		allowed = n.Status == protocol.StatusProposed && (to == protocol.StatusApproved || to == protocol.StatusRejected) || (n.Status == protocol.StatusApproved || n.Status == protocol.StatusRejected) && to == protocol.StatusSuperseded
	case protocol.NodeTask:
		allowed = n.Status == protocol.StatusPending && to == protocol.StatusReady || n.Status == protocol.StatusReady && to == protocol.StatusInProgress || n.Status == protocol.StatusInProgress && (to == protocol.StatusCompleted || to == protocol.StatusFailed || to == protocol.StatusBlocked) || (n.Status == protocol.StatusBlocked || n.Status == protocol.StatusFailed) && to == protocol.StatusSuperseded
	case protocol.NodeUnknown:
		allowed = n.Status == protocol.StatusOpen && to == protocol.StatusResolved
	case protocol.NodeMemoryCandidate:
		allowed = n.Status == protocol.StatusPending && (to == protocol.StatusConflicted || to == protocol.StatusStale || to == protocol.StatusRejected)
	case protocol.NodeRequirement, protocol.NodeNonGoal, protocol.NodeOption:
		allowed = n.Status == "active" && to == protocol.StatusSuperseded
	}
	if !allowed {
		return invalid("nodes.to_status", "transition")
	}
	return nil
}

// PrepareUpdate validates a complete batch against a private graph projection.
// IDs derive from work/revision/ref so equal inputs prepare equal deltas.
func PrepareUpdate(detail protocol.WorkDetail, req protocol.WorkUpdateRequest, now time.Time) (protocol.PreparedWorkUpdate, error) {
	return prepareUpdate(detail, req, now, false)
}
func prepareUpdate(detail protocol.WorkDetail, req protocol.WorkUpdateRequest, now time.Time, automatic bool) (protocol.PreparedWorkUpdate, error) {
	var p protocol.PreparedWorkUpdate
	if len(req.Nodes) > MaxNodeChanges {
		return p, invalid("nodes", "limit")
	}
	if len(req.Edges) > MaxEdgeChanges {
		return p, invalid("edges", "limit")
	}
	if len(req.Rationale) > MaxRationaleBytes {
		return p, invalid("rationale", "limit")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return p, invalid("request", "invalid")
	}
	if len(raw) > MaxWorkUpdateBytes {
		return p, invalid("request", "limit")
	}
	if !validID(req.WorkID) {
		return p, invalid("work_id", "invalid")
	}
	if req.WorkID != detail.Work.ID {
		return p, invalid("work_id", "foreign")
	}
	if req.ExpectedRevision <= 0 || req.ExpectedRevision != detail.Work.Revision {
		return p, invalid("expected_revision", "stale")
	}
	if detail.Work.Status != protocol.WorkOpen {
		return p, invalid("work", "invalid")
	}
	depth := detail.Work.WorkflowDepth
	if req.WorkflowDepth != "" {
		if req.WorkflowDepth != protocol.DepthGuided && req.WorkflowDepth != protocol.DepthDesigned || MaxDepth(depth, req.WorkflowDepth) != req.WorkflowDepth {
			return p, invalid("workflow_depth", "invalid")
		}
		depth = req.WorkflowDepth
	}
	if depth == protocol.DepthDirect || depth != protocol.DepthGuided && depth != protocol.DepthDesigned {
		return p, invalid("workflow_depth", "invalid")
	}
	p = protocol.PreparedWorkUpdate{WorkID: req.WorkID, ExpectedRevision: req.ExpectedRevision, WorkflowDepth: depth}
	d := detail
	d.Work.WorkflowDepth = depth
	d.Nodes = append([]protocol.WorkNode(nil), detail.Nodes...)
	d.Edges = append([]protocol.WorkEdge(nil), detail.Edges...)
	nodes := map[string]int{}
	refs := map[string]string{}
	evidence := map[string]protocol.Evidence{}
	for i, n := range d.Nodes {
		if n.WorkID != req.WorkID {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.work_id", "foreign")
		}
		if !validID(n.ID) {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.id", "invalid")
		}
		if _, ok := nodes[n.ID]; ok {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.id", "duplicate")
		}
		nodes[n.ID] = i
	}
	for _, e := range d.Evidence {
		if e.WorkID != req.WorkID {
			return protocol.PreparedWorkUpdate{}, invalid("evidence.work_id", "foreign")
		}
		if !validID(e.ID) {
			return protocol.PreparedWorkUpdate{}, invalid("evidence.id", "invalid")
		}
		if _, ok := evidence[e.ID]; ok {
			return protocol.PreparedWorkUpdate{}, invalid("evidence.id", "duplicate")
		}
		evidence[e.ID] = e
	}
	for _, n := range d.Nodes {
		seen := map[string]bool{}
		for _, id := range n.EvidenceIDs {
			if !validID(id) {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "invalid")
			}
			if seen[id] {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "duplicate")
			}
			seen[id] = true
			if _, ok := evidence[id]; !ok {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "missing")
			}
		}
	}
	changed := map[string]bool{}
	createIndex := map[string]int{}
	transitionIndex := map[string]int{}
	retirements := map[string]string{}
	for _, c := range req.Nodes {
		if c.SupersededBy != "" && (c.ID == "" || c.ToStatus != protocol.StatusSuperseded || !validID(c.SupersededBy)) {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.superseded_by", "invalid")
		}
		if len(c.Ref) > MaxClientRefBytes {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.ref", "limit")
		}
		if len(c.Title) > MaxNodeTitleBytes {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.title", "limit")
		}
		if len(c.Content) > MaxNodeContentBytes {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.content", "limit")
		}
		if len(c.EvidenceIDs) > MaxNodeEvidenceIDs {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "limit")
		}
		var n protocol.WorkNode
		if c.ID == "" {
			if !validID(c.Ref) || strings.TrimSpace(c.Title) == "" || c.ExpectedRevision != 0 || c.FromStatus != "" {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.create", "invalid")
			}
			if _, ok := refs[c.Ref]; ok {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.ref", "duplicate")
			}
			if _, ok := nodes[c.Ref]; ok {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.ref", "duplicate")
			}
			status, err := initialStatus(c)
			if err != nil {
				return protocol.PreparedWorkUpdate{}, err
			}
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", req.WorkID, req.ExpectedRevision, c.Ref)))
			id := "wnd_" + hex.EncodeToString(sum[:16])
			if _, ok := nodes[id]; ok {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.id", "duplicate")
			}
			n = protocol.WorkNode{ID: id, WorkID: req.WorkID, Kind: c.Kind, Title: c.Title, Content: append(json.RawMessage(nil), c.Content...), Status: status, Confidence: 1, Revision: 1, ValidFrom: now, CreatedAt: now, UpdatedAt: now}
			if _, err := decodeContent(n); err != nil {
				return protocol.PreparedWorkUpdate{}, err
			}
			if !automatic && n.Kind == protocol.NodeDecision && gateKind(n) != "" && n.Status == protocol.StatusApproved {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.to_status", "approval_required")
			}
			if automatic && n.Kind == protocol.NodeDecision && (n.Status == protocol.StatusApproved || n.Status == protocol.StatusRejected) {
				n.DecisionActor = "agent"
			}
			nodes[id] = len(d.Nodes)
			d.Nodes = append(d.Nodes, n)
			refs[c.Ref] = id
			createIndex[id] = len(p.Creates)
			p.Creates = append(p.Creates, n)
		} else {
			if !validID(c.ID) {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.id", "invalid")
			}
			if changed[c.ID] {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.id", "duplicate")
			}
			i, ok := nodes[c.ID]
			if !ok {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.id", "missing")
			}
			n = d.Nodes[i]
			if c.SupersededBy != "" && n.Kind != protocol.NodeTask {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.superseded_by", "invalid")
			}
			if n.Kind == protocol.NodeTask && c.ToStatus == protocol.StatusSuperseded {
				if c.SupersededBy == "" {
					return protocol.PreparedWorkUpdate{}, invalid("nodes.superseded_by", "required")
				}
				retirements[n.ID] = c.SupersededBy
			}
			if c.Ref != "" || c.Kind != "" || c.Title != "" || len(c.Content) != 0 {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.transition", "invalid")
			}
			if c.ExpectedRevision <= 0 || c.ExpectedRevision != n.Revision || c.FromStatus == "" || c.FromStatus != n.Status {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.expected_revision", "stale")
			}
			riskIntent := n.Kind == protocol.NodeUnknown && n.Status == protocol.StatusOpen && c.ToStatus == protocol.StatusAcceptedRisk && active(n) && blocking(n)
			if err := validateTransition(n, c.ToStatus); err != nil && !riskIntent && !(automatic && n.Kind == protocol.NodeDecision && n.Status == protocol.StatusProposed && (c.ToStatus == protocol.StatusApproved || c.ToStatus == protocol.StatusRejected)) {
				return protocol.PreparedWorkUpdate{}, err
			}
			p.NodeChecks = append(p.NodeChecks, protocol.WorkNodeCheck{ID: n.ID, ExpectedRevision: n.Revision, ExpectedStatus: n.Status})
			// A requested ready state is derived after projecting the entire batch.
			to := c.ToStatus
			if riskIntent && !automatic {
				to = n.Status
				p.Gates = append(p.Gates, protocol.WorkflowGate{WorkID: req.WorkID, NodeID: n.ID, NodeRevision: n.Revision, Kind: GateAcceptedRisk, Reason: GateAcceptedRisk, Summary: n.Title})
			}
			if n.Kind == protocol.NodeTask && to == protocol.StatusReady {
				to = n.Status
			}
			if to != n.Status {
				transitionIndex[n.ID] = len(p.Transitions)
				p.Transitions = append(p.Transitions, protocol.WorkNodeTransition{ID: n.ID, ExpectedRevision: n.Revision, FromStatus: n.Status, ToStatus: to, DecisionActor: func() string {
					if automatic && (n.Kind == protocol.NodeDecision || riskIntent) {
						return "agent"
					}
					return ""
				}()})
				n.Status = to
				if automatic && (n.Kind == protocol.NodeDecision || riskIntent) {
					n.DecisionActor = "agent"
				}
				n.Revision++
				n.UpdatedAt = now
			}
			d.Nodes[i] = n
		}
		changed[n.ID] = true
		seen := map[string]bool{}
		for _, id := range c.EvidenceIDs {
			if !validID(id) {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "invalid")
			}
			if seen[id] {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "duplicate")
			}
			seen[id] = true
			if _, ok := evidence[id]; !ok {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "missing")
			}
			already := false
			for _, old := range n.EvidenceIDs {
				if old == id {
					already = true
				}
			}
			if already {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "duplicate")
			}
			n.EvidenceIDs = append(append([]string(nil), n.EvidenceIDs...), id)
			p.EvidenceLinks = append(p.EvidenceLinks, protocol.WorkNodeEvidenceLink{NodeID: n.ID, EvidenceID: id})
		}
		d.Nodes[nodes[n.ID]] = n
		if i, ok := createIndex[n.ID]; ok {
			p.Creates[i] = n
		}
	}
	// Support must respect the existing evidence lifecycle, including superseded runs.
	activeEvidence := map[string]protocol.Evidence{}
	for _, e := range ActiveEvidence(d) {
		activeEvidence[e.ID] = e
	}
	for _, n := range d.Nodes {
		if changed[n.ID] && n.Kind == protocol.NodeUnknown && n.Status == protocol.StatusResolved && !hasActiveEvidence(n, activeEvidence) {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.evidence_ids", "structure")
		}
	}
	resolve := func(id string) (string, error) {
		if local, ok := refs[id]; ok {
			return local, nil
		}
		if !validID(id) {
			return "", invalid("edges.endpoint", "invalid")
		}
		if _, ok := nodes[id]; !ok {
			return "", invalid("edges.endpoint", "missing")
		}
		return id, nil
	}
	seenEdges := map[protocol.WorkEdge]bool{}
	for id, ref := range retirements {
		replacement, err := resolve(ref)
		if err != nil || replacement == id {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.superseded_by", "invalid")
		}
		i := nodes[id]
		d.Nodes[i].SupersededBy = replacement
		d.Nodes[i].ValidUntil = &now
		p.Transitions[transitionIndex[id]].SupersededBy = replacement
	}
	for _, e := range d.Edges {
		if e.WorkID != req.WorkID {
			return protocol.PreparedWorkUpdate{}, invalid("edges.work_id", "foreign")
		}
		if _, ok := nodes[e.FromNodeID]; !ok {
			return protocol.PreparedWorkUpdate{}, invalid("edges.endpoint", "missing")
		}
		if _, ok := nodes[e.ToNodeID]; !ok {
			return protocol.PreparedWorkUpdate{}, invalid("edges.endpoint", "missing")
		}
		if err := validateEdge(d.Nodes[nodes[e.FromNodeID]], e.Relation, d.Nodes[nodes[e.ToNodeID]]); err != nil {
			return protocol.PreparedWorkUpdate{}, err
		}
		seenEdges[e] = true
	}
	for _, e := range req.Edges {
		from, err := resolve(e.From)
		if err != nil {
			return protocol.PreparedWorkUpdate{}, err
		}
		to, err := resolve(e.To)
		if err != nil {
			return protocol.PreparedWorkUpdate{}, err
		}
		edge := protocol.WorkEdge{WorkID: req.WorkID, FromNodeID: from, Relation: e.Relation, ToNodeID: to}
		if seenEdges[edge] {
			return protocol.PreparedWorkUpdate{}, invalid("edges", "duplicate")
		}
		if err := validateEdge(d.Nodes[nodes[from]], e.Relation, d.Nodes[nodes[to]]); err != nil {
			return protocol.PreparedWorkUpdate{}, err
		}
		seenEdges[edge] = true
		d.Edges = append(d.Edges, edge)
		p.Edges = append(p.Edges, edge)
	}
	if dependencyCycle(d) {
		return protocol.PreparedWorkUpdate{}, invalid("edges.depends_on", "cycle")
	}
	for _, n := range d.Nodes {
		if _, err := decodeContent(n); err != nil {
			return protocol.PreparedWorkUpdate{}, err
		}
		if !active(n) {
			continue
		}
		switch n.Kind {
		case protocol.NodeOption:
			if rung(n) < 1 || rung(n) > 6 {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.solution_rung", "structure")
			}
		case protocol.NodeDecision:
			if !solutionSupported(d, n, activeEvidence) {
				return protocol.PreparedWorkUpdate{}, invalid("nodes.decision", "structure")
			}
		case protocol.NodeMemoryCandidate:
			if err := validateCandidate(d, n, activeEvidence); err != nil {
				return protocol.PreparedWorkUpdate{}, err
			}
		}
	}
	// Check starts against projected prerequisites before preserving in-progress state.
	readiness := d
	readiness.Nodes = append([]protocol.WorkNode(nil), d.Nodes...)
	for _, tr := range p.Transitions {
		if tr.FromStatus == protocol.StatusReady && tr.ToStatus == protocol.StatusInProgress {
			i := nodes[tr.ID]
			readiness.Nodes[i].Status = protocol.StatusReady
		}
	}
	derived := DeriveTaskStatuses(readiness)
	for id := range retirements {
		old := d.Nodes[nodes[id]]
		replacement := d.Nodes[nodes[old.SupersededBy]]
		if !validTaskReplacement(d, old, replacement, derived, activeEvidence) {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.superseded_by", "structure")
		}
	}
	for _, tr := range p.Transitions {
		if tr.FromStatus == protocol.StatusReady && tr.ToStatus == protocol.StatusInProgress && derived[tr.ID] != protocol.StatusReady {
			return protocol.PreparedWorkUpdate{}, invalid("nodes.readiness", "structure")
		}
	}
	derived = DeriveTaskStatuses(d)
	for _, n := range d.Nodes {
		status, ok := derived[n.ID]
		if !ok || status == n.Status {
			continue
		}
		if i, ok := createIndex[n.ID]; ok {
			p.Creates[i].Status = status
		} else if i, ok := transitionIndex[n.ID]; ok {
			p.Transitions[i].ToStatus = status
		} else {
			if !changed[n.ID] {
				p.NodeChecks = append(p.NodeChecks, protocol.WorkNodeCheck{ID: n.ID, ExpectedRevision: n.Revision, ExpectedStatus: n.Status})
			}
			p.Transitions = append(p.Transitions, protocol.WorkNodeTransition{ID: n.ID, ExpectedRevision: n.Revision, FromStatus: n.Status, ToStatus: status})
		}
		i := nodes[n.ID]
		d.Nodes[i].Status = status
		if _, created := createIndex[n.ID]; !created {
			d.Nodes[i].Revision++
		}
	}
	if !automatic {
		p.Gates = append(p.Gates, PendingWorkflowGates(d)...)
	}
	if HasMaterialGate(d) || len(p.Gates) > 0 {
		p.WorkflowDepth = protocol.DepthDesigned
	}
	return p, nil
}

// Retirement retains history but transfers every outstanding obligation to an
// active replacement. It cannot discard criteria, dependencies, requirements,
// or an approved decision, nor use an unrelated/unsupported task as a substitute.
func validTaskReplacement(d protocol.WorkDetail, old, next protocol.WorkNode, derived map[string]string, evidence map[string]protocol.Evidence) bool {
	if next.Kind != protocol.NodeTask || !active(next) || required(old) && !required(next) {
		return false
	}
	status := derived[next.ID]
	if status != protocol.StatusReady && status != protocol.StatusInProgress && status != protocol.StatusCompleted {
		return false
	}
	nodes := graphNodes(d)
	has := func(from, relation, to string) bool {
		for _, e := range d.Edges {
			if e.FromNodeID == from && e.Relation == relation && e.ToNodeID == to {
				return true
			}
		}
		return false
	}
	criteria, solution := false, false
	for _, edge := range d.Edges {
		if edge.Relation == protocol.RelVerifies && edge.ToNodeID == old.ID && active(nodes[edge.FromNodeID]) {
			criteria = true
			if !has(edge.FromNodeID, protocol.RelVerifies, next.ID) {
				return false
			}
		}
		if edge.FromNodeID == old.ID {
			target := nodes[edge.ToNodeID]
			if edge.Relation == protocol.RelDependsOn || edge.Relation == protocol.RelImplements && (target.Kind == protocol.NodeRequirement || active(target) && target.Status == protocol.StatusApproved) {
				if !has(next.ID, edge.Relation, edge.ToNodeID) {
					return false
				}
			}
		}
		if edge.FromNodeID == next.ID && edge.Relation == protocol.RelImplements {
			decision := nodes[edge.ToNodeID]
			if decision.Kind == protocol.NodeDecision && active(decision) && decision.Status == protocol.StatusApproved && required(decision) && solutionSupported(d, decision, evidence) {
				solution = true
			}
		}
	}
	return criteria && solution
}

// Gate metadata is itself deterministic evidence of Designed scope. Include
// historical decisions so denial or supersession can never lower that scope.
func HasMaterialGate(d protocol.WorkDetail) bool {
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeDecision && validGate(gateKind(n)) || n.Kind == protocol.NodeUnknown && n.Status == protocol.StatusAcceptedRisk {
			return true
		}
	}
	return false
}

func validateEdge(from protocol.WorkNode, relation string, to protocol.WorkNode) error {
	good := true
	switch relation {
	case protocol.RelRequires, protocol.RelServes, protocol.RelContradicts:
	case protocol.RelDependsOn:
		good = from.Kind == protocol.NodeTask && to.Kind == protocol.NodeTask
	case protocol.RelSelects:
		good = from.Kind == protocol.NodeDecision && to.Kind == protocol.NodeOption
	case protocol.RelImplements:
		good = from.Kind == protocol.NodeTask && (to.Kind == protocol.NodeRequirement || to.Kind == protocol.NodeDecision)
	case protocol.RelVerifies:
		good = from.Kind == protocol.NodeCriterion && (to.Kind == protocol.NodeRequirement || to.Kind == protocol.NodeTask || to.Kind == protocol.NodeDecision || to.Kind == protocol.NodeMemoryCandidate || to.Kind == protocol.NodeFact)
	case protocol.RelSupports:
		good = len(from.EvidenceIDs) > 0 && (to.Kind == protocol.NodeDecision || to.Kind == protocol.NodeRequirement || to.Kind == protocol.NodeMemoryCandidate || to.Kind == protocol.NodeFact)
	case protocol.RelCandidateFor:
		good = from.Kind == protocol.NodeMemoryCandidate && (to.Kind == protocol.NodeFact || to.Kind == protocol.NodeDecision)
	default:
		return invalid("edges.relation", "invalid")
	}
	if !good {
		return invalid("edges.relation", "structure")
	}
	return nil
}

func graphNodes(d protocol.WorkDetail) map[string]protocol.WorkNode {
	out := map[string]protocol.WorkNode{}
	for _, n := range d.Nodes {
		out[n.ID] = n
	}
	return out
}
func hasActiveEvidence(n protocol.WorkNode, ev map[string]protocol.Evidence) bool {
	for _, id := range n.EvidenceIDs {
		if e, ok := ev[id]; ok && e.StaleAt == nil && e.Availability != protocol.AvailUnavailable {
			return true
		}
	}
	return false
}
func selectedOption(d protocol.WorkDetail, n protocol.WorkNode) (protocol.WorkNode, bool) {
	nodes := graphNodes(d)
	var selected protocol.WorkNode
	count := 0
	for _, e := range d.Edges {
		if e.FromNodeID == n.ID && e.Relation == protocol.RelSelects {
			selected = nodes[e.ToNodeID]
			count++
		}
	}
	return selected, count == 1 && selected.Kind == protocol.NodeOption && active(selected) && rung(selected) >= 1 && rung(selected) <= 6
}
func hasCriterion(d protocol.WorkDetail, id string) bool {
	nodes := graphNodes(d)
	for _, e := range d.Edges {
		if e.Relation == protocol.RelVerifies && e.ToNodeID == id {
			n := nodes[e.FromNodeID]
			if n.Kind == protocol.NodeCriterion && active(n) {
				return true
			}
		}
	}
	return false
}
func solutionSupported(d protocol.WorkDetail, n protocol.WorkNode, ev map[string]protocol.Evidence) bool {
	opt, ok := selectedOption(d, n)
	return ok && hasCriterion(d, n.ID) && (hasActiveEvidence(n, ev) || hasActiveEvidence(opt, ev))
}
func validateCandidate(d protocol.WorkDetail, n protocol.WorkNode, ev map[string]protocol.Evidence) error {
	if _, err := DecodeMemoryCandidate(n); err != nil {
		return err
	}
	nodes := graphNodes(d)
	source := false
	for _, e := range d.Edges {
		if e.FromNodeID == n.ID && e.Relation == protocol.RelCandidateFor {
			src := nodes[e.ToNodeID]
			if active(src) && (src.Kind == protocol.NodeFact || src.Kind == protocol.NodeDecision && src.Status == protocol.StatusApproved) {
				source = true
			}
		}
	}
	if !source || !hasActiveEvidence(n, ev) {
		return invalid("nodes.candidate", "structure")
	}
	return nil
}

// credentialField retains the vault's credential-name rules for JSON fields.
// semantic_key is the declared graph identifier, not credential material.
func credentialField(key string) bool {
	if key == "semantic_key" {
		return false
	}
	_, assignment := vault.Redact([]byte(key + "=credential-probe"))
	_, header := vault.Redact([]byte(key + ": credential-probe"))
	return assignment || header
}

func containsSecret(value any) bool {
	switch v := value.(type) {
	case json.RawMessage:
		var content any
		if json.Unmarshal(v, &content) != nil {
			return false
		}
		if containsSecret(content) {
			return true
		}
		// Maps retain credential names but overwrite duplicate values. Inspect
		// raw tokens too, so an earlier secret cannot be hidden by a later key.
		decoder := json.NewDecoder(bytes.NewReader(v))
		for {
			token, err := decoder.Token()
			if err != nil {
				return false
			}
			if text, ok := token.(string); ok && containsSecret(text) {
				return true
			}
		}
	case string:
		_, changed := vault.Redact([]byte(v))
		return changed
	case map[string]any:
		for key, item := range v {
			if credentialField(key) || containsSecret(key) || containsSecret(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsSecret(item) {
				return true
			}
		}
	}
	return false
}
func dependencyCycle(d protocol.WorkDetail) bool {
	adj := map[string][]string{}
	for _, e := range d.Edges {
		if e.Relation == protocol.RelDependsOn {
			adj[e.FromNodeID] = append(adj[e.FromNodeID], e.ToNodeID)
		}
	}
	state := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, dep := range adj[id] {
			if visit(dep) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeTask && visit(n.ID) {
			return true
		}
	}
	return false
}

// DeriveTaskStatuses never reopens started/terminal tasks. Lost readiness blocks.
func DeriveTaskStatuses(d protocol.WorkDetail) map[string]string {
	return workflowgraph.DeriveTaskStatuses(d)
}

// MutationReady enforces the Designed solution and runnable implementation
// obligations independently of whether an approval is still proposed.
func MutationReady(d protocol.WorkDetail) bool {
	solution, requiredTask, runnable := false, false, false
	for _, n := range d.Nodes {
		if !active(n) {
			continue
		}
		if n.Kind == protocol.NodeUnknown && blocking(n) && n.Status != protocol.StatusResolved && n.Status != protocol.StatusAcceptedRisk {
			return false
		}
		if n.Kind == protocol.NodeDecision && n.Status == protocol.StatusApproved {
			if _, ok := selectedOption(d, n); ok {
				solution = true
			}
		}
		if n.Kind == protocol.NodeTask && required(n) {
			requiredTask = true
			runnable = runnable || n.Status == protocol.StatusReady || n.Status == protocol.StatusInProgress
		}
	}
	return solution && (!requiredTask || runnable)
}

// PendingWorkflowGates returns only unresolved, active required decisions.
func PendingWorkflowGates(d protocol.WorkDetail) []protocol.WorkflowGate {
	var out []protocol.WorkflowGate
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeDecision || !active(n) || n.Status != protocol.StatusProposed || !required(n) || !validGate(gateKind(n)) {
			continue
		}
		out = append(out, protocol.WorkflowGate{WorkID: d.Work.ID, NodeID: n.ID, NodeRevision: n.Revision, Kind: gateKind(n), Reason: gateKind(n), Summary: n.Title})
	}
	return out
}

// CompletionBlockers extends freshness checks with required graph obligations.
func CompletionBlockers(d protocol.WorkDetail) []string {
	if d.Work.WorkflowDepth == protocol.DepthDirect {
		return Unresolved(d)
	}
	var out []string
	filtered := d
	filtered.Nodes = nil
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeCriterion || active(n) && required(n) {
			filtered.Nodes = append(filtered.Nodes, n)
		}
	}
	out = append(out, Unresolved(filtered)...)
	evidence := map[string]protocol.Evidence{}
	for _, e := range ActiveEvidence(d) {
		evidence[e.ID] = e
	}
	solution, tasks := false, false
	for _, n := range d.Nodes {
		if !active(n) {
			continue
		}
		switch n.Kind {
		case protocol.NodeDecision:
			supported := n.Status == protocol.StatusApproved && solutionSupported(d, n, evidence)
			if supported {
				solution = true
			}
			if required(n) && !supported {
				out = append(out, n.Title)
			}
		case protocol.NodeTask:
			tasks = true
			if required(n) && n.Status != protocol.StatusCompleted {
				out = append(out, n.Title)
			}
		case protocol.NodeUnknown:
			if blocking(n) && n.Status != protocol.StatusResolved && n.Status != protocol.StatusAcceptedRisk {
				out = append(out, n.Title)
			}
		}
	}
	if !solution {
		out = append(out, "solution decision")
	}
	if !tasks {
		out = append(out, "task graph")
	}
	return out
}
