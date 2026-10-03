package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func upsertVaultObject(ctx context.Context, x execer, o protocol.VaultObjectRow) error {
	if o.Status == "" {
		o.Status = "available"
	}
	if o.Class == "" {
		o.Class = "plain"
	}
	now := time.Now().UTC()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	if o.LastReferencedAt.IsZero() {
		o.LastReferencedAt = o.CreatedAt
	}
	_, err := x.ExecContext(ctx, `INSERT INTO vault_objects (hash, size, original_size, class, truncated, status, created_at, last_referenced_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(hash) DO UPDATE SET status = excluded.status, last_referenced_at = excluded.last_referenced_at`,
		o.Hash, o.Size, o.OriginalSize, o.Class, b2i(o.Truncated), o.Status, FormatTime(o.CreatedAt), FormatTime(o.LastReferencedAt))
	return err
}

// UpsertVaultObject records a vault object; an existing row keeps its size and
// class and gets the new status and reference time.
func (s *Store) UpsertVaultObject(ctx context.Context, o protocol.VaultObjectRow) error {
	return upsertVaultObject(ctx, s.DB, o)
}

const vaultCols = `hash, size, original_size, class, truncated, status, created_at, last_referenced_at`

func scanVault(sc interface{ Scan(...any) error }) (protocol.VaultObjectRow, error) {
	var o protocol.VaultObjectRow
	var trunc int
	var created, ref string
	if err := sc.Scan(&o.Hash, &o.Size, &o.OriginalSize, &o.Class, &trunc, &o.Status, &created, &ref); err != nil {
		return o, err
	}
	o.Truncated = trunc != 0
	o.CreatedAt, o.LastReferencedAt = ParseTime(created), ParseTime(ref)
	return o, nil
}

// GetVaultObject returns one index row or ErrNotFound.
func (s *Store) GetVaultObject(ctx context.Context, hash string) (protocol.VaultObjectRow, error) {
	o, err := scanVault(s.DB.QueryRowContext(ctx, `SELECT `+vaultCols+` FROM vault_objects WHERE hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

// SetVaultObjectStatus changes an object's status (available or missing).
func (s *Store) SetVaultObjectStatus(ctx context.Context, hash, status string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE vault_objects SET status = ? WHERE hash = ?`, status, hash)
	return err
}

// TouchVaultObject records that an object is still referenced.
func (s *Store) TouchVaultObject(ctx context.Context, hash string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE vault_objects SET last_referenced_at = ? WHERE hash = ?`, FormatTime(at), hash)
	return err
}

// ListVaultObjects returns every index row.
func (s *Store) ListVaultObjects(ctx context.Context) ([]protocol.VaultObjectRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+vaultCols+` FROM vault_objects ORDER BY created_at, hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.VaultObjectRow{}
	for rows.Next() {
		o, err := scanVault(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteVaultObject removes an index row.
func (s *Store) DeleteVaultObject(ctx context.Context, hash string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM vault_objects WHERE hash = ?`, hash)
	return err
}

// AddFingerprint stores a workspace fingerprint.
func (s *Store) AddFingerprint(ctx context.Context, f protocol.Fingerprint) (protocol.Fingerprint, error) {
	return insertFingerprint(ctx, s.DB, f)
}

func insertFingerprint(ctx context.Context, x execer, f protocol.Fingerprint) (protocol.Fingerprint, error) {
	if f.ID == "" {
		f.ID = NewID("wfp")
	}
	if f.TakenAt.IsZero() {
		f.TakenAt = time.Now().UTC()
	}
	if f.Paths == nil {
		f.Paths = []string{}
	}
	paths, _ := json.Marshal(f.Paths)
	_, err := x.ExecContext(ctx, `INSERT INTO work_fingerprints (id, work_id, turn_id, kind, value, paths_json, taken_at) VALUES (?,?,?,?,?,?,?)`,
		f.ID, f.WorkID, f.TurnID, f.Kind, f.Value, string(paths), FormatTime(f.TakenAt))
	return f, err
}

// MarkEvidenceStale sets stale_at on the given evidence rows (those not already stale).
func (s *Store) MarkEvidenceStale(ctx context.Context, ids []string, at time.Time) error {
	for _, id := range ids {
		if _, err := s.DB.ExecContext(ctx, `UPDATE evidence SET stale_at = ? WHERE id = ? AND stale_at IS NULL`, FormatTime(at), id); err != nil {
			return err
		}
	}
	return nil
}

// ReferencedVaultHashes returns the vault hashes that GC must keep: evidence of
// works that are still open, and active (not stale) evidence of any work.
func (s *Store) ReferencedVaultHashes(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT e.vault_hash FROM evidence e JOIN works w ON w.id = e.work_id
		WHERE e.vault_hash != '' AND (w.status = 'open' OR e.stale_at IS NULL)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}

// CriterionUpdate changes a criterion node inside RecordAttempt's transaction.
type CriterionUpdate struct {
	NodeID   string
	Status   string
	Revision int
	At       time.Time
}

// RecordAttemptInput is everything that must commit together for one verification attempt.
type RecordAttemptInput struct {
	Attempt     protocol.VerificationAttempt
	Evidence    protocol.Evidence
	Object      *protocol.VaultObjectRow
	Fingerprint *protocol.Fingerprint
	Criterion   *CriterionUpdate
}

// RecordAttempt writes the vault row, fingerprint, evidence, attempt and criterion
// update in one transaction. Nothing is written when any part fails.
func (s *Store) RecordAttempt(ctx context.Context, in RecordAttemptInput) (protocol.VerificationAttempt, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.VerificationAttempt{}, err
	}
	defer tx.Rollback()
	if in.Object != nil {
		if err := upsertVaultObject(ctx, tx, *in.Object); err != nil {
			return protocol.VerificationAttempt{}, err
		}
	}
	a := in.Attempt
	if in.Fingerprint != nil {
		fp, err := insertFingerprint(ctx, tx, *in.Fingerprint)
		if err != nil {
			return protocol.VerificationAttempt{}, err
		}
		a.FingerprintID = fp.ID
	}
	ev, err := insertEvidence(ctx, tx, in.Evidence)
	if err != nil {
		return protocol.VerificationAttempt{}, err
	}
	a.EvidenceID = ev.ID
	if a, err = insertAttempt(ctx, tx, a); err != nil {
		return protocol.VerificationAttempt{}, err
	}
	if c := in.Criterion; c != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE work_nodes SET status = ?, revision = ?, updated_at = ? WHERE id = ?`,
			c.Status, c.Revision, FormatTime(c.At), c.NodeID); err != nil {
			return protocol.VerificationAttempt{}, err
		}
	}
	return a, tx.Commit()
}
