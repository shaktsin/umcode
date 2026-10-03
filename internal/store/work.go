package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func nullTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

const workCols = `id, thread_id, project_id, kind, status, workflow_depth, goal, created_at, completed_at`

func scanWork(sc interface{ Scan(...any) error }) (protocol.Work, error) {
	var w protocol.Work
	var project, completed sql.NullString
	var created string
	if err := sc.Scan(&w.ID, &w.ThreadID, &project, &w.Kind, &w.Status, &w.WorkflowDepth, &w.Goal, &created, &completed); err != nil {
		return w, err
	}
	w.ProjectID = project.String
	w.CreatedAt = ParseTime(created)
	w.CompletedAt = nullTime(completed)
	return w, nil
}

// CreateWork inserts a work, defaulting to an open, direct task.
func (s *Store) CreateWork(ctx context.Context, w protocol.Work) (protocol.Work, error) {
	if w.ID == "" {
		w.ID = NewID("wrk")
	}
	if w.Kind == "" {
		w.Kind = "task"
	}
	if w.Status == "" {
		w.Status = protocol.WorkOpen
	}
	if w.WorkflowDepth == "" {
		w.WorkflowDepth = protocol.DepthDirect
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now().UTC()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO works (`+workCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		w.ID, w.ThreadID, nullIfEmpty(w.ProjectID), w.Kind, w.Status, w.WorkflowDepth, w.Goal,
		FormatTime(w.CreatedAt), nullTimePtr(w.CompletedAt))
	return w, err
}

// OpenWorkForThread returns the thread's open work, if any.
func (s *Store) OpenWorkForThread(ctx context.Context, threadID string) (protocol.Work, bool, error) {
	w, err := scanWork(s.DB.QueryRowContext(ctx, `SELECT `+workCols+` FROM works
		WHERE thread_id = ? AND status = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, threadID, protocol.WorkOpen))
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Work{}, false, nil
	}
	return w, err == nil, err
}

// SetWorkDepth changes a work's workflow depth.
func (s *Store) SetWorkDepth(ctx context.Context, workID, depth string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE works SET workflow_depth = ? WHERE id = ?`, depth, workID)
	return err
}

// CloseWork sets a final status and completion time.
func (s *Store) CloseWork(ctx context.Context, workID, status string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE works SET status = ?, completed_at = ? WHERE id = ?`, status, FormatTime(at), workID)
	return err
}

// AbandonOpenWorks marks every open work of a thread abandoned.
func (s *Store) AbandonOpenWorks(ctx context.Context, threadID string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE works SET status = ?, completed_at = ? WHERE thread_id = ? AND status = ?`,
		protocol.WorkAbandoned, FormatTime(at), threadID, protocol.WorkOpen)
	return err
}

// AddWorkNode inserts a node.
func (s *Store) AddWorkNode(ctx context.Context, n protocol.WorkNode) (protocol.WorkNode, error) {
	if n.ID == "" {
		n.ID = NewID("wnd")
	}
	now := time.Now().UTC()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	if n.UpdatedAt.IsZero() {
		n.UpdatedAt = n.CreatedAt
	}
	if n.ValidFrom.IsZero() {
		n.ValidFrom = n.CreatedAt
	}
	if n.Revision == 0 {
		n.Revision = 1
	}
	if n.Confidence == 0 {
		n.Confidence = 1
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO work_nodes (id, work_id, kind, title, content_json, status, confidence, revision,
		valid_from, valid_until, superseded_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.WorkID, n.Kind, n.Title, string(n.Content), n.Status, n.Confidence, n.Revision,
		FormatTime(n.ValidFrom), nullTimePtr(n.ValidUntil), n.SupersededBy, FormatTime(n.CreatedAt), FormatTime(n.UpdatedAt))
	return n, err
}

// UpdateWorkNode changes a node's status and revision.
func (s *Store) UpdateWorkNode(ctx context.Context, id, status string, revision int, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE work_nodes SET status = ?, revision = ?, updated_at = ? WHERE id = ?`,
		status, revision, FormatTime(at), id)
	return err
}

