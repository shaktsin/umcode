package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/protocol"
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
