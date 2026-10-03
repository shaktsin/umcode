package work

import (
	"context"
	"errors"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/vault"
)

// Retention is how long an unreferenced vault object is kept, by kind.
type Retention struct{ Raw, Stale, Blob, Redacted time.Duration }

const day = 24 * time.Hour

// blobBytes is the size from which an object counts as a large blob.
const blobBytes = 1 << 20

// DefaultRetention returns the defaults used when nothing is configured.
func DefaultRetention() Retention {
	return Retention{Raw: 30 * day, Stale: 30 * day, Blob: 90 * day, Redacted: 7 * day}
}

// RetentionFromDays builds a Retention from configured day counts; zero (or
// negative) keeps the default.
func RetentionFromDays(raw, stale, blob, redacted int) Retention {
	r := DefaultRetention()
	set := func(dst *time.Duration, days int) {
		if days > 0 {
			*dst = time.Duration(days) * day
		}
	}
	set(&r.Raw, raw)
	set(&r.Stale, stale)
	set(&r.Blob, blob)
	set(&r.Redacted, redacted)
	return r
}

// forObject picks the retention for an object: redacted output expires
// fastest, large blobs last longest, and other output follows the shorter of
// the raw and stale periods.
func (r Retention) forObject(o protocol.VaultObjectRow) time.Duration {
	switch {
	case o.Class == vault.ClassRedacted:
		return r.Redacted
	case o.Size >= blobBytes:
		return r.Blob
	case r.Raw < r.Stale:
		return r.Raw
	}
	return r.Stale
}

// GCReport summarizes one RunGC pass.
type GCReport struct {
	Adopted, MarkedMissing, Deleted int
	FreedBytes                      int64
}

// gcAfterFileDelete is a test hook: an error returned here simulates a crash
// after an object file is removed and before its index row is.
var gcAfterFileDelete func() error

// RunGC reconciles the vault with its index and deletes expired, unreferenced
// objects. It only touches vault files and index rows, never project files. It
// is safe to interrupt: the next run finishes whatever was left.
func RunGC(ctx context.Context, st *store.Store, v *vault.Vault, ret Retention, now time.Time) (GCReport, error) {
	var rep GCReport
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	rows, err := st.ListVaultObjects(ctx)
	if err != nil {
		return rep, err
	}
	indexed := map[string]protocol.VaultObjectRow{}
	for _, r := range rows {
		indexed[r.Hash] = r
	}
	// Reconcile: adopt files without a row.
	err = v.Walk(func(hash string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := indexed[hash]; ok {
			return nil
		}
		data, err := v.Get(hash)
		if err != nil {
			return nil // unreadable or corrupt: leave it for a later run
		}
		row := protocol.VaultObjectRow{Hash: hash, Class: vault.ClassPlain, Status: "available", Size: int64(len(data)),
			OriginalSize: int64(len(data)), CreatedAt: now, LastReferencedAt: now}
		if err := st.UpsertVaultObject(ctx, row); err != nil {
			return err
		}
		indexed[hash] = row
		rep.Adopted++
		return nil
	})
	if err != nil {
		return rep, err
	}
	// Reconcile: rows without a file become missing; rows whose file is back become available.
	for _, r := range indexed {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		switch has := v.Has(r.Hash); {
		case !has && r.Status != "missing":
			if err := st.SetVaultObjectStatus(ctx, r.Hash, "missing"); err != nil {
				return rep, err
			}
			r.Status = "missing"
			indexed[r.Hash] = r
			rep.MarkedMissing++
		case has && r.Status == "missing":
			if err := st.SetVaultObjectStatus(ctx, r.Hash, "available"); err != nil {
				return rep, err
			}
			r.Status = "available"
			indexed[r.Hash] = r
		}
	}
	// Collect.
	refs, err := st.ReferencedVaultHashes(ctx)
	if err != nil {
		return rep, err
	}
	for hash, r := range indexed {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if refs[hash] {
			continue
		}
		if r.Status == "missing" && !v.Has(hash) {
			// Nothing left on disk: drop the row once it has also expired.
			if now.Sub(r.LastReferencedAt) > ret.forObject(r) {
				if err := st.DeleteVaultObject(ctx, hash); err != nil {
					return rep, err
				}
			}
			continue
		}
		if now.Sub(r.LastReferencedAt) <= ret.forObject(r) {
			continue
		}
		if err := v.Delete(hash); err != nil {
			return rep, err
		}
		if gcAfterFileDelete != nil {
			if err := gcAfterFileDelete(); err != nil {
				return rep, err
			}
		}
		if err := st.DeleteVaultObject(ctx, hash); err != nil {
			return rep, err
		}
		rep.Deleted++
		rep.FreedBytes += r.Size
	}
	return rep, nil
}

// StartGC runs RunGC once and then daily until ctx is done. It never blocks
// the caller and never prompts; failures are logged and counted.
func (s *Service) StartGC(ctx context.Context, ret Retention) {
	if s.Vault == nil {
		return
	}
	run := func() {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if _, err := RunGC(c, s.Store, s.Vault, ret, s.now()); err != nil && !errors.Is(err, context.Canceled) {
			s.count("gc", err)
		}
	}
	go func() {
		run()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}

// Stats summarizes the vault index: object count, bytes, and bytes GC could
// reclaim right now (unreferenced and past retention).
func Stats(ctx context.Context, st *store.Store, ret Retention, now time.Time) (protocol.VaultStats, error) {
	rows, err := st.ListVaultObjects(ctx)
	if err != nil {
		return protocol.VaultStats{}, err
	}
	refs, err := st.ReferencedVaultHashes(ctx)
	if err != nil {
		return protocol.VaultStats{}, err
	}
	var out protocol.VaultStats
	for _, r := range rows {
		if r.Status == "missing" {
			continue
		}
		out.Objects++
		out.Bytes += r.Size
		if !refs[r.Hash] && now.Sub(r.LastReferencedAt) > ret.forObject(r) {
			out.EligibleBytes += r.Size
		}
	}
	return out, nil
}