// AddWorkEdge links two nodes; an identical edge is ignored.
func (s *Store) AddWorkEdge(ctx context.Context, e protocol.WorkEdge) error {
	_, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO work_edges (work_id, from_node_id, relation, to_node_id) VALUES (?,?,?,?)`,
		e.WorkID, e.FromNodeID, e.Relation, e.ToNodeID)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertEvidence(ctx context.Context, x execer, e protocol.Evidence) (protocol.Evidence, error) {
	if e.ID == "" {
		e.ID = NewID("evd")
	}
	if e.ObservedAt.IsZero() {
		e.ObservedAt = time.Now().UTC()
	}
	if e.Confidence == 0 {
		e.Confidence = 1
	}
	if e.Availability == "" {
		e.Availability = protocol.AvailNone
	}
	_, err := x.ExecContext(ctx, `INSERT INTO evidence (id, work_id, node_id, kind, source_uri, source_revision, content_hash,
		summary, confidence, observed_at, stale_at, vault_hash, env_fingerprint, availability) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.WorkID, nullIfEmpty(e.NodeID), e.Kind, e.SourceURI, e.SourceRevision, e.ContentHash, e.Summary,
		e.Confidence, FormatTime(e.ObservedAt), nullTimePtr(e.StaleAt), e.VaultHash, e.EnvFingerprint, e.Availability)
	return e, err
}

// AddEvidence inserts an immutable observation.
func (s *Store) AddEvidence(ctx context.Context, e protocol.Evidence) (protocol.Evidence, error) {
	return insertEvidence(ctx, s.DB, e)
}

