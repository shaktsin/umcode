package engine

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/vault"
	"github.com/shaktsin/umcode/internal/work"
)

func storageTestEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default(home)
	st, err := store.Open(t.Context(), filepath.Join(home, "storage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	v := &vault.Vault{Dir: cfg.Storage.VaultDir}
	return &Engine{Cfg: cfg, Store: st, Work: &work.Service{Store: st, Vault: v}}, st
}

func TestStorageRetentionValidation(t *testing.T) {
	e, _ := storageTestEngine(t)
	want := protocol.RetentionDays{Raw: 2, Stale: 3, Blob: 4, Redacted: 5}
	if _, err := e.SetStorageRetention(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []protocol.RetentionDays{
		{}, {Raw: 1, Stale: 1, Blob: 1}, {Raw: 0, Stale: 1, Blob: 1, Redacted: 1},
		{Raw: -1, Stale: 1, Blob: 1, Redacted: 1}, {Raw: 3651, Stale: 1, Blob: 1, Redacted: 1},
	} {
		if _, err := e.SetStorageRetention(t.Context(), invalid); err == nil {
			t.Fatalf("accepted invalid retention %+v", invalid)
		}
		got, err := e.StorageSettings(t.Context())
		if err != nil || got.Retention != want {
			t.Fatalf("invalid update changed saved retention: %+v err=%v", got, err)
		}
	}
	var fractional protocol.RetentionDays
	if err := json.Unmarshal([]byte(`{"raw":1.5,"stale":2,"blob":3,"redacted":4}`), &fractional); err == nil {
		t.Fatal("fractional retention JSON was accepted")
	}
}

func TestStorageRetentionPersistence(t *testing.T) {
	e, st := storageTestEngine(t)
	want := protocol.RetentionDays{Raw: 9, Stale: 8, Blob: 7, Redacted: 6}
	got, err := e.StorageSettings(t.Context())
	if err != nil || got.Retention != (protocol.RetentionDays{Raw: 30, Stale: 30, Blob: 90, Redacted: 7}) || got.Source != "default" {
		t.Fatalf("default=%+v err=%v", got, err)
	}
	if _, err := e.SetStorageRetention(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(t.Context(), filepath.Join(e.Cfg.Home, "storage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	e.Store, e.Work.Store = st2, st2
	got, err = e.StorageSettings(t.Context())
	if err != nil || got.Retention != want || got.Source != "stored" {
		t.Fatalf("reopened=%+v err=%v", got, err)
	}
}

func TestStorageCollectorPolicySnapshot(t *testing.T) {
	e, _ := storageTestEngine(t)
	initial := protocol.RetentionDays{Raw: 10, Stale: 11, Blob: 12, Redacted: 13}
	updated := protocol.RetentionDays{Raw: 1, Stale: 2, Blob: 3, Redacted: 4}
	if _, err := e.SetStorageRetention(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	if got, err := e.storageRetention(t.Context()); err != nil || got != work.RetentionFromDays(10, 11, 12, 13) {
		t.Fatalf("initial policy=%+v", got)
	}
	if _, err := e.SetStorageRetention(t.Context(), updated); err != nil {
		t.Fatal(err)
	}
	if got, err := e.storageRetention(t.Context()); err != nil || got != work.RetentionFromDays(1, 2, 3, 4) {
		t.Fatalf("updated policy did not apply without restart: %+v", got)
	}
	retention, err := e.storageRetention(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.Stats(t.Context(), e.Store, retention, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestStorageRetentionConfigPrecedence(t *testing.T) {
	e, _ := storageTestEngine(t)
	e.Cfg.Storage.RetentionRawDays = 5
	e.Cfg.Storage.RetentionBlobDays = 6
	got, err := e.StorageSettings(t.Context())
	want := protocol.RetentionDays{Raw: 5, Stale: 30, Blob: 6, Redacted: 7}
	if err != nil || got.Retention != want || got.Source != "config" {
		t.Fatalf("config fallback=%+v err=%v", got, err)
	}
	if _, err := e.SetStorageRetention(t.Context(), protocol.RetentionDays{Raw: 2, Stale: 2, Blob: 2, Redacted: 2}); err != nil {
		t.Fatal(err)
	}
	got, err = e.StorageSettings(t.Context())
	if err != nil || got.Retention.Raw != 2 || got.Source != "stored" {
		t.Fatalf("stored setting did not override config: %+v err=%v", got, err)
	}
}

func TestStorageCleanupReport(t *testing.T) {
	e, _ := storageTestEngine(t)
	if _, err := e.SetStorageRetention(t.Context(), protocol.RetentionDays{Raw: 1, Stale: 1, Blob: 1, Redacted: 1}); err != nil {
		t.Fatal(err)
	}
	o, err := e.Work.Vault.Put([]byte("expired storage"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := e.Store.UpsertVaultObject(t.Context(), protocol.VaultObjectRow{Hash: o.Hash, Class: o.Class, Size: o.Size, OriginalSize: o.OriginalSize, CreatedAt: old, LastReferencedAt: old}); err != nil {
		t.Fatal(err)
	}
	result, err := e.CleanupStorage(t.Context())
	if err != nil || result.Deleted != 1 || result.FreedBytes != int64(len("expired storage")) || result.Stats.Objects != 0 {
		t.Fatalf("cleanup=%+v err=%v", result, err)
	}
	result, err = e.CleanupStorage(t.Context())
	if err != nil || result.Deleted != 0 || result.FreedBytes != 0 || result.Stats.EligibleBytes != 0 {
		t.Fatalf("empty cleanup=%+v err=%v", result, err)
	}
}
