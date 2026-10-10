package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// WorkUpdateConflictError classifies stale or mismatched persistence predicates
// without exposing graph contents.
type WorkUpdateConflictError struct{}

func (*WorkUpdateConflictError) Error() string { return "work update conflict" }

// ErrWorkUpdateConflict supports errors.Is as well as typed errors.As checks.
var ErrWorkUpdateConflict = &WorkUpdateConflictError{}

// ApplyWorkUpdate rechecks the prepared graph's ownership and optimistic
// predicates under one writer lock, then commits the whole batch once.
func (s *Store) ApplyWorkUpdate(ctx context.Context, update protocol.PreparedWorkUpdate) (protocol.WorkUpdateResult, error) {
	var zero protocol.WorkUpdateResult
	// The SQLite driver maps serializable isolation to BEGIN IMMEDIATE. Acquire
	// the writer lock before reading, avoiding deferred read-to-write upgrades.
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	w, err := scanWork(tx.QueryRowContext(ctx, `SELECT `+workCols+` FROM works WHERE id = ?`, update.WorkID))
	if errors.Is(err, sql.ErrNoRows) {
		return zero, ErrWorkUpdateConflict
	}
	if err != nil {
		return zero, err
	}
	if w.Status != protocol.WorkOpen || update.ExpectedRevision < 1 || w.Revision != update.ExpectedRevision {
		return zero, ErrWorkUpdateConflict
	}
	depth := update.WorkflowDepth
	if depth == "" {
		depth = w.WorkflowDepth
	}
	if workDepthRank(depth) < 0 || workDepthRank(depth) < workDepthRank(w.WorkflowDepth) {
		return zero, ErrWorkUpdateConflict
	}
	created := make(map[string]bool, len(update.Creates))
	for _, n := range update.Creates {
		if n.WorkID != w.ID || n.ID == "" || created[n.ID] {
			return zero, ErrWorkUpdateConflict
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_nodes WHERE id = ?`, n.ID).Scan(&count); err != nil {
			return zero, err
		}
		if count != 0 {
			return zero, ErrWorkUpdateConflict
		}
		created[n.ID] = true
	}
	checkNode := func(id string, revision int, status string, predicate bool) error {
		if !predicate && created[id] {
			return nil
		}
		var workID, gotStatus string
		var gotRevision int
		err := tx.QueryRowContext(ctx, `SELECT work_id, revision, status FROM work_nodes WHERE id = ?`, id).Scan(&workID, &gotRevision, &gotStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkUpdateConflict
		}
		if err != nil {
			return err
		}
		if workID != w.ID || predicate && (revision < 1 || gotRevision != revision || gotStatus != status) {
			return ErrWorkUpdateConflict
		}
		return nil
	}
	for _, c := range update.NodeChecks {
		if err := checkNode(c.ID, c.ExpectedRevision, c.ExpectedStatus, true); err != nil {
			return zero, err
		}
	}
	for _, tr := range update.Transitions {
		if err := checkNode(tr.ID, tr.ExpectedRevision, tr.FromStatus, true); err != nil {
			return zero, err
		}
		if tr.SupersededBy != "" {
			if tr.SupersededBy == tr.ID || tr.ToStatus != protocol.StatusSuperseded {
				return zero, ErrWorkUpdateConflict
			}
			if err := checkNode(tr.SupersededBy, 0, "", false); err != nil {
				return zero, err
			}
		}
	}
	for _, n := range update.Creates {
		if n.SupersededBy != "" {
			if err := checkNode(n.SupersededBy, 0, "", false); err != nil {
				return zero, err
			}
		}
	}
	for _, e := range update.Edges {
		if e.WorkID != w.ID {
			return zero, ErrWorkUpdateConflict
		}
		for _, id := range []string{e.FromNodeID, e.ToNodeID} {
			if err := checkNode(id, 0, "", false); err != nil {
				return zero, err
			}
		}
	}
	for _, link := range update.EvidenceLinks {
		if err := checkNode(link.NodeID, 0, "", false); err != nil {
			return zero, err
		}
		var workID string
		err := tx.QueryRowContext(ctx, `SELECT work_id FROM evidence WHERE id = ?`, link.EvidenceID).Scan(&workID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && workID != w.ID {
			return zero, ErrWorkUpdateConflict
		}
		if err != nil {
			return zero, err
		}
	}
	// Every read predicate has passed before the first write.
	changed, err := tx.ExecContext(ctx, `UPDATE works SET revision = revision + 1, workflow_depth = ? WHERE id = ? AND revision = ? AND status = ?`, depth, w.ID, update.ExpectedRevision, protocol.WorkOpen)
	if err != nil {
		return zero, err
	}
	if err := requireWorkUpdateRow(changed); err != nil {
		return zero, err
	}
	for _, n := range update.Creates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_nodes (id, work_id, kind, title, content_json, status, confidence, revision,
			valid_from, valid_until, superseded_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.ID, w.ID, n.Kind, n.Title, string(n.Content), n.Status, n.Confidence, n.Revision,
			FormatTime(n.ValidFrom), nullTimePtr(n.ValidUntil), n.SupersededBy, FormatTime(n.CreatedAt), FormatTime(n.UpdatedAt)); err != nil {
			return zero, err
		}
	}
	at := FormatTime(time.Now().UTC())
	for _, tr := range update.Transitions {
		changed, err := tx.ExecContext(ctx, `UPDATE work_nodes SET status = ?, revision = revision + 1, updated_at = ?,
			valid_until=CASE WHEN ?<>'' THEN ? ELSE valid_until END,
			superseded_by=CASE WHEN ?<>'' THEN ? ELSE superseded_by END
			WHERE id = ? AND work_id = ? AND revision = ? AND status = ?`,
			tr.ToStatus, at, tr.SupersededBy, at, tr.SupersededBy, tr.SupersededBy, tr.ID, w.ID, tr.ExpectedRevision, tr.FromStatus)
		if err != nil {
			return zero, err
		}
		if err := requireWorkUpdateRow(changed); err != nil {
			return zero, err
		}
	}
	for _, e := range update.Edges {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_edges (work_id, from_node_id, relation, to_node_id) VALUES (?,?,?,?)`, w.ID, e.FromNodeID, e.Relation, e.ToNodeID); err != nil {
			return zero, err
		}
	}
	for _, link := range update.EvidenceLinks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_node_evidence (work_id, node_id, evidence_id) VALUES (?,?,?)`, w.ID, link.NodeID, link.EvidenceID); err != nil {
			return zero, err
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return protocol.WorkUpdateResult{Revision: w.Revision + 1, Created: len(update.Creates), Transitioned: len(update.Transitions), Linked: len(update.Edges) + len(update.EvidenceLinks)}, nil
}

func requireWorkUpdateRow(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrWorkUpdateConflict
	}
	return nil
}

func workDepthRank(depth string) int {
	switch depth {
	case protocol.DepthDirect:
		return 0
	case protocol.DepthGuided:
		return 1
	case protocol.DepthDesigned:
		return 2
	default:
		return -1
	}
}

func nullTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

const workCols = `id, thread_id, project_id, kind, status, workflow_depth, goal, created_at, completed_at, revision`

func scanWork(sc interface{ Scan(...any) error }) (protocol.Work, error) {
	var w protocol.Work
	var project, completed sql.NullString
	var created string
	if err := sc.Scan(&w.ID, &w.ThreadID, &project, &w.Kind, &w.Status, &w.WorkflowDepth, &w.Goal, &created, &completed, &w.Revision); err != nil {
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
	if w.Revision == 0 {
		w.Revision = 1
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO works (`+workCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		w.ID, w.ThreadID, nullIfEmpty(w.ProjectID), w.Kind, w.Status, w.WorkflowDepth, w.Goal,
		FormatTime(w.CreatedAt), nullTimePtr(w.CompletedAt), w.Revision)
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

