package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func qualifyFixture() QualifyInput {
	c := protocol.WorkNode{ID: "candidate", WorkID: "work", Kind: protocol.NodeMemoryCandidate, Status: protocol.StatusPending, Revision: 1, Content: json.RawMessage(`{"category":"command","semantic_key":"test-command","text":"  - Use go test ./... for this repository  ","scope_paths":["internal/./work","app"],"source_revision":"final"}`), EvidenceIDs: []string{"ev-z", "ev-a"}}
	return QualifyInput{ProjectRoot: "/nonexistent/project", FinalRevision: "final", Candidate: c, Detail: protocol.WorkDetail{
		Work:         protocol.Work{ID: "work", ProjectID: "project", Status: protocol.WorkCompleted, WorkflowDepth: protocol.DepthDesigned},
		Nodes:        []protocol.WorkNode{c, {ID: "fact", WorkID: "work", Kind: protocol.NodeFact, Status: "active"}, {ID: "criterion", WorkID: "work", Kind: protocol.NodeCriterion, Status: protocol.AttemptPassed}},
		Edges:        []protocol.WorkEdge{{WorkID: "work", FromNodeID: "candidate", Relation: protocol.RelCandidateFor, ToNodeID: "fact"}, {WorkID: "work", FromNodeID: "criterion", Relation: protocol.RelVerifies, ToNodeID: "candidate"}},
		Evidence:     []protocol.Evidence{{ID: "ev-a", WorkID: "work", SourceRevision: "final", Summary: "PRIVATE EVIDENCE BODY"}, {ID: "ev-z", WorkID: "work", SourceRevision: "final"}, {ID: "ev-check", WorkID: "work", SourceRevision: "final", NodeID: "criterion"}},
		Attempts:     []protocol.VerificationAttempt{{ID: "attempt", WorkID: "work", CriterionNodeID: "criterion", Status: protocol.AttemptPassed, EvidenceID: "ev-check", FingerprintID: "fingerprint", StartedAt: time.Unix(2, 0), FinishedAt: time.Unix(3, 0)}},
		Fingerprints: []protocol.Fingerprint{{ID: "fingerprint", WorkID: "work", Value: "final", Kind: protocol.FingerprintVerification}},
	}}
}

func changeContent(in *QualifyInput, field string, value any) {
	var content map[string]any
	_ = json.Unmarshal(in.Candidate.Content, &content)
	if value == nil {
		delete(content, field)
	} else {
		content[field] = value
	}
	in.Candidate.Content, _ = json.Marshal(content)
	in.Detail.Nodes[0] = in.Candidate
}

func memoryRow(text string) protocol.ProjectMemory {
	sum := sha256.Sum256([]byte(text))
	return protocol.ProjectMemory{ID: "memory", ProjectID: "project", SemanticKey: "test-command", Category: "command", Text: text, TextHash: hex.EncodeToString(sum[:]), TargetPath: "UMCODE.md", Status: protocol.MemoryStatusActive}
}

