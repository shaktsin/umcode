package memory

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/vault"
	"github.com/shaktsin/umcode/internal/work"
)

// Diagnostic is a bounded metadata projection. It contains no guidance,
// semantic keys, evidence bodies, filesystem snapshots, or error/panic values.
type Diagnostic struct {
	ProjectID, WorkID, CandidateNodeID, ThreadID, TurnID, OperationID string
	SourceRevision                                                    string
	SourceNodeIDs, EvidenceIDs                                        []string
	TargetPath, BeforeHash, AfterHash, Status, Reason, Recovery       string
	BytesBefore, BytesAfter, Inserted, Replaced, Unchanged, Conflicts int
	EstimatedInstructionTokensAdded, EstimatedContextTokensAvoided    int
}

const MaxDiagnostics = 128
const (
	ReasonInserted            = "inserted"
	ReasonReplaced            = "replaced"
	ReasonStorage             = "storage"
	ReasonFileIO              = "file_io"
	ReasonUnsupportedPlatform = "unsupported_platform"
	ReasonCompareAndSwap      = "compare_and_swap"
	ReasonRecoveryCompleted   = "recovery_completed"
	ReasonRecoveryRetried     = "recovery_retried"
)

var errUnsupportedPlatform = errors.New("contained memory writes unavailable")
var diagnosticIdentity = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
var diagnosticHash = regexp.MustCompile(`^[a-fA-F0-9]{40}([a-fA-F0-9]{24})?$`)

