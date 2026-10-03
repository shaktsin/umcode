package work

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/vault"
)

var gcNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

type gcEnv struct {
	st *store.Store
	v  *vault.Vault
}

func newGC(t *testing.T) gcEnv {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return gcEnv{st: st, v: &vault.Vault{Dir: filepath.Join(t.TempDir(), "vault")}}
}

// object stores content in the vault and indexes it as last referenced ageDays ago.
func (g gcEnv) object(t *testing.T, content string, ageDays int) string {
	t.Helper()
	o, err := g.v.Put([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	at := gcNow.Add(-time.Duration(ageDays) * 24 * time.Hour)
	if err := g.st.UpsertVaultObject(context.Background(), protocol.VaultObjectRow{Hash: o.Hash, Class: o.Class, Size: o.Size,
		OriginalSize: o.OriginalSize, CreatedAt: at, LastReferencedAt: at}); err != nil {
		t.Fatal(err)
	}
	return o.Hash
}

// reference attaches evidence for hash to a new work with the given status and staleness.
func (g gcEnv) reference(t *testing.T, hash, status string, stale bool) {
	t.Helper()
	ctx := context.Background()
	th, err := g.st.CreateThread(ctx, protocol.Thread{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := g.st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "g", CreatedAt: gcNow})
	if err != nil {
		t.Fatal(err)
	}
	e := protocol.Evidence{WorkID: w.ID, Kind: protocol.EvidenceVerificationOutput, VaultHash: hash, ObservedAt: gcNow}
	if stale {
		e.StaleAt = &gcNow
	}
	if _, err := g.st.AddEvidence(ctx, e); err != nil {
		t.Fatal(err)
	}
	if status != protocol.WorkOpen {
		if err := g.st.CloseWork(ctx, w.ID, status, gcNow); err != nil {
			t.Fatal(err)
		}
	}
}

func (g gcEnv) gc(t *testing.T) GCReport {
	t.Helper()
	r, err := RunGC(context.Background(), g.st, g.v, DefaultRetention(), gcNow)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGCDeletesUnreferencedPastRetention(t *testing.T) {
	g := newGC(t)
	h := g.object(t, "old output", 40)
	r := g.gc(t)
	if g.v.Has(h) || r.Deleted != 1 || r.FreedBytes != int64(len("old output")) {
		t.Fatalf("report %+v has=%v", r, g.v.Has(h))
	}
	if rows, _ := g.st.ListVaultObjects(context.Background()); len(rows) != 0 {
		t.Fatalf("rows remain: %v", rows)
	}
}

func TestGCKeepsReferenced(t *testing.T) {
	g := newGC(t)
	open := g.object(t, "open work output", 400)
	g.reference(t, open, protocol.WorkOpen, true)
	active := g.object(t, "active output", 400)
	g.reference(t, active, protocol.WorkCompleted, false)
	g.gc(t)
	if !g.v.Has(open) || !g.v.Has(active) {
		t.Fatal("referenced object deleted")
	}
}

func TestGCDeletesObjectsOnlyStaleEvidenceReferences(t *testing.T) {
	g := newGC(t)
	h := g.object(t, "stale only", 40)
	g.reference(t, h, protocol.WorkCompleted, true)
	g.gc(t)
	if g.v.Has(h) {
		t.Fatal("object referenced only by stale evidence of a closed work should expire")
	}
}

func TestGCKeepsWithinRetention(t *testing.T) {
	g := newGC(t)
	young := g.object(t, "young", 5)
	secret, err := g.v.Put([]byte("TOKEN=abcdef123456\n"))
	if err != nil || secret.Class != vault.ClassRedacted {
		t.Fatalf("put: %v %+v", err, secret)
	}
	at := gcNow.Add(-8 * 24 * time.Hour)
	g.st.UpsertVaultObject(context.Background(), protocol.VaultObjectRow{Hash: secret.Hash, Class: secret.Class, Size: secret.Size, OriginalSize: secret.OriginalSize, CreatedAt: at, LastReferencedAt: at})
	g.gc(t)
	if !g.v.Has(young) {
		t.Fatal("object within retention deleted")
	}
	if g.v.Has(secret.Hash) {
		t.Fatal("redacted object past 7 days should be deleted")
	}
}

func TestGCAdoptsOrphanFile(t *testing.T) {
	g := newGC(t)
	o, _ := g.v.Put([]byte("written, then crashed before the index row"))
	r := g.gc(t)
	rows, _ := g.st.ListVaultObjects(context.Background())
	if r.Adopted != 1 || len(rows) != 1 || rows[0].Hash != o.Hash || !g.v.Has(o.Hash) {
		t.Fatalf("report %+v rows %v", r, rows)
	}
}

func TestGCMarksMissing(t *testing.T) {
	g := newGC(t)
	h := g.object(t, "will vanish", 1)
	g.reference(t, h, protocol.WorkOpen, false)
	if err := g.v.Delete(h); err != nil {
		t.Fatal(err)
	}
	r := g.gc(t)
	if r.MarkedMissing != 1 {
		t.Fatalf("report %+v", r)
	}
	ctx := context.Background()
	th, _, _ := g.st.ListThreads(ctx, protocol.ThreadListParams{})
	works, _ := g.st.ListWorks(ctx, th[0].ID)
	d, _ := g.st.GetWorkDetail(ctx, works[0].ID)
	if d.Evidence[0].Availability != protocol.AvailUnavailable {
		t.Fatalf("availability = %s", d.Evidence[0].Availability)
	}
}

func TestGCCrashMidRunThenResume(t *testing.T) {
	g := newGC(t)
	gone := g.object(t, "expired", 60)
	kept := g.object(t, "kept", 60)
	g.reference(t, kept, protocol.WorkOpen, false)
	gcAfterFileDelete = func() error { return errors.New("crash") }
	_, err := RunGC(context.Background(), g.st, g.v, DefaultRetention(), gcNow)
	gcAfterFileDelete = nil
	if err == nil {
		t.Fatal("expected the injected crash")
	}
	if g.v.Has(gone) {
		t.Fatal("file should already be deleted at the crash point")
	}
	r := g.gc(t)
	rows, _ := g.st.ListVaultObjects(context.Background())
	if !g.v.Has(kept) || len(rows) != 1 || rows[0].Hash != kept || r.Deleted != 0 {
		t.Fatalf("after resume: report %+v rows %v", r, rows)
	}
}

func TestGCPropertyNeverDeletesReferenced(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	g := newGC(t) // one store for all rounds: opening a store dominates the cost
	for i := 0; i < 200; i++ {
		n := 1 + rng.Intn(6)
		for j := 0; j < n; j++ {
			h := g.object(t, fmt.Sprintf("obj-%d-%d", i, j), rng.Intn(200))
			switch rng.Intn(4) {
			case 0:
				g.reference(t, h, protocol.WorkOpen, rng.Intn(2) == 0)
			case 1:
				g.reference(t, h, protocol.WorkCompleted, false)
			case 2:
				g.reference(t, h, protocol.WorkCompleted, true)
			}
		}
		g.gc(t)
		refs, err := g.st.ReferencedVaultHashes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for h := range refs {
			if !g.v.Has(h) {
				t.Fatalf("iteration %d: referenced object %s deleted", i, h)
			}
		}
	}
}

func TestGCHonorsContextDeadline(t *testing.T) {
	g := newGC(t)
	h := g.object(t, "expired", 90)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	RunGC(ctx, g.st, g.v, DefaultRetention(), gcNow)
	if !g.v.Has(h) || time.Since(start) > time.Second {
		t.Fatal("expired context must stop GC without deleting")
	}
}

func TestGCNeverTouchesProjectFiles(t *testing.T) {
	g := newGC(t)
	project := t.TempDir()
	keep := filepath.Join(project, "main.go")
	os.WriteFile(keep, []byte("package main"), 0o600)
	g.object(t, "expired", 90)
	g.gc(t)
	if _, err := os.Stat(keep); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionDefaultsFromConfig(t *testing.T) {
	d := DefaultRetention()
	day := 24 * time.Hour
	if d.Raw != 30*day || d.Stale != 30*day || d.Blob != 90*day || d.Redacted != 7*day {
		t.Fatalf("defaults = %+v", d)
	}
	if got := RetentionFromDays(0, 0, 0, 0); got != d {
		t.Fatalf("zero config = %+v", got)
	}
	if got := RetentionFromDays(1, 2, 3, 4); got != (Retention{Raw: day, Stale: 2 * day, Blob: 3 * day, Redacted: 4 * day}) {
		t.Fatalf("explicit = %+v", got)
	}
}