func insertAttempt(ctx context.Context, x execer, a protocol.VerificationAttempt) (protocol.VerificationAttempt, error) {
	if a.ID == "" {
		a.ID = NewID("vat")
	}
	var exit any
	if a.ExitCode != nil {
		exit = *a.ExitCode
	}
	_, err := x.ExecContext(ctx, `INSERT INTO verification_attempts (id, work_id, criterion_node_id, check_type, command,
		environment_json, status, exit_code, evidence_id, started_at, finished_at, fingerprint_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.WorkID, nullIfEmpty(a.CriterionNodeID), a.CheckType, a.Command, string(a.Environment), a.Status, exit,
		nullIfEmpty(a.EvidenceID), FormatTime(a.StartedAt), FormatTime(a.FinishedAt), nullIfEmpty(a.FingerprintID))
	return a, err
}

// AddVerificationAttempt appends an attempt; attempts are never updated.
func (s *Store) AddVerificationAttempt(ctx context.Context, a protocol.VerificationAttempt) (protocol.VerificationAttempt, error) {
	return insertAttempt(ctx, s.DB, a)
}

// ListWorks returns a thread's works, newest first.
func (s *Store) ListWorks(ctx context.Context, threadID string) ([]protocol.Work, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE thread_id = ? ORDER BY created_at DESC, rowid DESC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Work{}
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWorkDetail loads a work with its nodes, edges, evidence and attempts.
func (s *Store) GetWorkDetail(ctx context.Context, workID string) (protocol.WorkDetail, error) {
	d := protocol.WorkDetail{Nodes: []protocol.WorkNode{}, Edges: []protocol.WorkEdge{},
		Evidence: []protocol.Evidence{}, Attempts: []protocol.VerificationAttempt{}, Fingerprints: []protocol.Fingerprint{}}
	w, err := scanWork(s.DB.QueryRowContext(ctx, `SELECT `+workCols+` FROM works WHERE id = ?`, workID))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Work = w

	rows, err := s.DB.QueryContext(ctx, `SELECT id, work_id, kind, title, content_json, status, confidence, revision, valid_from,
		valid_until, superseded_by, created_at, updated_at FROM work_nodes WHERE work_id = ? ORDER BY created_at, rowid`, workID)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var n protocol.WorkNode
		var content, validFrom, created, updated string
		var validUntil sql.NullString
		if err := rows.Scan(&n.ID, &n.WorkID, &n.Kind, &n.Title, &content, &n.Status, &n.Confidence, &n.Revision,
			&validFrom, &validUntil, &n.SupersededBy, &created, &updated); err != nil {
			rows.Close()
			return d, err
		}
		if content != "" {
			n.Content = json.RawMessage(content)
		}
		n.ValidFrom, n.ValidUntil = ParseTime(validFrom), nullTime(validUntil)
		n.CreatedAt, n.UpdatedAt = ParseTime(created), ParseTime(updated)
		d.Nodes = append(d.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return d, err
	}
	rows.Close()

	rows, err = s.DB.QueryContext(ctx, `SELECT work_id, from_node_id, relation, to_node_id FROM work_edges WHERE work_id = ? ORDER BY rowid`, workID)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var e protocol.WorkEdge
		if err := rows.Scan(&e.WorkID, &e.FromNodeID, &e.Relation, &e.ToNodeID); err != nil {
			rows.Close()
			return d, err
		}
		d.Edges = append(d.Edges, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return d, err
	}
	rows.Close()

	rows, err = s.DB.QueryContext(ctx, `SELECT e.id, e.work_id, e.node_id, e.kind, e.source_uri, e.source_revision, e.content_hash,
		e.summary, e.confidence, e.observed_at, e.stale_at, e.vault_hash, e.env_fingerprint,
		CASE WHEN e.vault_hash = '' THEN e.availability WHEN v.status = 'available' THEN 'available' ELSE 'unavailable' END
		FROM evidence e LEFT JOIN vault_objects v ON v.hash = e.vault_hash
		WHERE e.work_id = ? ORDER BY e.observed_at, e.rowid`, workID)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var e protocol.Evidence
		var node, stale sql.NullString
		var observed string
		if err := rows.Scan(&e.ID, &e.WorkID, &node, &e.Kind, &e.SourceURI, &e.SourceRevision, &e.ContentHash,
			&e.Summary, &e.Confidence, &observed, &stale, &e.VaultHash, &e.EnvFingerprint, &e.Availability); err != nil {
			rows.Close()
			return d, err
		}
		e.NodeID, e.ObservedAt, e.StaleAt = node.String, ParseTime(observed), nullTime(stale)
		d.Evidence = append(d.Evidence, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return d, err
	}
	rows.Close()

	rows, err = s.DB.QueryContext(ctx, `SELECT id, work_id, criterion_node_id, check_type, command, environment_json, status,
		exit_code, evidence_id, started_at, finished_at, fingerprint_id FROM verification_attempts WHERE work_id = ? ORDER BY started_at, rowid`, workID)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var a protocol.VerificationAttempt
		var crit, evid, fpid sql.NullString
		var exit sql.NullInt64
		var env, started, finished string
		if err := rows.Scan(&a.ID, &a.WorkID, &crit, &a.CheckType, &a.Command, &env, &a.Status, &exit, &evid, &started, &finished, &fpid); err != nil {
			rows.Close()
			return d, err
		}
		a.CriterionNodeID, a.EvidenceID, a.FingerprintID = crit.String, evid.String, fpid.String
		if env != "" {
			a.Environment = json.RawMessage(env)
		}
		if exit.Valid {
			code := int(exit.Int64)
			a.ExitCode = &code
		}
		a.StartedAt, a.FinishedAt = ParseTime(started), ParseTime(finished)
		d.Attempts = append(d.Attempts, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return d, err
	}
	rows.Close()

	rows, err = s.DB.QueryContext(ctx, `SELECT id, work_id, turn_id, kind, value, paths_json, taken_at FROM work_fingerprints
		WHERE work_id = ? ORDER BY taken_at, rowid`, workID)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var f protocol.Fingerprint
		var paths, taken string
		if err := rows.Scan(&f.ID, &f.WorkID, &f.TurnID, &f.Kind, &f.Value, &paths, &taken); err != nil {
			return d, err
		}
		f.Paths = []string{}
		_ = json.Unmarshal([]byte(paths), &f.Paths)
		f.TakenAt = ParseTime(taken)
		d.Fingerprints = append(d.Fingerprints, f)
	}
	return d, rows.Err()
}