// Catches missing freshness, provenance, eligibility, and ownership checks.
func TestQualifyMatrix(t *testing.T) {
	cases := []struct {
		name, status, reason string
		change               func(*QualifyInput)
	}{
		{"qualified", "", "", func(*QualifyInput) {}},
		{"work open", "stale", "work_not_completed", func(in *QualifyInput) { in.Detail.Work.Status = protocol.WorkOpen }},
		{"projectless", "rejected", "project_required", func(in *QualifyInput) { in.Detail.Work.ProjectID = "" }},
		{"candidate not pending", "stale", "candidate_inactive", func(in *QualifyInput) { in.Candidate.Status = protocol.StatusRejected }},
		{"candidate expired", "stale", "candidate_inactive", func(in *QualifyInput) { at := time.Unix(1, 0); in.Candidate.ValidUntil = &at }},
		{"foreign candidate", "rejected", "candidate_invalid", func(in *QualifyInput) { in.Candidate.WorkID = "other" }},
		{"missing canonical candidate", "stale", "candidate_inactive", func(in *QualifyInput) { in.Detail.Nodes = in.Detail.Nodes[1:] }},
		{"canonical candidate changed", "stale", "candidate_inactive", func(in *QualifyInput) { in.Detail.Nodes[0].Revision++ }},
		{"missing source", "stale", "source_stale", func(in *QualifyInput) { in.Detail.Edges = in.Detail.Edges[1:] }},
		{"source expired", "stale", "source_stale", func(in *QualifyInput) { at := time.Unix(1, 0); in.Detail.Nodes[1].ValidUntil = &at }},
		{"source stale", "stale", "source_stale", func(in *QualifyInput) { in.Detail.Nodes[1].Status = protocol.StatusStale }},
		{"foreign source", "stale", "source_stale", func(in *QualifyInput) { in.Detail.Nodes[1].WorkID = "other" }},
		{"foreign source edge", "stale", "source_stale", func(in *QualifyInput) { in.Detail.Edges[0].WorkID = "other" }},
		{"ambiguous source", "conflicted", "source_ambiguous", func(in *QualifyInput) {
			in.Detail.Nodes = append(in.Detail.Nodes, protocol.WorkNode{ID: "fact2", WorkID: "work", Kind: protocol.NodeFact, Status: "active"})
			in.Detail.Edges = append(in.Detail.Edges, protocol.WorkEdge{WorkID: "work", FromNodeID: "candidate", Relation: protocol.RelCandidateFor, ToNodeID: "fact2"})
		}},
		{"unsupported source", "rejected", "source_unsupported", func(in *QualifyInput) { in.Detail.Nodes[1].Kind = protocol.NodeTask }},
		{"proposed decision", "rejected", "source_unsupported", func(in *QualifyInput) {
			in.Detail.Nodes[1].Kind = protocol.NodeDecision
			in.Detail.Nodes[1].Status = protocol.StatusProposed
		}},
		{"rejected decision", "stale", "source_stale", func(in *QualifyInput) {
			in.Detail.Nodes[1].Kind = protocol.NodeDecision
			in.Detail.Nodes[1].Status = protocol.StatusRejected
		}},
		{"superseded decision", "stale", "source_stale", func(in *QualifyInput) {
			in.Detail.Nodes[1].Kind = protocol.NodeDecision
			in.Detail.Nodes[1].Status = protocol.StatusSuperseded
		}},
		{"no evidence", "stale", "evidence_stale", func(in *QualifyInput) { in.Candidate.EvidenceIDs = nil; in.Detail.Nodes[0] = in.Candidate }},
		{"one missing evidence", "stale", "evidence_stale", func(in *QualifyInput) { in.Detail.Evidence = in.Detail.Evidence[1:] }},
		{"foreign evidence", "stale", "evidence_stale", func(in *QualifyInput) { in.Detail.Evidence[0].WorkID = "other" }},
		{"expired evidence", "stale", "evidence_stale", func(in *QualifyInput) { at := time.Unix(1, 0); in.Detail.Evidence[0].StaleAt = &at }},
		{"unavailable vault", "stale", "evidence_stale", func(in *QualifyInput) {
			in.Detail.Evidence[0].VaultHash = "hash"
			in.Detail.Evidence[0].Availability = protocol.AvailUnavailable
		}},
		{"unconfirmed vault", "stale", "evidence_stale", func(in *QualifyInput) { in.Detail.Evidence[0].VaultHash = "hash" }},
		{"old evidence revision", "stale", "evidence_stale", func(in *QualifyInput) { in.Detail.Evidence[0].SourceRevision = "old" }},
		{"failed check", "stale", "verification_stale", func(in *QualifyInput) { in.Detail.Attempts[0].Status = protocol.AttemptFailed }},
		{"missing check", "stale", "verification_stale", func(in *QualifyInput) { in.Detail.Attempts = nil }},
		{"stale criterion", "stale", "verification_stale", func(in *QualifyInput) { in.Detail.Nodes[2].Status = protocol.StatusStale }},
		{"foreign check", "stale", "verification_stale", func(in *QualifyInput) { in.Detail.Attempts[0].WorkID = "other" }},
		{"missing check evidence", "stale", "verification_stale", func(in *QualifyInput) { in.Detail.Attempts[0].EvidenceID = "missing" }},
		{"old check fingerprint", "stale", "verification_stale", func(in *QualifyInput) { in.Detail.Fingerprints[0].Value = "old" }},
		{"changed after check", "stale", "verification_stale", func(in *QualifyInput) {
			in.Detail.Evidence = append(in.Detail.Evidence, protocol.Evidence{ID: "edit", WorkID: "work", Kind: protocol.EvidenceFileChange, ObservedAt: time.Unix(4, 0)})
		}},
		{"new failed check", "stale", "verification_stale", func(in *QualifyInput) {
			a := in.Detail.Attempts[0]
			a.ID = "new"
			a.Status = protocol.AttemptFailed
			a.StartedAt = time.Unix(5, 0)
			in.Detail.Attempts = append(in.Detail.Attempts, a)
		}},
		{"obsolete candidate evidence", "stale", "evidence_stale", func(in *QualifyInput) {
			in.Detail.Attempts[0].EvidenceID = "ev-a"
			in.Detail.Attempts = append(in.Detail.Attempts, protocol.VerificationAttempt{ID: "new", WorkID: "work", CriterionNodeID: "criterion", Status: protocol.AttemptPassed, EvidenceID: "ev-check", FingerprintID: "fingerprint", StartedAt: time.Unix(4, 0), FinishedAt: time.Unix(5, 0)})
		}},
		{"optional unrelated failed criterion", "", "", func(in *QualifyInput) {
			in.Detail.Nodes = append(in.Detail.Nodes, protocol.WorkNode{ID: "optional", WorkID: "work", Kind: protocol.NodeCriterion, Status: protocol.AttemptFailed, Content: json.RawMessage(`{"required":false}`)})
		}},
		{"optional applicable failed criterion", "stale", "verification_stale", func(in *QualifyInput) {
			in.Detail.Nodes[2].Content = json.RawMessage(`{"required":false}`)
			in.Detail.Nodes[2].Status = protocol.AttemptFailed
		}},
		{"required unrelated failed criterion", "stale", "verification_stale", func(in *QualifyInput) {
			in.Detail.Nodes = append(in.Detail.Nodes, protocol.WorkNode{ID: "required", WorkID: "work", Kind: protocol.NodeCriterion, Status: protocol.AttemptFailed})
		}},
		{"inactive extra source", "", "", func(in *QualifyInput) {
			in.Detail.Nodes = append(in.Detail.Nodes, protocol.WorkNode{ID: "old", WorkID: "work", Kind: protocol.NodeFact, Status: protocol.StatusSuperseded})
			in.Detail.Edges = append(in.Detail.Edges, protocol.WorkEdge{WorkID: "work", FromNodeID: "candidate", Relation: protocol.RelCandidateFor, ToNodeID: "old"})
		}},
		{"available vault", "", "", func(in *QualifyInput) {
			in.Detail.Evidence[0].VaultHash = "hash"
			in.Detail.Evidence[0].Availability = protocol.AvailAvailable
		}},
		{"root scope", "", "", func(in *QualifyInput) { changeContent(in, "scope_paths", nil) }},
		{"generic unscoped prose", "rejected", "content_generic", func(in *QualifyInput) {
			changeContent(in, "scope_paths", nil)
			changeContent(in, "text", "Use good names")
		}},
		{"revision mismatch", "stale", "revision_mismatch", func(in *QualifyInput) { in.FinalRevision = "new" }},
		{"missing revision", "stale", "revision_mismatch", func(in *QualifyInput) { changeContent(in, "source_revision", nil) }},
		{"unsafe text", "rejected", "content_invalid", func(in *QualifyInput) { changeContent(in, "text", "# heading") }},
		{"secret text", "rejected", "content_invalid", func(in *QualifyInput) { changeContent(in, "text", "API_TOKEN=ordinarysecret12345") }},
		{"generic advice", "rejected", "content_generic", func(in *QualifyInput) { changeContent(in, "text", "Always write tests and follow best practices") }},
		{"preference", "rejected", "content_unsupported", func(in *QualifyInput) { changeContent(in, "text", "The user prefers tabs in this repository") }},
		{"guess", "rejected", "content_unsupported", func(in *QualifyInput) { changeContent(in, "text", "This repository probably uses Go") }},
		{"temporary", "rejected", "content_temporary", func(in *QualifyInput) { changeContent(in, "text", "Tests currently fail in this repository") }},
		{"branch local", "rejected", "content_branch_local", func(in *QualifyInput) { changeContent(in, "text", "On branch feature/foo this repository uses Go") }},
		{"raw output", "rejected", "content_unsupported", func(in *QualifyInput) { changeContent(in, "text", "ok github.com/project/pkg 0.3s") }},
		{"contradiction", "rejected", "source_unsupported", func(in *QualifyInput) {
			in.Detail.Edges = append(in.Detail.Edges, protocol.WorkEdge{WorkID: "work", FromNodeID: "criterion", Relation: protocol.RelContradicts, ToNodeID: "candidate"})
		}},
		{"outside absolute path", "rejected", "content_invalid", func(in *QualifyInput) { changeContent(in, "text", "This repository writes to /etc/hosts") }},
		{"outside markdown path", "rejected", "content_invalid", func(in *QualifyInput) { changeContent(in, "text", "This repository writes [hosts](/etc/hosts)") }},
		{"outside assignment path", "rejected", "content_invalid", func(in *QualifyInput) { changeContent(in, "text", "This repository uses --output=/etc/hosts") }},
		{"fact approved decision category", "rejected", "source_unsupported", func(in *QualifyInput) { changeContent(in, "category", "approved_decision") }},
		{"same current", "promoted", "already_current", func(in *QualifyInput) {
			in.ActiveMemories = []protocol.ProjectMemory{memoryRow("- Use go test ./... for this repository")}
		}},
		{"key conflict", "conflicted", "semantic_conflict", func(in *QualifyInput) {
			in.ActiveMemories = []protocol.ProjectMemory{memoryRow("- Run make test for this repository")}
		}},
		{"explicit replacement", "", "", func(in *QualifyInput) {
			in.ActiveMemories = []protocol.ProjectMemory{memoryRow("- Run make test for this repository")}
			changeContent(in, "replaces_memory", "memory")
		}},
		{"edited ownership", "conflicted", "replacement_conflict", func(in *QualifyInput) {
			row := memoryRow("- Run make test for this repository")
			row.Text = "- Edited guidance"
			in.ActiveMemories = []protocol.ProjectMemory{row}
			changeContent(in, "replaces_memory", "memory")
		}},
		{"missing replacement", "conflicted", "replacement_conflict", func(in *QualifyInput) { changeContent(in, "replaces_memory", "absent") }},
		{"wrong project replacement", "conflicted", "replacement_conflict", func(in *QualifyInput) {
			row := memoryRow("- Run make test for this repository")
			row.ProjectID = "other"
			in.ActiveMemories = []protocol.ProjectMemory{row}
			changeContent(in, "replaces_memory", "memory")
		}},
		{"duplicate identity", "conflicted", "semantic_conflict", func(in *QualifyInput) {
			row := memoryRow("- Run make test for this repository")
			in.ActiveMemories = []protocol.ProjectMemory{row, row}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := qualifyFixture()
			tc.change(&in)
			before, _ := json.Marshal(in)
			proposal, outcome := Qualify(in)
			if outcome.Status != tc.status || outcome.Reason != tc.reason {
				t.Fatalf("outcome=%+v, want %s/%s", outcome, tc.status, tc.reason)
			}
			if tc.status != "" && !reflect.DeepEqual(proposal, Proposal{}) {
				t.Fatalf("ineligible candidate produced proposal: %+v", proposal)
			}
			if tc.status == "" && proposal.Text != "- Use go test ./... for this repository" {
				t.Fatalf("proposal=%+v", proposal)
			}
			after, _ := json.Marshal(in)
			if string(before) != string(after) {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestQualifyCanonicalProposal(t *testing.T) {
	for _, category := range []string{"capability", "command", "boundary", "invariant", "convention", "path", "approved_decision"} {
		t.Run(category, func(t *testing.T) {
			in := qualifyFixture()
			changeContent(&in, "category", category)
			if category == "approved_decision" {
				in.Detail.Nodes[1].Kind = protocol.NodeDecision
				in.Detail.Nodes[1].Status = protocol.StatusApproved
			}
			p, out := Qualify(in)
			if out != (Outcome{}) {
				t.Fatal(out)
			}
			want := Proposal{WorkID: "work", CandidateNodeID: "candidate", SemanticKey: "test-command", Category: category, Text: "- Use go test ./... for this repository", SourceRevision: "final", ScopePaths: []string{"app", "internal/work"}, EvidenceIDs: []string{"ev-a", "ev-z"}}
			if !reflect.DeepEqual(p, want) {
				t.Fatalf("proposal=%+v", p)
			}
			blob, _ := json.Marshal(p)
			if strings.Contains(string(blob), "PRIVATE EVIDENCE") {
				t.Fatal("evidence leaked")
			}
			p.EvidenceIDs[0] = "changed"
			p.ScopePaths[0] = "changed"
			if in.Candidate.EvidenceIDs[0] != "ev-z" {
				t.Fatal("proposal aliases input")
			}
		})
	}
	t.Run("legacy scope", func(t *testing.T) {
		in := qualifyFixture()
		changeContent(&in, "scope_paths", nil)
		changeContent(&in, "scope", "internal/./work")
		p, out := Qualify(in)
		if out != (Outcome{}) || !reflect.DeepEqual(p.ScopePaths, []string{"internal/work"}) {
			t.Fatalf("%+v %+v", p, out)
		}
	})
	t.Run("inside absolute path", func(t *testing.T) {
		in := qualifyFixture()
		changeContent(&in, "text", "This repository stores packages in /nonexistent/project/internal")
		p, out := Qualify(in)
		if out != (Outcome{}) || p.Text != "- This repository stores packages in /nonexistent/project/internal" {
			t.Fatalf("%+v %+v", p, out)
		}
	})
}
