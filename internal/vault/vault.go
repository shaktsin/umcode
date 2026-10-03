// Package vault is a content-addressed store for large immutable evidence.
// Objects are redacted before they are hashed or written, so a secret that
// appeared in tool output never reaches disk.
package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const (
	ClassPlain    = "plain"
	ClassRedacted = "redacted"

	defaultMaxObjectBytes = 8 << 20
	truncationMarker      = "\n[... output truncated ...]\n"
)

var (
	// ErrMissing means the object file does not exist.
	ErrMissing = errors.New("vault object is missing")
	// ErrCorrupt means the object file does not hash to its name.
	ErrCorrupt = errors.New("vault object is corrupt")
)

// Object describes a stored object.
type Object struct {
	Hash         string
	Size         int64 // bytes stored
	OriginalSize int64 // bytes before redaction and clipping
	Class        string
	Truncated    bool
}

// Vault stores objects under <Dir>/objects/<first 2 hex>/<sha256 hex>.
type Vault struct {
	Dir            string
	MaxObjectBytes int64 // 0 means 8 MB
}

func (v *Vault) limit() int {
	if v.MaxObjectBytes > 0 {
		return int(v.MaxObjectBytes)
	}
	return defaultMaxObjectBytes
}

func (v *Vault) path(hash string) string {
	return filepath.Join(v.Dir, "objects", hash[:2], hash)
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// Put redacts, clips, hashes and stores data. Storing the same content twice
// writes one file.
func (v *Vault) Put(data []byte) (Object, error) {
	obj := Object{OriginalSize: int64(len(data)), Class: ClassPlain}
	redacted, changed := Redact(data)
	if changed {
		obj.Class = ClassRedacted
	}
	if limit := v.limit(); len(redacted) > limit {
		redacted = clipHeadTail(redacted, limit)
		obj.Truncated = true
	}
	sum := sha256.Sum256(redacted)
	obj.Hash = hex.EncodeToString(sum[:])
	obj.Size = int64(len(redacted))

	final := v.path(obj.Hash)
	if _, err := os.Stat(final); err == nil {
		return obj, nil
	}
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Object{}, err
	}
	tmp, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return Object{}, err
	}
	cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
	if _, err := tmp.Write(redacted); err != nil {
		cleanup()
		return Object{}, err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return Object{}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return Object{}, err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		os.Remove(tmp.Name())
		return Object{}, err
	}
	return obj, nil
}

// clipHeadTail keeps the start and the end (build output carries its verdict
// at the tail) with a marker between, never splitting a UTF-8 sequence.
func clipHeadTail(b []byte, limit int) []byte {
	headN := limit * 65 / 100
	tailN := limit - headN
	head := b[:headN]
	for !utf8.Valid(head) && len(head) > 0 {
		head = head[:len(head)-1]
	}
	tail := b[len(b)-tailN:]
	for len(tail) > 0 && !utf8.Valid(tail) {
		tail = tail[1:]
	}
	var out bytes.Buffer
	out.Write(head)
	out.WriteString(truncationMarker)
	out.Write(tail)
	return out.Bytes()
}

// Get returns an object's bytes after re-hashing them.
func (v *Vault) Get(hash string) ([]byte, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("%w: bad hash", ErrMissing)
	}
	data, err := os.ReadFile(v.path(hash))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, ErrCorrupt
	}
	return data, nil
}

// Has reports whether an object file exists (without verifying it).
func (v *Vault) Has(hash string) bool {
	if !validHash(hash) {
		return false
	}
	_, err := os.Stat(v.path(hash))
	return err == nil
}

// Delete removes an object file; a missing file is not an error.
func (v *Vault) Delete(hash string) error {
	if !validHash(hash) {
		return nil
	}
	if err := os.Remove(v.path(hash)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Walk calls fn with the hash of every object file on disk. Leftover temp
// files from interrupted writes are ignored.
func (v *Vault) Walk(fn func(hash string) error) error {
	root := filepath.Join(v.Dir, "objects")
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !validHash(d.Name()) {
			return nil
		}
		return fn(d.Name())
	})
}
