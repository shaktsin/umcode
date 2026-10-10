package memory

import (
	"context"

	"github.com/shaktsin/umcode/internal/store"
)

// Recover reconciles exact before/after states in storage creation/ID order.
// Third-state files are never rewritten, including user edits after rename.
func (s *Service) Recover(ctx context.Context) Report {
	var report Report
	ops, err := s.st.ListIncompleteMemoryPromotionOps(ctx)
	if err != nil {
		report.Pending++
		return report
	}
	for _, listed := range ops {
		lock := projectLock(listed.ProjectID)
		lock.Lock()
		func() {
			// Another reconciler may have finished this operation while we waited.
			op, err := s.st.GetMemoryPromotionOp(ctx, listed.ID)
			if err != nil {
				report.Pending++
				return
			}
			if op.State == "committed" || op.State == "conflicted" {
				return
			}
			p, err := s.st.GetProject(ctx, op.ProjectID)
			if err != nil {
				s.fail(ctx, op, err, &report)
				return
			}
			target, err := openTarget(p.Root, op.TargetPath)
			if err != nil {
				s.fail(ctx, op, err, &report)
				return
			}
			defer target.close()
			hash := memoryHash(target.before)
			if hash != op.FileHashBefore && hash != op.FileHashAfter {
				s.fail(ctx, op, store.ErrMemoryConflict, &report)
				return
			}
			if err = s.step("recheck"); err == nil {
				err = target.check(hash)
			}
			if err == nil && hash == op.FileHashBefore && hash != op.FileHashAfter {
				err = target.replace(op.AfterBytes, s.step)
			}
			if err == nil {
				err = s.finish(ctx, p, op)
			}
			if err != nil {
				s.fail(ctx, op, err, &report)
				return
			}
			report.Promoted++
			report.BytesBefore += len(op.BeforeBytes)
			report.BytesAfter += len(op.AfterBytes)
			if op.FileHashBefore == op.FileHashAfter {
				report.Unchanged++
			}
		}()
		lock.Unlock()
	}
	return report
}
