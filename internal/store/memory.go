package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

const projectMemoryCols = `id, project_id, work_id, candidate_node_id, semantic_key, category, target_path,
	text, text_hash, status, source_revision, evidence_json, file_hash_before, file_hash_after, superseded_by, created_at, promoted_at`

func scanProjectMemory(sc interface{ Scan(...any) error }) (protocol.ProjectMemory, error) {
	var m protocol.ProjectMemory
	var created string
	var promoted sql.NullString
	err := sc.Scan(&m.ID, &m.ProjectID, &m.WorkID, &m.CandidateNodeID, &m.SemanticKey, &m.Category, &m.TargetPath,
		&m.Text, &m.TextHash, &m.Status, &m.SourceRevision, &m.EvidenceJSON, &m.FileHashBefore, &m.FileHashAfter,
		&m.SupersededBy, &created, &promoted)
	if err != nil {
		return m, err
	}
	m.CreatedAt, m.PromotedAt = ParseTime(created), nullTime(promoted)
	return m, nil
}

// GetProjectMemory loads a current or historical memory row by ID.
func (s *Store) GetProjectMemory(ctx context.Context, id string) (protocol.ProjectMemory, error) {
	m, err := scanProjectMemory(s.DB.QueryRowContext(ctx, `SELECT `+projectMemoryCols+` FROM project_memories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// ListProjectMemories loads all active semantic identities, across target files.
func (s *Store) ListProjectMemories(ctx context.Context, projectID string) ([]protocol.ProjectMemory, error) {
	return activeMemories(ctx, s.DB, projectID)
}

// GetMemoryPromotionOp supplies exact persisted snapshots to the strict recorder.
func (s *Store) GetMemoryPromotionOp(ctx context.Context, id string) (protocol.MemoryPromotionOp, error) {
	op, err := scanMemoryPromotionOp(s.DB.QueryRowContext(ctx, `SELECT `+memoryPromotionOpCols+` FROM memory_promotion_ops WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return op, ErrNotFound
	}
	return op, err
}

// SetMemoryCandidateOutcome curates only the expected pending candidate of a
// completed Work; it cannot change a candidate reserved by a prepared operation.
func (s *Store) SetMemoryCandidateOutcome(ctx context.Context, workID, candidateID string, revision int, status string) error {
	if status != protocol.MemoryOutcomeRejected && status != protocol.MemoryOutcomeStale && status != protocol.MemoryOutcomeConflicted {
		return ErrMemoryStale
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE work_nodes SET status=?, revision=revision+1, updated_at=?
 WHERE id=? AND work_id=? AND kind='memory_candidate' AND status='pending' AND revision=?
 AND valid_until IS NULL AND superseded_by = ''
 AND EXISTS (SELECT 1 FROM works WHERE id=? AND status='completed')
 AND NOT EXISTS (SELECT 1 FROM memory_promotion_ops WHERE candidate_node_id=? AND state IN ('prepared','file_written','pending_repair'))`, status, Now(), candidateID, workID, revision, workID, candidateID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrMemoryStale
	}
	return err
}

// ListActiveProjectMemories returns generated entries for one project file.
func (s *Store) ListActiveProjectMemories(ctx context.Context, projectID, targetPath string) ([]protocol.ProjectMemory, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+projectMemoryCols+` FROM project_memories
		WHERE project_id = ? AND target_path = ? AND status = ? ORDER BY created_at, id`, projectID, targetPath, protocol.MemoryStatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.ProjectMemory
	for rows.Next() {
		m, err := scanProjectMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const memoryPromotionOpCols = `id, project_id, work_id, candidate_node_id, memory_id, thread_id, turn_id, target_path,
	state, file_hash_before, file_hash_after, before_bytes, after_bytes, error_class, created_at, updated_at`

func scanMemoryPromotionOp(sc interface{ Scan(...any) error }) (protocol.MemoryPromotionOp, error) {
	var op protocol.MemoryPromotionOp
	var created, updated string
	err := sc.Scan(&op.ID, &op.ProjectID, &op.WorkID, &op.CandidateNodeID, &op.MemoryID, &op.ThreadID, &op.TurnID,
		&op.TargetPath, &op.State, &op.FileHashBefore, &op.FileHashAfter, &op.BeforeBytes, &op.AfterBytes,
		&op.ErrorClass, &created, &updated)
	if err != nil {
		return op, err
	}
	op.CreatedAt, op.UpdatedAt = ParseTime(created), ParseTime(updated)
	return op, nil
}

// ListIncompleteMemoryPromotionOps returns operations requiring restart recovery.
// Conflicted operations are terminal; pending repair remains visible for repair.
func (s *Store) ListIncompleteMemoryPromotionOps(ctx context.Context) ([]protocol.MemoryPromotionOp, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+memoryPromotionOpCols+` FROM memory_promotion_ops
		WHERE state IN (?, ?, ?) ORDER BY created_at, id`, protocol.MemoryOpPrepared, protocol.MemoryOpFileWritten, protocol.MemoryOpPendingRepair)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.MemoryPromotionOp
	for rows.Next() {
		op, err := scanMemoryPromotionOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// MemoryConflictError and MemoryStaleError carry no candidate or evidence text.
type MemoryConflictError struct{}

func (*MemoryConflictError) Error() string { return "memory promotion conflict" }

type MemoryStaleError struct{}

func (*MemoryStaleError) Error() string { return "memory promotion stale" }

var ErrMemoryConflict = &MemoryConflictError{}
var ErrMemoryStale = &MemoryStaleError{}

// MemoryPromotionSnapshot contains canonical data read under the writer lock.
// Validate must be pure: querying this Store from the callback would deadlock its
// single connection. The coordinator uses its qualifier with this snapshot and
// compares the qualified proposal with the prepared input.
type MemoryPromotionSnapshot struct {
	Detail                     protocol.WorkDetail
	Candidate                  protocol.WorkNode
	ProjectRoot, FinalRevision string
	ActiveMemories             []protocol.ProjectMemory
}

// PrepareMemoryPromotion holds immutable proposed provenance and exact file
// snapshots. Memory's lifecycle fields are assigned by the store. Validate is
// required to recheck source, category and verification rules without importing
// internal/memory (which depends on store through internal/work).
type PrepareMemoryPromotion struct {
	Memory                                          protocol.ProjectMemory
	ExpectedWorkRevision, ExpectedCandidateRevision int
	ReplacesMemory                                  string
	ThreadID, TurnID                                string
	BeforeBytes, AfterBytes                         []byte
	Validate                                        func(MemoryPromotionSnapshot) error
}

func memoryHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func activeMemories(ctx context.Context, q workQuerier, projectID string) ([]protocol.ProjectMemory, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+projectMemoryCols+` FROM project_memories WHERE project_id=? AND status='active' ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.ProjectMemory
	for rows.Next() {
		m, err := scanProjectMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type memoryCandidateIdentity struct {
	Category       string `json:"category"`
	SemanticKey    string `json:"semantic_key"`
	Text           string `json:"text"`
	SourceRevision string `json:"source_revision"`
	ReplacesMemory string `json:"replaces_memory"`
}

// Qualification normalizes any supported bullet prefix to a Markdown dash.
// Structural safety remains the required qualifier callback's responsibility.
func canonicalMemoryCandidateText(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "* ") || strings.HasPrefix(text, "+ ") {
		text = strings.TrimSpace(text[2:])
	}
	return "- " + text
}

// PrepareMemoryPromotion reserves the semantic identity before any file write.
// BEGIN IMMEDIATE serializes both reservation checks and provisional inserts.
func (s *Store) PrepareMemoryPromotion(ctx context.Context, in PrepareMemoryPromotion) (protocol.MemoryPromotionOp, error) {
	var zero protocol.MemoryPromotionOp
	m := in.Memory
	if in.Validate == nil || m.ProjectID == "" || m.WorkID == "" || m.CandidateNodeID == "" || m.SemanticKey == "" || m.Text == "" || m.SourceRevision == "" || in.TurnID == "" || len(in.BeforeBytes) > 32<<10 || len(in.AfterBytes) > 32<<10 || memoryHash(in.BeforeBytes) != m.FileHashBefore || memoryHash(in.AfterBytes) != m.FileHashAfter || memoryHash([]byte(m.Text)) != m.TextHash {
		return zero, ErrMemoryStale
	}
	if path.IsAbs(m.TargetPath) || path.Clean(m.TargetPath) != m.TargetPath || strings.HasPrefix(m.TargetPath, "../") || path.Base(m.TargetPath) != "UMCODE.md" {
		return zero, ErrMemoryConflict
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	d, err := getWorkDetail(ctx, tx, m.WorkID)
	if errors.Is(err, ErrNotFound) {
		return zero, ErrMemoryStale
	}
	if err != nil {
		return zero, err
	}
	if d.Work.Status != protocol.WorkCompleted || d.Work.ProjectID != m.ProjectID || d.Work.ThreadID != in.ThreadID || in.ExpectedWorkRevision < 1 || d.Work.Revision != in.ExpectedWorkRevision {
		return zero, ErrMemoryStale
	}
	var candidate protocol.WorkNode
	for _, n := range d.Nodes {
		if n.ID == m.CandidateNodeID {
			candidate = n
			break
		}
	}
	if candidate.Kind != protocol.NodeMemoryCandidate || candidate.Status != protocol.StatusPending || candidate.ValidUntil != nil || candidate.SupersededBy != "" || in.ExpectedCandidateRevision < 1 || candidate.Revision != in.ExpectedCandidateRevision {
		return zero, ErrMemoryStale
	}
	var identity memoryCandidateIdentity
	if json.Unmarshal(candidate.Content, &identity) != nil || identity.Category != m.Category || identity.SemanticKey != m.SemanticKey || canonicalMemoryCandidateText(identity.Text) != m.Text || identity.SourceRevision != m.SourceRevision || identity.ReplacesMemory != in.ReplacesMemory {
		return zero, ErrMemoryStale
	}
	var evidenceIDs []string
	if json.Unmarshal([]byte(m.EvidenceJSON), &evidenceIDs) != nil || len(evidenceIDs) == 0 {
		return zero, ErrMemoryStale
	}
	sort.Strings(evidenceIDs)
	linked := append([]string(nil), candidate.EvidenceIDs...)
	sort.Strings(linked)
	if !reflect.DeepEqual(evidenceIDs, linked) {
		return zero, ErrMemoryStale
	}
	available := map[string]bool{}
	for _, e := range d.Evidence {
		if e.StaleAt == nil && (e.VaultHash == "" || e.Availability == protocol.AvailAvailable) {
			available[e.ID] = true
		}
	}
	for i, id := range evidenceIDs {
		if !available[id] || i > 0 && id == evidenceIDs[i-1] {
			return zero, ErrMemoryStale
		}
	}
	snapshot := MemoryPromotionSnapshot{Detail: d, Candidate: candidate, FinalRevision: finalMemoryRevision(d)}
	if snapshot.FinalRevision == "" || snapshot.FinalRevision != m.SourceRevision {
		return zero, ErrMemoryStale
	}
	if err := tx.QueryRowContext(ctx, `SELECT root FROM projects WHERE id=?`, m.ProjectID).Scan(&snapshot.ProjectRoot); errors.Is(err, sql.ErrNoRows) {
		return zero, ErrMemoryStale
	} else if err != nil {
		return zero, err
	}
	snapshot.ActiveMemories, err = activeMemories(ctx, tx, m.ProjectID)
	if err != nil {
		return zero, err
	}
	var current *protocol.ProjectMemory
	for i := range snapshot.ActiveMemories {
		row := &snapshot.ActiveMemories[i]
		if row.SemanticKey == m.SemanticKey {
			current = row
		}
	}
	// Pending-repair operations retain their reservation until recovery resolves
	// their exact bytes. Reserve the candidate as well, across changed proposals.
	var reserved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_promotion_ops o JOIN project_memories m ON m.id=o.memory_id
 WHERE o.project_id=? AND o.state IN ('prepared','file_written','pending_repair') AND (m.semantic_key=? OR o.candidate_node_id=?)`, m.ProjectID, m.SemanticKey, m.CandidateNodeID).Scan(&reserved); err != nil {
		return zero, err
	}
	if reserved != 0 {
		return zero, ErrMemoryConflict
	}
	duplicate := current != nil && current.Category == m.Category && current.Text == m.Text && current.TextHash == m.TextHash && current.TargetPath == m.TargetPath
	if in.ReplacesMemory != "" && (current == nil || current.ID != in.ReplacesMemory || current.TargetPath != m.TargetPath || memoryHash([]byte(current.Text)) != current.TextHash || current.SupersededBy != "") {
		return zero, ErrMemoryConflict
	}
	if current != nil && !duplicate && in.ReplacesMemory == "" {
		return zero, ErrMemoryConflict
	}
	if duplicate && (!bytes.Equal(in.BeforeBytes, in.AfterBytes) || m.FileHashBefore != m.FileHashAfter) {
		return zero, ErrMemoryConflict
	}
	if err := in.Validate(snapshot); err != nil {
		return zero, err
	}
	at := time.Now().UTC()
	if duplicate {
		m.ID = current.ID
	} else {
		if m.ID == "" {
			m.ID = NewID("mem")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO project_memories (`+projectMemoryCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.ID, m.ProjectID, m.WorkID, m.CandidateNodeID, m.SemanticKey, m.Category, m.TargetPath, m.Text, m.TextHash, protocol.MemoryStatusPendingRepair, m.SourceRevision, m.EvidenceJSON, m.FileHashBefore, m.FileHashAfter, "", FormatTime(at), nil)
		if err != nil {
			return zero, err
		}
	}
	op := protocol.MemoryPromotionOp{ID: NewID("mpo"), ProjectID: m.ProjectID, WorkID: m.WorkID, CandidateNodeID: m.CandidateNodeID, MemoryID: m.ID, ThreadID: in.ThreadID, TurnID: in.TurnID, TargetPath: m.TargetPath, State: protocol.MemoryOpPrepared, FileHashBefore: m.FileHashBefore, FileHashAfter: m.FileHashAfter, BeforeBytes: bytes.Clone(in.BeforeBytes), AfterBytes: bytes.Clone(in.AfterBytes), CreatedAt: at, UpdatedAt: at}
	var before, after any
	if in.BeforeBytes != nil {
		before = in.BeforeBytes
	}
	if in.AfterBytes != nil {
		after = in.AfterBytes
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_promotion_ops (`+memoryPromotionOpCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, op.ProjectID, op.WorkID, op.CandidateNodeID, op.MemoryID, op.ThreadID, op.TurnID, op.TargetPath, op.State, op.FileHashBefore, op.FileHashAfter, before, after, "", FormatTime(at), FormatTime(at))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return op, nil
}

// MarkMemoryFileWritten also resumes an exact pending-repair operation.
func (s *Store) MarkMemoryFileWritten(ctx context.Context, opID string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE memory_promotion_ops SET state='file_written',error_class='',updated_at=? WHERE id=? AND state IN ('prepared','pending_repair')`, Now(), opID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 0 {
		return nil
	}
	var state string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM memory_promotion_ops WHERE id=?`, opID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if state == protocol.MemoryOpFileWritten || state == protocol.MemoryOpCommitted {
		return nil
	}
	return ErrMemoryConflict
}

func (s *Store) CommitMemoryPromotion(ctx context.Context, opID string, promotedAt time.Time) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := scanMemoryPromotionOp(tx.QueryRowContext(ctx, `SELECT `+memoryPromotionOpCols+` FROM memory_promotion_ops WHERE id=?`, opID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if op.State == protocol.MemoryOpCommitted {
		return nil
	}
	if op.State != protocol.MemoryOpFileWritten && !(op.State == protocol.MemoryOpPrepared || op.State == protocol.MemoryOpPendingRepair) {
		return ErrMemoryConflict
	}
	if op.State != protocol.MemoryOpFileWritten && (op.FileHashBefore != op.FileHashAfter || !bytes.Equal(op.BeforeBytes, op.AfterBytes)) {
		return ErrMemoryConflict
	}
	m, err := scanProjectMemory(tx.QueryRowContext(ctx, `SELECT `+projectMemoryCols+` FROM project_memories WHERE id=?`, op.MemoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMemoryStale
	}
	if err != nil {
		return err
	}
	if m.ProjectID != op.ProjectID || m.TargetPath != op.TargetPath || m.Status != protocol.MemoryStatusActive && m.Status != protocol.MemoryStatusPendingRepair {
		return ErrMemoryConflict
	}
	if m.Status != protocol.MemoryStatusActive {
		if m.WorkID != op.WorkID || m.CandidateNodeID != op.CandidateNodeID {
			return ErrMemoryConflict
		}
		var content string
		if err := tx.QueryRowContext(ctx, `SELECT content_json FROM work_nodes WHERE id=? AND work_id=?`, op.CandidateNodeID, op.WorkID).Scan(&content); errors.Is(err, sql.ErrNoRows) {
			return ErrMemoryStale
		} else if err != nil {
			return err
		}
		var identity memoryCandidateIdentity
		if json.Unmarshal([]byte(content), &identity) != nil {
			return ErrMemoryStale
		}
		// The canonical candidate persists the replacement identity. SupersededBy
		// retains its normal meaning throughout preparation and recovery.
		current, err := scanProjectMemory(tx.QueryRowContext(ctx, `SELECT `+projectMemoryCols+` FROM project_memories WHERE project_id=? AND semantic_key=? AND status='active'`, m.ProjectID, m.SemanticKey))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if identity.ReplacesMemory != "" {
			if errors.Is(err, sql.ErrNoRows) || current.ID != identity.ReplacesMemory || current.TargetPath != m.TargetPath || current.SupersededBy != "" || memoryHash([]byte(current.Text)) != current.TextHash {
				return ErrMemoryConflict
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_memories SET status='superseded',superseded_by=? WHERE id=? AND status='active'`, m.ID, current.ID); err != nil {
				return err
			}
		} else if err == nil {
			return ErrMemoryConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_memories SET status='active',promoted_at=? WHERE id=?`, FormatTime(promotedAt), m.ID); err != nil {
			return err
		}
	}
	if err := transitionMemoryCandidate(ctx, tx, op, protocol.StatusPromoted, promotedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_promotion_ops SET state='committed',error_class='',updated_at=? WHERE id=?`, FormatTime(promotedAt), op.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailMemoryPromotion changes only lifecycle fields, preserving all immutable
// provenance and exact recovery snapshots. Active duplicate rows are untouched.
func (s *Store) FailMemoryPromotion(ctx context.Context, opID, state, candidateStatus, errorClass string) error {
	if state != protocol.MemoryOpConflicted && state != protocol.MemoryOpPendingRepair || state == protocol.MemoryOpConflicted && candidateStatus != protocol.StatusConflicted || state == protocol.MemoryOpPendingRepair && candidateStatus != protocol.StatusPending {
		return ErrMemoryConflict
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := scanMemoryPromotionOp(tx.QueryRowContext(ctx, `SELECT `+memoryPromotionOpCols+` FROM memory_promotion_ops WHERE id=?`, opID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if op.State == protocol.MemoryOpCommitted || op.State == protocol.MemoryOpConflicted && state != op.State {
		return ErrMemoryConflict
	}
	if op.State == state && op.ErrorClass == errorClass {
		return nil
	}
	at := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE project_memories SET status=? WHERE id=? AND status='pending_repair' AND work_id=? AND candidate_node_id=?`, state, op.MemoryID, op.WorkID, op.CandidateNodeID); err != nil {
		return err
	}
	if err := transitionMemoryCandidate(ctx, tx, op, candidateStatus, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_promotion_ops SET state=?,error_class=?,updated_at=? WHERE id=?`, state, errorClass, FormatTime(at), opID); err != nil {
		return err
	}
	return tx.Commit()
}