func safeIdentity(value string) string {
	if value == "" {
		return ""
	}
	_, redacted := vault.Redact([]byte(value))
	if diagnosticIdentity.MatchString(value) && !redacted {
		return value
	}
	return "sha256:" + memoryHash([]byte(value))
}
func safeRevision(value string) string {
	if value == "" {
		return ""
	}
	if diagnosticHash.MatchString(value) {
		return strings.ToLower(value)
	}
	// Final fingerprints are canonical hashes in production. Hash noncanonical
	// legacy/test revisions instead of exposing arbitrary persisted prose.
	return "sha256:" + memoryHash([]byte(value))
}
func safeIdentities(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	if len(values) > work.MaxNodeEvidenceIDs {
		values = values[:work.MaxNodeEvidenceIDs]
	}
	for i := range values {
		values[i] = safeIdentity(values[i])
	}
	return values
}
func (r *Report) diagnostic(d Diagnostic) {
	d.ProjectID = safeIdentity(d.ProjectID)
	d.WorkID = safeIdentity(d.WorkID)
	d.CandidateNodeID = safeIdentity(d.CandidateNodeID)
	d.ThreadID = safeIdentity(d.ThreadID)
	d.TurnID = safeIdentity(d.TurnID)
	d.OperationID = safeIdentity(d.OperationID)
	d.SourceRevision = safeRevision(d.SourceRevision)
	d.SourceNodeIDs = safeIdentities(d.SourceNodeIDs)
	d.EvidenceIDs = safeIdentities(d.EvidenceIDs)
	if !memoryTarget(d.TargetPath) || len(d.TargetPath) > 4096 {
		d.TargetPath = ""
	} else if _, secret := vault.Redact([]byte(d.TargetPath)); secret {
		d.TargetPath = ""
	}
	if d.BeforeHash != "" && !diagnosticHash.MatchString(d.BeforeHash) {
		d.BeforeHash = safeRevision(d.BeforeHash)
	}
	if d.AfterHash != "" && !diagnosticHash.MatchString(d.AfterHash) {
		d.AfterHash = safeRevision(d.AfterHash)
	}
	if d.Status == protocol.MemoryOutcomeConflicted {
		d.Conflicts = 1
	}
	r.EstimatedInstructionTokensAdded += d.EstimatedInstructionTokensAdded
	r.EstimatedContextTokensAvoided += d.EstimatedContextTokensAvoided
	if len(r.Diagnostics) < MaxDiagnostics {
		r.Diagnostics = append(r.Diagnostics, d)
	} else {
		r.DiagnosticsDropped++
	}
}
func requestDiagnostic(req Request) Diagnostic {
	return Diagnostic{ProjectID: req.Project.ID, WorkID: req.WorkID, ThreadID: req.ThreadID, TurnID: req.TurnID}
}
func candidateDiagnostic(req Request, d protocol.WorkDetail, c protocol.WorkNode) Diagnostic {
	result := requestDiagnostic(req)
	result.CandidateNodeID = c.ID
	result.SourceRevision = finalRevision(d)
	result.EvidenceIDs = append([]string(nil), c.EvidenceIDs...)
	for _, e := range d.Edges {
		if e.WorkID == d.Work.ID && e.FromNodeID == c.ID && e.Relation == protocol.RelCandidateFor {
			result.SourceNodeIDs = append(result.SourceNodeIDs, e.ToNodeID)
		}
	}
	return result
}
func (s *Service) operationDiagnostic(ctx context.Context, op protocol.MemoryPromotionOp) Diagnostic {
	result := requestDiagnostic(Request{Project: protocol.Project{ID: op.ProjectID}, WorkID: op.WorkID, ThreadID: op.ThreadID, TurnID: op.TurnID})
	if detail, err := s.st.GetWorkDetail(ctx, op.WorkID); err == nil {
		for _, n := range detail.Nodes {
			if n.ID == op.CandidateNodeID {
				result = candidateDiagnostic(Request{Project: protocol.Project{ID: op.ProjectID}, WorkID: op.WorkID, ThreadID: op.ThreadID, TurnID: op.TurnID}, detail, n)
				if c, err := work.DecodeMemoryCandidate(n); err == nil {
					if op.FileHashBefore == op.FileHashAfter {
						result.Unchanged = 1
						result.EstimatedContextTokensAvoided = (len(c.Text) + 3) / 4
					} else if c.ReplacesMemory != "" {
						result.Replaced = 1
						if prior, err := s.st.GetProjectMemory(ctx, c.ReplacesMemory); err == nil {
							result.EstimatedContextTokensAvoided = (len(prior.Text) + 3) / 4
						}
					} else {
						result.Inserted = 1
					}
				}
				break
			}
		}
	}
	if result.SourceRevision == "" {
		if row, err := s.st.GetProjectMemory(ctx, op.MemoryID); err == nil {
			result.SourceRevision = row.SourceRevision
			_ = json.Unmarshal([]byte(row.EvidenceJSON), &result.EvidenceIDs)
		}
	}
	result.CandidateNodeID = op.CandidateNodeID
	result.OperationID = op.ID
	result.TargetPath = op.TargetPath
	result.BeforeHash = op.FileHashBefore
	result.AfterHash = op.FileHashAfter
	result.BytesBefore = len(op.BeforeBytes)
	result.BytesAfter = len(op.AfterBytes)
	if added := result.BytesAfter - result.BytesBefore; added > 0 {
		result.EstimatedInstructionTokensAdded = (added + 3) / 4
	}
	return result
}

type diagnosticError struct {
	err    error
	reason string
}

func (e *diagnosticError) Error() string { return e.err.Error() }
func (e *diagnosticError) Unwrap() error { return e.err }
func classified(err error, reason string) error {
	if err == nil {
		return nil
	}
	return &diagnosticError{err: err, reason: reason}
}
func failureReason(err error, fallback string) string {
	switch {
	case errors.Is(err, store.ErrMemoryConflict):
		return ReasonCompareAndSwap
	case errors.Is(err, store.ErrMemoryStale):
		return ReasonRevisionMismatch
	case errors.Is(err, errTargetSizeLimit):
		return ReasonSizeLimit
	case errors.Is(err, errUnsupportedPlatform):
		return ReasonUnsupportedPlatform
	}
	var typed *diagnosticError
	if errors.As(err, &typed) {
		return typed.reason
	}
	return fallback
}
