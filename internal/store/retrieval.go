package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"github.com/shaktsin/umcode/internal/vault"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func retrievalText(text string) string { b, _ := vault.Redact([]byte(text)); return string(b) }
func retrievalHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func indexRetrievalDocument(ctx context.Context, x execer, kind, id, workID, text string) error {
	if len(text) > retrieval.MaxBodyBytes || !utf8.ValidString(text) {
		_, err := x.ExecContext(ctx, `DELETE FROM retrieval_documents WHERE source_kind=? AND source_id=?`, kind, id)
		return err
	}
	body := retrievalText(text)
	_, err := x.ExecContext(ctx, `INSERT INTO retrieval_documents(id,source_kind,source_id,work_id,source_hash,body) VALUES(?,?,?,?,?,?) ON CONFLICT(source_kind,source_id) DO UPDATE SET source_hash=excluded.source_hash,body=excluded.body`, kind+":"+id, kind, id, workID, retrievalHash(body), body)
	return err
}

// backfillRetrieval runs inside migration17's transaction. Batches are bounded
// before allocation and redaction precedes every persisted document.
func backfillRetrieval(ctx context.Context, tx *sql.Tx) error {
	for _, source := range []struct{ table, kind, text string }{{"work_nodes", "node", "title"}, {"evidence", "evidence", "summary"}} {
		last := ""
		for {
			rows, err := tx.QueryContext(ctx, `SELECT id,work_id,`+source.text+` FROM `+source.table+` WHERE id>? AND length(CAST(`+source.text+` AS BLOB))<=4096 ORDER BY id LIMIT 64`, last)
			if err != nil {
				return err
			}
			type doc struct{ id, workID, text string }
			var batch []doc
			for rows.Next() {
				var d doc
				if err := rows.Scan(&d.id, &d.workID, &d.text); err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, d)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				break
			}
			for _, d := range batch {
				if err := indexRetrievalDocument(ctx, tx, source.kind, d.id, d.workID, d.text); err != nil {
					return err
				}
				last = d.id
			}
		}
	}
	return nil
}

