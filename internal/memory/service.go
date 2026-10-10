package memory

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/work"
)

type Request struct {
	Project                  protocol.Project
	WorkID, ThreadID, TurnID string
}

type Report struct {
	Promoted, Rejected, Stale, Conflicted, Pending, Inserted, Replaced, Unchanged int
	BytesBefore, BytesAfter                                                       int
	RecoveryCompleted, RecoveryRetried, RecoveryConflicted, RecoveryPendingRepair int
	EstimatedInstructionTokensAdded, EstimatedContextTokensAvoided                int
	Diagnostics                                                                   []Diagnostic
	DiagnosticsDropped                                                            int
}

// Service synchronously promotes completed Work. Feature gating belongs to its
// caller. Reports contain only bounded safe metadata, never candidate text or
// evidence bodies.
type Service struct {
	st          *store.Store
	projects    *projects.Service
	targetBytes int
	// Emit receives a file-change event only after its unique history row exists.
	Emit func(protocol.FileChangeData)
	// EmitRecovery optionally attributes recovery publication separately. When
	// absent, recovery preserves the original Emit callback contract.
	EmitRecovery func(protocol.FileChangeData)
	// boundary permits deterministic failures at commit-protocol boundaries.
	boundary func(string) error
}

func New(st *store.Store, projects *projects.Service, targetBytes int) *Service {
	return &Service{st: st, projects: projects, targetBytes: targetBytes}
}

// Shared across service instances, so startup recovery and completed Works use
// the same serialization boundary in this process.
var projectLocks sync.Map

// Prepared operation snapshots are bounded to this limit. An oversized live
// target is retryable before preparation, but necessarily a third state after it.
var errTargetSizeLimit = errors.New("memory target size limit")

