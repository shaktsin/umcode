// Package teststore creates isolated, fully migrated SQLite stores for tests.
package teststore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/shaktsin/umcode/internal/store"
)

var prepared struct {
	sync.Once
	bytes []byte
	err   error
}

func preparedBytes() ([]byte, error) {
	prepared.Do(func() {
		dir, err := os.MkdirTemp("", "umcode-teststore-")
		if err != nil {
			prepared.err = err
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "seed.db")
		seed, err := store.Open(context.Background(), path)
		if err != nil {
			prepared.err = err
			return
		}
		if err := seed.Close(); err != nil {
			prepared.err = err
			return
		}
		prepared.bytes, prepared.err = os.ReadFile(path)
	})
	return prepared.bytes, prepared.err
}

// Open creates a new test database at path. It refuses to overwrite an
// existing file so tests that exercise reopening keep using store.Open.
func Open(ctx context.Context, path string) (*store.Store, error) {
	data, err := preparedBytes()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	n, writeErr := f.Write(data)
	if n != len(data) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		os.Remove(path)
		return nil, err
	}
	return store.Open(ctx, path)
}