// SearchRetrieval never calls the global transcript search. Scope predicates
// precede rank/LIMIT, and index bodies are checked against canonical text.
func (s *Store) SearchRetrieval(ctx context.Context, scope retrieval.Scope, q retrieval.Query) ([]retrieval.Candidate, error) {
	if scope.ThreadID == "" || scope.WorkID == "" {
		return nil, errors.New("invalid retrieval scope")
	}
	var valid int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM works w JOIN threads t ON t.id=w.thread_id WHERE w.id=? AND w.thread_id=? AND coalesce(w.project_id,'')=? AND coalesce(t.project_id,'')=? AND w.status='open'`, scope.WorkID, scope.ThreadID, scope.ProjectID, scope.ProjectID).Scan(&valid)
	if err != nil {
		return nil, err
	}
	if valid != 1 {
		return nil, errors.New("invalid retrieval scope")
	}
	if q.FTS == "" {
		return nil, nil
	}
	if len(q.FTS) > 2048 {
		return nil, errors.New("retrieval query oversized")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT d.source_kind,d.source_id,d.source_hash,
 CASE d.source_kind WHEN 'node' THEN n.title WHEN 'evidence' THEN e.summary ELSE x.text END,
 CASE d.source_kind WHEN 'node' THEN n.kind ELSE d.source_kind END,
 CASE d.source_kind WHEN 'node' THEN CAST(n.revision AS TEXT) WHEN 'evidence' THEN e.source_revision ELSE '' END,
 coalesce(x.path,''),coalesce(x.workspace_root_hash,''),coalesce(x.content_hash,''),coalesce(x.start_line,0),coalesce(x.end_line,0),bm25(retrieval_fts)
 FROM retrieval_fts JOIN retrieval_documents d ON d.rowid=retrieval_fts.rowid
 JOIN works w ON w.id=d.work_id
 LEFT JOIN work_nodes n ON d.source_kind='node' AND n.id=d.source_id AND n.work_id=w.id
 LEFT JOIN evidence e ON d.source_kind='evidence' AND e.id=d.source_id AND e.work_id=w.id
 LEFT JOIN observed_excerpts x ON d.source_kind='excerpt' AND x.id=d.source_id AND x.work_id=w.id
 LEFT JOIN evidence xe ON xe.id=x.evidence_id AND xe.work_id=w.id
 WHERE retrieval_fts MATCH ? AND w.id=? AND w.thread_id=? AND coalesce(w.project_id,'')=? AND (
 (d.source_kind='node' AND (n.kind IN ('requirement','artifact') OR (n.kind='decision' AND n.status='approved')) AND n.status NOT IN ('rejected','superseded') AND n.valid_until IS NULL AND n.superseded_by='' AND length(CAST(n.title AS BLOB))<=4096)
 OR (d.source_kind='evidence' AND e.stale_at IS NULL AND length(CAST(e.summary AS BLOB))<=4096)
 OR (d.source_kind='excerpt' AND xe.stale_at IS NULL AND xe.id IS NOT NULL AND length(CAST(x.text AS BLOB))<=4096))
 ORDER BY bm25(retrieval_fts),d.id LIMIT 64`, q.FTS, scope.WorkID, scope.ThreadID, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	var out []retrieval.Candidate
	total := 0
	for rows.Next() {
		var c retrieval.Candidate
		var kind, id, hash, canonical string
		if err := rows.Scan(&kind, &id, &hash, &canonical, &c.Kind, &c.SourceRevision, &c.Path, &c.WorkspaceRootHash, &c.ContentHash, &c.StartLine, &c.EndLine, &c.LexicalScore); err != nil {
			rows.Close()
			return nil, err
		}
		c.Body = retrievalText(canonical)
		if !utf8.ValidString(c.Body) || retrievalHash(c.Body) != hash {
			continue
		}
		total += len(c.Body)
		if total > retrieval.MaxCandidateBytes {
			rows.Close()
			return nil, errors.New("retrieval candidates oversized")
		}
		c.ID = kind + ":" + id
		c.ThreadID = scope.ThreadID
		c.WorkID = scope.WorkID
		c.ProjectID = scope.ProjectID
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT i.id,i.text,bm25(items_fts) FROM items_fts f JOIN items i ON i.id=f.item_id JOIN threads t ON t.id=i.thread_id WHERE items_fts MATCH ? AND i.thread_id=? AND f.thread_id=i.thread_id AND coalesce(t.project_id,'')=? AND i.turn_id<>? AND i.kind IN ('userMessage','agentMessage','inboundEvent') AND i.status='completed' AND length(CAST(i.text AS BLOB))<=4096 ORDER BY bm25(items_fts),i.id LIMIT 64`, q.FTS, scope.ThreadID, scope.ProjectID, scope.TurnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c retrieval.Candidate
		var id, text string
		if err := rows.Scan(&id, &text, &c.LexicalScore); err != nil {
			return nil, err
		}
		c.ID = "item:" + id
		c.Kind = "conversation"
		c.Body = retrievalText(text)
		c.ThreadID = scope.ThreadID
		c.ProjectID = scope.ProjectID
		c.Historical = true
		total += len(c.Body)
		if total > retrieval.MaxCandidateBytes {
			return nil, errors.New("retrieval candidates oversized")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordDiscoveryObservation atomically persists only bounded typed provenance.
func (s *Store) RecordDiscoveryObservation(ctx context.Context, e protocol.Evidence, excerpts []protocol.ObservedExcerpt) (protocol.Evidence, error) {
	if len(excerpts) > retrieval.MaxCandidates {
		return protocol.Evidence{}, errors.New("too many observed excerpts")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Evidence{}, err
	}
	defer tx.Rollback()
	e, err = insertEvidence(ctx, tx, e)
	if err != nil {
		return protocol.Evidence{}, err
	}
	for _, x := range excerpts {
		if x.WorkID != "" && x.WorkID != e.WorkID || x.EvidenceID != "" && x.EvidenceID != e.ID || filepath.IsAbs(x.Path) || strings.Contains(x.Path, "\\") || x.Path == "" || x.Path == "." || strings.Contains("/"+x.Path+"/", "/../") || len(x.Text) > retrieval.MaxBodyBytes || !utf8.ValidString(x.Text) || x.StartLine < 1 || x.EndLine < x.StartLine || x.EndLine-x.StartLine+1 > retrieval.MaxLines || len(x.ContentHash) != 64 || len(x.WorkspaceRootHash) != 64 {
			return protocol.Evidence{}, errors.New("invalid observed excerpt")
		}
		if _, err := hex.DecodeString(x.ContentHash); err != nil {
			return protocol.Evidence{}, errors.New("invalid excerpt hash")
		}
		if _, err := hex.DecodeString(x.WorkspaceRootHash); err != nil {
			return protocol.Evidence{}, errors.New("invalid workspace hash")
		}
		if x.ID == "" {
			x.ID = NewID("rex")
		}
		x.Text = retrievalText(x.Text)
		_, err = tx.ExecContext(ctx, `INSERT INTO observed_excerpts(id,work_id,evidence_id,path,workspace_root_hash,content_hash,start_line,end_line,text,observed_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, x.ID, e.WorkID, e.ID, x.Path, x.WorkspaceRootHash, x.ContentHash, x.StartLine, x.EndLine, x.Text, FormatTime(e.ObservedAt))
		if err != nil {
			return protocol.Evidence{}, err
		}
		if err := indexRetrievalDocument(ctx, tx, "excerpt", x.ID, e.WorkID, x.Text); err != nil {
			return protocol.Evidence{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return protocol.Evidence{}, fmt.Errorf("record discovery: %w", err)
	}
	return e, nil
}