func projectLock(id string) *sync.Mutex {
	v, _ := projectLocks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func (s *Service) step(stage string) error {
	if s.boundary != nil {
		return s.boundary(stage)
	}
	return nil
}

func (r *Report) outcome(status string) {
	switch status {
	case protocol.MemoryOutcomePromoted:
		r.Promoted++
	case protocol.MemoryOutcomeRejected:
		r.Rejected++
	case protocol.MemoryOutcomeStale:
		r.Stale++
	case protocol.MemoryOutcomeConflicted:
		r.Conflicted++
	default:
		r.Pending++
	}
}

// PromoteCompleted processes a fixed candidate-ID snapshot. An empty snapshot
// returns before any promotion-table query or project filesystem access.
func (s *Service) PromoteCompleted(ctx context.Context, req Request) Report {
	var report Report
	if req.Project.ID == "" || req.WorkID == "" {
		return report
	}
	lock := projectLock(req.Project.ID)
	lock.Lock()
	defer lock.Unlock()
	detail, err := s.st.GetWorkDetail(ctx, req.WorkID)
	if err != nil {
		report.Pending++
		diag := requestDiagnostic(req)
		diag.Status = protocol.MemoryOutcomePending
		diag.Reason = ReasonStorage
		report.diagnostic(diag)
		return report
	}
	var ids []string
	for _, n := range detail.Nodes {
		if n.Kind == protocol.NodeMemoryCandidate && n.Status == protocol.StatusPending && active(n) {
			ids = append(ids, n.ID)
		}
	}
	if len(ids) == 0 {
		return report
	}
	sort.Strings(ids)
	var project protocol.Project
	initial := Outcome{}
	if detail.Work.ProjectID != req.Project.ID || detail.Work.ThreadID != req.ThreadID || detail.Work.Status != protocol.WorkCompleted {
		initial = Outcome{protocol.MemoryOutcomeStale, ReasonWorkNotCompleted}
	} else {
		project, err = s.st.GetProject(ctx, req.Project.ID)
		if err != nil {
			initial = Outcome{protocol.MemoryOutcomePending, ReasonStorage}
		} else if project.Root != req.Project.Root {
			initial = Outcome{protocol.MemoryOutcomeStale, ReasonRevisionMismatch}
		}
	}
	for _, id := range ids {
		if initial.Status != "" {
			for _, n := range detail.Nodes {
				if n.ID == id {
					diag := candidateDiagnostic(req, detail, n)
					diag.Status, diag.Reason = initial.Status, initial.Reason
					report.outcome(initial.Status)
					report.diagnostic(diag)
					break
				}
			}
			continue
		}
		s.promoteCandidate(ctx, req, project, id, &report)
	}
	return report
}

func (s *Service) promoteCandidate(ctx context.Context, req Request, project protocol.Project, id string, report *Report) {
	diag := requestDiagnostic(req)
	diag.CandidateNodeID = id
	diag.Status, diag.Reason = protocol.MemoryOutcomePending, ReasonStorage
	defer func() { report.diagnostic(diag) }()
	d, err := s.st.GetWorkDetail(ctx, req.WorkID)
	if err != nil {
		report.Pending++
		return
	}
	var candidate protocol.WorkNode
	for _, n := range d.Nodes {
		if n.ID == id {
			candidate = n
			break
		}
	}
	diag = candidateDiagnostic(req, d, candidate)
	diag.Status, diag.Reason = protocol.MemoryOutcomePending, ReasonStorage
	rows, err := s.st.ListProjectMemories(ctx, project.ID)
	if err != nil {
		report.Pending++
		return
	}
	in := QualifyInput{Detail: d, Candidate: candidate, ProjectRoot: project.Root, FinalRevision: finalRevision(d), ActiveMemories: rows}
	proposal, duplicate, out := promotionProposal(in)
	if out.Status != "" {
		out = s.curate(ctx, candidate, out, report)
		diag.Status, diag.Reason = out.Status, out.Reason
		return
	}
	inventory, err := projects.InstructionInventory(project.Root, proposal.ScopePaths)
	if err != nil {
		out = s.curate(ctx, candidate, Outcome{protocol.MemoryOutcomeConflicted, ReasonInstructionInventoryInvalid}, report)
		diag.Status, diag.Reason = out.Status, out.Reason
		return
	}
	placement, out := ResolvePlacement(PlacementInput{ProjectRoot: project.Root, ScopePaths: proposal.ScopePaths, Existing: inventory})
	if out.Status != "" {
		out = s.curate(ctx, candidate, out, report)
		diag.Status, diag.Reason = out.Status, out.Reason
		return
	}
	if duplicate.ID != "" {
		placement.RelativePath = duplicate.TargetPath
	}
	diag.TargetPath = placement.RelativePath
	target, err := openTarget(project.Root, placement.RelativePath)
	if err != nil {
		out = s.curate(ctx, candidate, Outcome{errorOutcome(err), failureReason(err, ReasonFileIO)}, report)
		diag.Status, diag.Reason = out.Status, out.Reason
		return
	}
	defer target.close()
	s.promote(ctx, req, d, candidate, proposal, duplicate, rows, target, report, &diag)
}

func (s *Service) curate(ctx context.Context, candidate protocol.WorkNode, out Outcome, report *Report) Outcome {
	if out.Status != protocol.MemoryOutcomePending {
		if err := s.st.SetMemoryCandidateOutcome(ctx, candidate.WorkID, candidate.ID, candidate.Revision, out.Status); err != nil {
			out = Outcome{errorOutcome(err), failureReason(err, ReasonStorage)}
		}
	}
	report.outcome(out.Status)
	return out
}

func errorOutcome(err error) string {
	if errors.Is(err, store.ErrMemoryConflict) {
		return protocol.MemoryOutcomeConflicted
	}
	if errors.Is(err, store.ErrMemoryStale) {
		return protocol.MemoryOutcomeStale
	}
	return protocol.MemoryOutcomePending
}

func promotionProposal(in QualifyInput) (Proposal, protocol.ProjectMemory, Outcome) {
	p, out := Qualify(in)
	if out.Status != protocol.MemoryOutcomePromoted || out.Reason != ReasonAlreadyCurrent {
		return p, protocol.ProjectMemory{}, out
	}
	c, err := work.DecodeMemoryCandidate(in.Candidate)
	if err != nil {
		return Proposal{}, protocol.ProjectMemory{}, Outcome{Status: protocol.MemoryOutcomeStale, Reason: ReasonCandidateInactive}
	}
	for _, row := range in.ActiveMemories {
		if row.ProjectID == in.Detail.Work.ProjectID && row.SemanticKey == c.SemanticKey && row.Status == protocol.MemoryStatusActive {
			ids := append([]string(nil), in.Candidate.EvidenceIDs...)
			sort.Strings(ids)
			return Proposal{WorkID: in.Detail.Work.ID, CandidateNodeID: in.Candidate.ID, SemanticKey: c.SemanticKey, Category: c.Category, Text: c.Text, SourceRevision: c.SourceRevision, ScopePaths: c.ScopePaths, EvidenceIDs: ids, ReplacesMemory: c.ReplacesMemory}, row, Outcome{}
		}
	}
	return Proposal{}, protocol.ProjectMemory{}, Outcome{Status: protocol.MemoryOutcomeStale, Reason: ReasonCandidateInactive}
}

func finalRevision(d protocol.WorkDetail) string {
	var last *protocol.Fingerprint
	for i := range d.Fingerprints {
		f := &d.Fingerprints[i]
		if f.Kind != protocol.FingerprintTurnEnd && f.Kind != protocol.FingerprintVerification {
			continue
		}
		if last == nil || !f.TakenAt.Before(last.TakenAt) {
			last = f
		}
	}
	if last == nil {
		return ""
	}
	return last.Value
}

func (s *Service) promote(ctx context.Context, req Request, d protocol.WorkDetail, candidate protocol.WorkNode, p Proposal, duplicate protocol.ProjectMemory, rows []protocol.ProjectMemory, target *fileTarget, report *Report, diag *Diagnostic) {
	diag.BytesBefore, diag.BytesAfter = len(target.before), len(target.before)
	diag.BeforeHash, diag.AfterHash = memoryHash(target.before), memoryHash(target.before)
	var applicable []protocol.ProjectMemory
	for _, row := range rows {
		if row.TargetPath == target.rel {
			applicable = append(applicable, row)
		}
	}
	merge, out := Merge(MergeInput{Current: target.before, Active: applicable, Proposals: []Proposal{p}, TargetBytes: s.targetBytes, TargetPath: target.rel})
	if out.Status != "" {
		out = s.curate(ctx, candidate, out, report)
		diag.Status, diag.Reason = out.Status, out.Reason
		return
	}
	if err := s.step("prepare"); err != nil {
		diag.Status, diag.Reason = protocol.MemoryOutcomePending, ReasonStorage
		report.Pending++
		return
	}
	diag.BeforeHash, diag.AfterHash = merge.BeforeHash, merge.AfterHash
	diag.BytesAfter = len(merge.After)
	evidence, _ := json.Marshal(p.EvidenceIDs)
	op, err := s.st.PrepareMemoryPromotion(ctx, store.PrepareMemoryPromotion{
		Memory:               protocol.ProjectMemory{UserOwned: len(merge.Adopted) > 0, ProjectID: req.Project.ID, WorkID: p.WorkID, CandidateNodeID: p.CandidateNodeID, SemanticKey: p.SemanticKey, Category: p.Category, TargetPath: target.rel, Text: p.Text, TextHash: memoryHash([]byte(p.Text)), SourceRevision: p.SourceRevision, EvidenceJSON: string(evidence), FileHashBefore: merge.BeforeHash, FileHashAfter: merge.AfterHash},
		ExpectedWorkRevision: d.Work.Revision, ExpectedCandidateRevision: candidate.Revision, ReplacesMemory: p.ReplacesMemory, ThreadID: req.ThreadID, TurnID: req.TurnID, BeforeBytes: target.before, AfterBytes: merge.After,
		Validate: func(snapshot store.MemoryPromotionSnapshot) error {
			if snapshot.ProjectRoot != req.Project.Root {
				return store.ErrMemoryStale
			}
			current, existing, out := promotionProposal(QualifyInput{Detail: snapshot.Detail, Candidate: snapshot.Candidate, ProjectRoot: snapshot.ProjectRoot, FinalRevision: snapshot.FinalRevision, ActiveMemories: snapshot.ActiveMemories})
			if out.Status == protocol.MemoryOutcomeConflicted {
				return store.ErrMemoryConflict
			}
			if out.Status != "" || !reflect.DeepEqual(current, p) || !reflect.DeepEqual(existing, duplicate) {
				return store.ErrMemoryStale
			}
			return nil
		},
	})
	if err != nil {
		out = s.curate(ctx, candidate, Outcome{errorOutcome(err), failureReason(err, ReasonStorage)}, report)
		diag.Status, diag.Reason = out.Status, out.Reason
		return
	}
	diag.OperationID = op.ID
	if err = s.step("recheck"); err == nil {
		err = target.check(merge.BeforeHash)
	}
	if err == nil && merge.BeforeHash != merge.AfterHash {
		err = target.replace(merge.After, s.step)
	}
	if err == nil {
		err = s.finish(ctx, req.Project, op, s.Emit)
	}
	if err != nil {
		out = s.fail(ctx, op, err, ReasonFileIO, report)
		diag.Status, diag.Reason = out.Status, out.Reason
		return
	}
	diag.Status, diag.Reason = protocol.MemoryOutcomePromoted, ReasonInserted
	diag.Inserted, diag.Replaced, diag.Unchanged = len(merge.Inserted), len(merge.Replaced), len(merge.Unchanged)
	if diag.Unchanged > 0 {
		diag.Reason = ReasonAlreadyCurrent
		diag.EstimatedContextTokensAvoided = (len(p.Text) + 3) / 4
	}
	if diag.Replaced > 0 {
		diag.Reason = ReasonReplaced
		for _, row := range rows {
			if row.ID == p.ReplacesMemory {
				diag.EstimatedContextTokensAvoided = (len(row.Text) + 3) / 4
				break
			}
		}
	}
	if added := diag.BytesAfter - diag.BytesBefore; added > 0 {
		diag.EstimatedInstructionTokensAdded = (added + 3) / 4
	}
	report.Promoted++
	report.Inserted += len(merge.Inserted)
	report.Replaced += len(merge.Replaced)
	report.Unchanged += len(merge.Unchanged)
	report.BytesBefore += len(target.before)
	report.BytesAfter += len(merge.After)
}

func (s *Service) finish(ctx context.Context, project protocol.Project, op protocol.MemoryPromotionOp, emit func(protocol.FileChangeData)) error {
	if op.FileHashBefore != op.FileHashAfter {
		if err := s.step("record"); err != nil {
			return classified(err, ReasonStorage)
		}
		var before *string
		if op.BeforeBytes != nil {
			v := string(op.BeforeBytes)
			before = &v
		}
		_, err := s.projects.NewRecorder(project, op.ThreadID, op.TurnID, emit).RecordPromotion(ctx, op.ID, filepath.Join(project.Root, filepath.FromSlash(op.TargetPath)), before)
		if err != nil {
			return classified(err, ReasonStorage)
		}
		if err := s.step("mark_written"); err != nil {
			return classified(err, ReasonStorage)
		}
		if err := s.st.MarkMemoryFileWritten(ctx, op.ID); err != nil {
			return classified(err, ReasonStorage)
		}
	}
	if err := s.step("finalize"); err != nil {
		return classified(err, ReasonStorage)
	}
	return classified(s.st.CommitMemoryPromotion(ctx, op.ID, time.Now()), ReasonStorage)
}

func (s *Service) fail(ctx context.Context, op protocol.MemoryPromotionOp, err error, fallback string, report *Report) Outcome {
	state, status, class := protocol.MemoryOpPendingRepair, protocol.MemoryOutcomePending, "retryable"
	if errors.Is(err, store.ErrMemoryConflict) || errors.Is(err, errTargetSizeLimit) {
		state, status, class = protocol.MemoryOpConflicted, protocol.MemoryOutcomeConflicted, "compare_and_swap"
	}
	// If persistence itself is unavailable the prepared/file_written operation
	// already retains exact snapshots for the next startup reconciliation.
	if failure := s.st.FailMemoryPromotion(ctx, op.ID, state, status, class); failure != nil {
		status = protocol.MemoryOutcomePending
		err = classified(failure, ReasonStorage)
	}
	report.outcome(status)
	return Outcome{status, failureReason(err, fallback)}
}
