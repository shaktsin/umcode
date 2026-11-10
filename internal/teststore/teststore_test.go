package teststore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPreparedStoresAreIsolated(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first.db")
	first, err := Open(t.Context(), firstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.DB.Exec(`CREATE TABLE fixture_only (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	second, err := Open(t.Context(), filepath.Join(t.TempDir(), "second.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var count int
	if err := second.DB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("prepared migrations missing: count=%d err=%v", count, err)
	}
	if err := second.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='fixture_only'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("store state leaked between fixtures: count=%d err=%v", count, err)
	}
	if _, err := Open(t.Context(), firstPath); !os.IsExist(err) {
		t.Fatalf("existing database was not protected: %v", err)
	}
}
