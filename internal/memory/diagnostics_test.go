package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPromotionDiagnosticsProvenanceAndEstimates(t *testing.T) {
	s, st, req := serviceFixture(t)
	r := s.PromoteCompleted(t.Context(), req)
	if len(r.Diagnostics) != 1 {
		t.Fatalf("diagnostics=%+v", r)
	}
	d := r.Diagnostics[0]
	if d.ProjectID != req.Project.ID || d.WorkID != req.WorkID || d.ThreadID != req.ThreadID || d.TurnID != req.TurnID || d.CandidateNodeID == "" || len(d.SourceNodeIDs) != 1 || len(d.EvidenceIDs) != 2 || d.SourceRevision == "" || d.OperationID == "" {
		t.Fatalf("provenance=%+v", d)
	}
	if d.TargetPath != "UMCODE.md" || d.Status != protocol.MemoryOutcomePromoted || d.Reason != ReasonInserted || len(d.BeforeHash) != 64 || len(d.AfterHash) != 64 || d.BytesBefore != 0 || d.BytesAfter != len(readTarget(t, req.Project)) || d.Inserted != 1 || d.EstimatedInstructionTokensAdded != (d.BytesAfter+3)/4 || d.EstimatedContextTokensAvoided != 0 {
		t.Fatalf("file/accounting=%+v", d)
	}
	rows, err := st.ListProjectMemories(t.Context(), req.Project.ID)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	duplicate := addCompletedWork(t, st, req.Project, "test-command", "Use go test ./... for this repository")
	r = s.PromoteCompleted(t.Context(), duplicate)
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].Reason != ReasonAlreadyCurrent || r.Diagnostics[0].Unchanged != 1 || r.EstimatedContextTokensAvoided != (len(rows[0].Text)+3)/4 || r.EstimatedInstructionTokensAdded != 0 {
		t.Fatalf("duplicate=%+v", r)
	}
	newer := addCompletedWork(t, st, req.Project, "test-command", "Use go test ./internal/work for this repository")
	setCandidateField(t, st, newer, "replaces_memory", rows[0].ID)
	r = s.PromoteCompleted(t.Context(), newer)
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].Reason != ReasonReplaced || r.Diagnostics[0].Replaced != 1 || r.EstimatedContextTokensAvoided != (len(rows[0].Text)+3)/4 {
		t.Fatalf("replacement=%+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Use go test") || strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "test-command") {
		t.Fatalf("semantic report=%s", b)
	}
}

func TestPromotionDiagnosticsOutcomeReasons(t *testing.T) {
	for _, mode := range []string{"qualify", "size", "file", "storage", "merge"} {
		t.Run(mode, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			reason := ReasonContentTemporary
			switch mode {
			case "qualify":
				setCandidateField(t, st, req, "text", "Tests currently fail for this task")
			case "size":
				s.targetBytes = 1
				reason = ReasonSizeLimit
			case "file":
				os.WriteFile(filepath.Join(req.Project.Root, "UMCODE.md"), []byte("user\n"), 0444)
				reason = ReasonFileIO
			case "storage":
				execService(t, st, `CREATE TRIGGER fail_prepare BEFORE INSERT ON memory_promotion_ops BEGIN SELECT RAISE(ABORT,'PRIVATE STORAGE ERROR'); END`)
				reason = ReasonStorage
			case "merge":
				os.WriteFile(filepath.Join(req.Project.Root, "UMCODE.md"), []byte("## Verified project memory\n<!-- umcode:generated -->\n## Verified project memory\n<!-- umcode:generated -->\n"), 0644)
				reason = ReasonManagedSectionConflict
			}
			r := s.PromoteCompleted(t.Context(), req)
			if len(r.Diagnostics) != 1 || r.Diagnostics[0].Reason != reason || r.Diagnostics[0].Status == "" {
				t.Fatalf("reason=%s report=%+v", reason, r)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "Tests currently") {
				t.Fatalf("unsafe report=%s", b)
			}
		})
	}
}

func TestRecoveryDiagnosticsBreakdown(t *testing.T) {
	for _, mode := range []string{"before", "after", "third", "pending"} {
		t.Run(mode, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			stop := "write"
			if mode == "after" {
				stop = "record"
			}
			s.boundary = func(stage string) error {
				if stage == stop {
					return errors.New("PRIVATE FAILURE")
				}
				return nil
			}
			if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
				t.Fatalf("prepare=%+v", r)
			}
			s.boundary = nil
			if mode == "third" {
				os.WriteFile(filepath.Join(req.Project.Root, "UMCODE.md"), []byte("external\n"), 0644)
			}
			if mode == "pending" {
				s.boundary = func(stage string) error {
					if stage == "write" {
						return errors.New("PRIVATE RETRY ERROR")
					}
					return nil
				}
			}
			r := s.Recover(t.Context())
			if len(r.Diagnostics) != 1 {
				t.Fatalf("diagnostics=%+v", r)
			}
			d := r.Diagnostics[0]
			if d.WorkID != req.WorkID || d.CandidateNodeID == "" || d.OperationID == "" || d.TargetPath != "UMCODE.md" || d.SourceRevision == "" || len(d.EvidenceIDs) != 2 || len(d.BeforeHash) != 64 || len(d.AfterHash) != 64 {
				t.Fatalf("recovery provenance=%+v", d)
			}
			switch mode {
			case "before":
				if r.RecoveryCompleted != 1 || r.RecoveryRetried != 1 || d.Recovery != "retried" {
					t.Fatalf("retry=%+v", r)
				}
			case "after":
				if r.RecoveryCompleted != 1 || r.RecoveryRetried != 0 || d.Recovery != "completed" {
					t.Fatalf("finish=%+v", r)
				}
			case "third":
				if r.RecoveryConflicted != 1 || d.Recovery != "conflicted" || d.Reason != ReasonCompareAndSwap {
					t.Fatalf("conflict=%+v", r)
				}
			case "pending":
				if r.RecoveryPendingRepair != 1 || r.RecoveryRetried != 1 || d.Recovery != "pending_repair" || d.Reason != ReasonFileIO {
					t.Fatalf("pending=%+v", r)
				}
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "Use go test") {
				t.Fatalf("unsafe recovery=%s", b)
			}
			ops, err := st.ListIncompleteMemoryPromotionOps(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if mode != "pending" && len(ops) != 0 {
				t.Fatalf("incomplete=%+v", ops)
			}
		})
	}
}

func TestPromotionDiagnosticsBoundedAndContentFree(t *testing.T) {
	var r Report
	for i := 0; i < MaxDiagnostics+2; i++ {
		r.diagnostic(Diagnostic{WorkID: "api_key=PRIVATE_SECRET", SourceRevision: "PRIVATE REVISION BODY", EvidenceIDs: []string{"ev_safe", "api_key=PRIVATE_EVIDENCE_SECRET"}, Status: protocol.MemoryOutcomePromoted, Reason: ReasonAlreadyCurrent, EstimatedContextTokensAvoided: 1})
	}
	if len(r.Diagnostics) != MaxDiagnostics || r.DiagnosticsDropped != 2 || r.EstimatedContextTokensAvoided != MaxDiagnostics+2 {
		t.Fatalf("unbounded/lost totals: %+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "api_key") {
		t.Fatalf("semantic identities exposed=%s", b)
	}
}
