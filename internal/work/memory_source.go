package work

import (
	"encoding/json"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

// VerifiedCommandFact is engine-owned provenance from a successful verification
// observation. It never accepts free-form guidance from tool output or clients.
type VerifiedCommandFact struct {
	Type           string `json:"type"`
	Command        string `json:"command"`
	EvidenceID     string `json:"evidence_id"`
	SourceRevision string `json:"source_revision"`
}

// MemorySourceGuidance projects bounded canonical source content. Decision titles
// are immutable within a node revision and covered by the approval transition.
func MemorySourceGuidance(n protocol.WorkNode) (category, text string, ok bool) {
	switch n.Kind {
	case protocol.NodeFact:
		var f VerifiedCommandFact
		if json.Unmarshal(n.Content, &f) != nil || f.Type != "verified_command" || f.Command == "" || strings.TrimSpace(f.Command) != f.Command || f.EvidenceID == "" || f.SourceRevision == "" {
			return "", "", false
		}
		category, text = protocol.MemoryCategoryCommand, "Use "+f.Command+" for this repository"
	case protocol.NodeDecision:
		if n.Status != protocol.StatusApproved || n.DecisionActor != "user" {
			return "", "", false
		}
		category, text = protocol.MemoryCategoryApprovedDecision, n.Title
	default:
		return "", "", false
	}
	content, _ := json.Marshal(MemoryCandidateContent{Category: category, SemanticKey: "source", Text: text, SourceRevision: "source"})
	c, err := DecodeMemoryCandidate(protocol.WorkNode{Content: content})
	if err != nil {
		return "", "", false
	}
	return c.Category, c.Text, true
}