// SetWorkDepth atomically raises a work's depth and revision. Repeats and
// downgrades are no-ops, including observations from the legacy workflow.
func (s *Store) SetWorkDepth(ctx context.Context, workID, depth string) error {
	rank := workDepthRank(depth)
	if rank < 0 {
		return errors.New("invalid work depth")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE works SET workflow_depth = ?, revision = revision + 1 WHERE id = ?
		AND CASE workflow_depth WHEN 'direct' THEN 0 WHEN 'guided' THEN 1 WHEN 'designed' THEN 2 ELSE -1 END < ?`, depth, workID, rank)
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
	return getWorkDetail(ctx, s.DB, workID)
}

type workQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getWorkDetail(ctx context.Context, q workQuerier, workID string) (protocol.WorkDetail, error) {
	d := protocol.WorkDetail{Nodes: []protocol.WorkNode{}, Edges: []protocol.WorkEdge{},
		Evidence: []protocol.Evidence{}, Attempts: []protocol.VerificationAttempt{}, Fingerprints: []protocol.Fingerprint{}}
	w, err := scanWork(q.QueryRowContext(ctx, `SELECT `+workCols+` FROM works WHERE id = ?`, workID))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Work = w

	rows, err := q.QueryContext(ctx, `SELECT id, work_id, kind, title, content_json, status, confidence, revision, valid_from,
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

	rows, err = q.QueryContext(ctx, `SELECT node_id, evidence_id FROM work_node_evidence
		WHERE work_id = ? ORDER BY node_id, evidence_id`, workID)
	if err != nil {
		return d, err
	}
	nodeIndex := make(map[string]int, len(d.Nodes))
	for i := range d.Nodes {
		nodeIndex[d.Nodes[i].ID] = i
	}
	for rows.Next() {
		var nodeID, evidenceID string
		if err := rows.Scan(&nodeID, &evidenceID); err != nil {
			rows.Close()
			return d, err
		}
		if i, ok := nodeIndex[nodeID]; ok {
			d.Nodes[i].EvidenceIDs = append(d.Nodes[i].EvidenceIDs, evidenceID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return d, err
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT work_id, from_node_id, relation, to_node_id FROM work_edges WHERE work_id = ? ORDER BY rowid`, workID)
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

	rows, err = q.QueryContext(ctx, `SELECT e.id, e.work_id, e.node_id, e.kind, e.source_uri, e.source_revision, e.content_hash,
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

	rows, err = q.QueryContext(ctx, `SELECT id, work_id, criterion_node_id, check_type, command, environment_json, status,
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

	rows, err = q.QueryContext(ctx, `SELECT id, work_id, turn_id, kind, value, paths_json, taken_at FROM work_fingerprints
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

// finalMemoryRevision selects the latest recorded workspace fingerprint; the
// canonical ordered snapshot breaks ties by insertion order.
func finalMemoryRevision(d protocol.WorkDetail) string {
	var final *protocol.Fingerprint
	for i := range d.Fingerprints {
		f := &d.Fingerprints[i]
		if f.Kind != protocol.FingerprintTurnEnd && f.Kind != protocol.FingerprintVerification {
			continue
		}
		if final == nil || !f.TakenAt.Before(final.TakenAt) {
			final = f
		}
	}
	if final == nil {
		return ""
	}
	return final.Value
}

// transitionMemoryCandidate permits post-completion curation without changing
// the completed Work revision or its verification graph. Source deletion leaves
// durable operation provenance intact and is harmless during failure handling.
func transitionMemoryCandidate(ctx context.Context, tx *sql.Tx, op protocol.MemoryPromotionOp, status string, at time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE work_nodes SET status=?,revision=revision+1,updated_at=? WHERE id=? AND work_id=? AND kind='memory_candidate' AND status<>? AND status IN ('pending','conflicted')`, status, FormatTime(at), op.CandidateNodeID, op.WorkID, status)
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
	var current string
	err = tx.QueryRowContext(ctx, `SELECT status FROM work_nodes WHERE id=? AND work_id=? AND kind='memory_candidate'`, op.CandidateNodeID, op.WorkID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if status == protocol.StatusPromoted {
			return ErrMemoryStale
		}
		return nil
	}
	if err != nil {
		return err
	}
	if current != status {
		return ErrMemoryStale
	}
	return nil
}
