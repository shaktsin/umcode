package store

import (
	"context"
	"database/sql"
	"errors"

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
