package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/vault"
	"strings"
	"time"
)

type memoryCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func encodeMemoryCursor(c memoryCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeMemoryCursor(s string) (memoryCursor, error) {
	var c memoryCursor
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil {
		return c, errors.New("invalid memory cursor")
	}
	if e = json.Unmarshal(b, &c); e != nil || c.ID == "" || c.At.IsZero() {
		return c, errors.New("invalid memory cursor")
	}
	return c, nil
}
func memoryReview(m protocol.ProjectMemory) protocol.MemoryReviewEntry {
	var ids []string
	_ = json.Unmarshal([]byte(m.EvidenceJSON), &ids)
	if ids == nil {
		ids = []string{}
	}
	return protocol.MemoryReviewEntry{ID: m.ID, ProjectID: m.ProjectID, SemanticKey: m.SemanticKey, Category: m.Category, TargetPath: m.TargetPath, Text: m.Text, TextHash: m.TextHash, Status: m.Status, SourceRevision: m.SourceRevision, EvidenceIDs: ids, UserOwned: m.UserOwned, CreatedAt: m.CreatedAt, PromotedAt: m.PromotedAt, Revision: 1}
}
func (s *Store) ListMemoryReview(ctx context.Context, p protocol.MemoryListParams) (protocol.MemoryListResult, error) {
	var out protocol.MemoryListResult
	if strings.TrimSpace(p.ProjectID) == "" {
		return out, errors.New("projectId is required")
	}
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return out, errors.New("memory page limit must be 1..100")
	}
	q := `SELECT ` + projectMemoryCols + ` FROM project_memories WHERE project_id=?`
	args := []any{p.ProjectID}
	if !p.IncludeHistory {
		q += ` AND status='active'`
	}
	if p.Cursor != "" {
		c, e := decodeMemoryCursor(p.Cursor)
		if e != nil {
			return out, e
		}
		q += ` AND (created_at>? OR (created_at=? AND id>?))`
		stamp := FormatTime(c.At)
		args = append(args, stamp, stamp, c.ID)
	}
	q += ` ORDER BY created_at,id LIMIT ?`
	args = append(args, limit+1)
	rows, e := s.DB.QueryContext(ctx, q, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	var ms []protocol.ProjectMemory
	for rows.Next() {
		m, e := scanProjectMemory(rows)
		if e != nil {
			return out, e
		}
		ms = append(ms, m)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(ms) > limit {
		ms = ms[:limit]
		last := ms[len(ms)-1]
		out.NextCursor = encodeMemoryCursor(memoryCursor{last.CreatedAt, last.ID})
	}
	out.Entries = make([]protocol.MemoryReviewEntry, 0, len(ms))
	for _, m := range ms {
		out.Entries = append(out.Entries, memoryReview(m))
	}
	return out, nil
}
func (s *Store) RecordMemoryRetirement(ctx context.Context, projectID, key, revision, hash string) error {
	if projectID == "" || key == "" || hash == "" {
		return errors.New("retirement identity is required")
	}
	_, e := s.DB.ExecContext(ctx, `INSERT INTO memory_retirements(id,project_id,semantic_key,source_revision,text_hash,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,semantic_key,source_revision,text_hash) DO NOTHING`, NewID("ret"), projectID, key, revision, hash, Now())
	return e
}
func (s *Store) MemoryCandidateRetired(ctx context.Context, projectID, key, revision, hash string) (bool, error) {
	var found int
	e := s.DB.QueryRowContext(ctx, `SELECT 1 FROM memory_retirements WHERE project_id=? AND semantic_key=? AND source_revision=? AND text_hash=?`, projectID, key, revision, hash).Scan(&found)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, fmt.Errorf("check memory retirement: %w", e)
	}
	return true, nil
}

// RetireProjectMemory performs status and tombstone changes atomically.
func (s *Store) RetireProjectMemory(ctx context.Context, id, expectedHash string) error {
	unlock := vault.LockLifecycle()
	defer unlock()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var project, key, revision, hash string
	e = tx.QueryRowContext(ctx, `SELECT project_id,semantic_key,source_revision,text_hash FROM project_memories WHERE id=? AND status='active'`, id).Scan(&project, &key, &revision, &hash)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if hash != expectedHash {
		return ErrMemoryStale
	}
	if _, e = tx.ExecContext(ctx, `UPDATE project_memories SET status='superseded',superseded_by='user_retired' WHERE id=? AND status='active' AND text_hash=?`, id, expectedHash); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM memory_vault_refs WHERE memory_id=?`, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO memory_retirements(id,project_id,semantic_key,source_revision,text_hash,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,semantic_key,source_revision,text_hash) DO NOTHING`, NewID("ret"), project, key, revision, hash, Now()); e != nil {
		return e
	}
	return tx.Commit()
}

// CorrectProjectMemory atomically retires the active record and creates an
// explicitly user-owned successor with preserved immutable provenance.
func (s *Store) CorrectProjectMemory(ctx context.Context, old protocol.ProjectMemory, text string, remains bool) (protocol.ProjectMemory, error) {
	unlock := vault.LockLifecycle()
	defer unlock()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return protocol.ProjectMemory{}, e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `UPDATE project_memories SET status='superseded',superseded_by='user_corrected' WHERE id=? AND project_id=? AND status='active' AND text_hash=?`, old.ID, old.ProjectID, old.TextHash)
	if e != nil {
		return protocol.ProjectMemory{}, e
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		if e != nil {
			return protocol.ProjectMemory{}, e
		}
		return protocol.ProjectMemory{}, ErrMemoryStale
	}
	m := old
	m.ID = NewID("mem")
	m.Text = text
	m.TextHash = memoryTextHash(text)
	m.UserOwned = true
	m.Status = protocol.MemoryStatusActive
	m.SupersededBy = ""
	m.FileHashBefore = ""
	m.FileHashAfter = ""
	m.CreatedAt = time.Now().UTC()
	m.PromotedAt = nil
	if remains {
		m.TargetPath = old.TargetPath
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO project_memories(id,project_id,work_id,candidate_node_id,semantic_key,category,target_path,text,text_hash,user_owned,status,source_revision,evidence_json,file_hash_before,file_hash_after,superseded_by,created_at,promoted_at) VALUES(?,?,?,?,?,?,?,?,?,1,'active',?,?, '', '', '', ?,NULL)`, m.ID, m.ProjectID, m.WorkID, m.CandidateNodeID, m.SemanticKey, m.Category, m.TargetPath, m.Text, m.TextHash, m.SourceRevision, m.EvidenceJSON, FormatTime(m.CreatedAt))
	if e != nil {
		return protocol.ProjectMemory{}, e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM memory_vault_refs WHERE memory_id=?`, old.ID); e != nil {
		return protocol.ProjectMemory{}, e
	}
	if e = pinMemoryEvidence(ctx, tx, m.ID, m.EvidenceJSON); e != nil {
		return protocol.ProjectMemory{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO memory_retirements(id,project_id,semantic_key,source_revision,text_hash,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,semantic_key,source_revision,text_hash) DO NOTHING`, NewID("ret"), old.ProjectID, old.SemanticKey, old.SourceRevision, old.TextHash, Now()); e != nil {
		return protocol.ProjectMemory{}, e
	}
	if e = tx.Commit(); e != nil {
		return protocol.ProjectMemory{}, e
	}
	return m, nil
}
func memoryTextHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
