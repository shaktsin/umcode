package memory

import (
	"context"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// Recover reconciles exact before/after states in storage creation/ID order.
// Third-state files are never rewritten, including user edits after rename.
func (s *Service) Recover(ctx context.Context) Report {
	var report Report
	ops, err := s.st.ListIncompleteMemoryPromotionOps(ctx)
	if err != nil {
		report.Pending++
		report.RecoveryPendingRepair++
		report.diagnostic(Diagnostic{Status: protocol.MemoryOutcomePending, Reason: ReasonStorage, Recovery: "pending_repair"})
		return report
	}
	for _, listed := range ops {
		func() {
			lock := projectLock(listed.ProjectID)
			lock.Lock()
			defer lock.Unlock()
			op, err := s.st.GetMemoryPromotionOp(ctx, listed.ID)
			if err != nil {
				report.Pending++
				report.RecoveryPendingRepair++
				d := s.operationDiagnostic(ctx, listed)
				d.Status, d.Reason, d.Recovery = protocol.MemoryOutcomePending, ReasonStorage, "pending_repair"
				report.diagnostic(d)
				return
			}
			if op.State == "committed" || op.State == "conflicted" {
				return
			}
			diag := s.operationDiagnostic(ctx, op)
			defer func() { report.diagnostic(diag) }()
			fail := func(err error, fallback string) {
				out := s.fail(ctx, op, err, fallback, &report)
				diag.Status, diag.Reason = out.Status, out.Reason
				diag.EstimatedInstructionTokensAdded, diag.EstimatedContextTokensAvoided = 0, 0
				diag.Inserted, diag.Replaced, diag.Unchanged = 0, 0, 0
				if out.Status == protocol.MemoryOutcomeConflicted {
					report.RecoveryConflicted++
					diag.Recovery = "conflicted"
				} else {
					report.RecoveryPendingRepair++
					diag.Recovery = "pending_repair"
				}
			}
			p, err := s.st.GetProject(ctx, op.ProjectID)
			if err != nil {
				fail(err, ReasonStorage)
				return
			}
			target, err := openTarget(p.Root, op.TargetPath)
			if err != nil {
				fail(err, ReasonFileIO)
				return
			}
			defer target.close()
			hash := memoryHash(target.before)
			beforeMatches := hash == op.FileHashBefore && (target.before == nil) == (op.BeforeBytes == nil)
			afterMatches := hash == op.FileHashAfter && (target.before == nil) == (op.AfterBytes == nil)
			if !beforeMatches && !afterMatches {
				fail(store.ErrMemoryConflict, ReasonCompareAndSwap)
				return
			}
			if err = s.step("recheck"); err == nil {
				err = target.check(hash)
			}
			retried := false
			if err == nil && beforeMatches && !afterMatches {
				retried = true
				report.RecoveryRetried++
				err = target.replace(op.AfterBytes, s.step)
			}
			if err == nil {
				emit := s.Emit
				if s.EmitRecovery != nil {
					emit = s.EmitRecovery
				}
				err = s.finish(ctx, p, op, emit)
			}
			if err != nil {
				fail(err, ReasonFileIO)
				return
			}
			report.Promoted++
			report.RecoveryCompleted++
			diag.Status = protocol.MemoryOutcomePromoted
			diag.Reason, diag.Recovery = ReasonRecoveryCompleted, "completed"
			if retried {
				diag.Reason, diag.Recovery = ReasonRecoveryRetried, "retried"
			}
			report.BytesBefore += len(op.BeforeBytes)
			report.BytesAfter += len(op.AfterBytes)
			report.Inserted += diag.Inserted
			report.Replaced += diag.Replaced
			report.Unchanged += diag.Unchanged
		}()
	}
	return report
}
