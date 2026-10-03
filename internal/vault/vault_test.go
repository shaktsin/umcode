package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func objectFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(filepath.Join(dir, "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestPutDedupes(t *testing.T) {
	v := &Vault{Dir: t.TempDir()}
	a, err := v.Put([]byte("same output\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Put([]byte("same output\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash || len(objectFiles(t, v.Dir)) != 1 {
		t.Fatalf("hashes %s/%s, files %v", a.Hash, b.Hash, objectFiles(t, v.Dir))
	}
	want := filepath.Join(v.Dir, "objects", a.Hash[:2], a.Hash)
	if got := objectFiles(t, v.Dir)[0]; got != want {
		t.Fatalf("layout = %s, want %s", got, want)
	}
}

func TestGetVerifiesHash(t *testing.T) {
	v := &Vault{Dir: t.TempDir()}
	o, _ := v.Put([]byte("original"))
	path := filepath.Join(v.Dir, "objects", o.Hash[:2], o.Hash)
	if got, err := v.Get(o.Hash); err != nil || string(got) != "original" {
		t.Fatalf("get = %q, %v", got, err)
	}
	os.WriteFile(path, []byte("tampered"), 0o644)
	if got, err := v.Get(o.Hash); !errors.Is(err, ErrCorrupt) || got != nil {
		t.Fatalf("corrupt get = %q, %v", got, err)
	}
	if err := v.Delete(o.Hash); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get(o.Hash); !errors.Is(err, ErrMissing) || got != nil {
		t.Fatalf("missing get = %q, %v", got, err)
	}
	if v.Has(o.Hash) {
		t.Fatal("Has reports a deleted object")
	}
}

func TestRedactionTable(t *testing.T) {
	const keep = "go test ./... ok"
	cases := []struct{ name, secret, text string }{
		{"private key", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", "-----BEGIN RSA PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END RSA PRIVATE KEY-----"},
		{"openai style", "sk-abcdefghijklmnopqrstuvwxyz0123", "key sk-abcdefghijklmnopqrstuvwxyz0123 here"},
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "token ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"aws key", "AKIAABCDEFGHIJKLMNOP", "id AKIAABCDEFGHIJKLMNOP"},
		{"slack token", "xoxb-123456789012-abcdefghijkl", "slack xoxb-123456789012-abcdefghijkl"},
		{"bearer", "abc.def.ghi", "Authorization: Bearer abc.def.ghi"},
		{"env token", "hunter2hunter2", "API_TOKEN=hunter2hunter2"},
		{"password colon", "s3cretvalue", "password: s3cretvalue"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed := Redact([]byte(keep + "\n" + c.text + "\n" + keep))
			if !changed || strings.Contains(string(out), c.secret) {
				t.Fatalf("changed=%v out=%q", changed, out)
			}
			if !strings.HasPrefix(string(out), keep+"\n") || !strings.HasSuffix(string(out), "\n"+keep) {
				t.Fatalf("surrounding text altered: %q", out)
			}
		})
	}
	if out, changed := Redact([]byte(keep)); changed || string(out) != keep {
		t.Fatalf("clean text changed: %q %v", out, changed)
	}
}

func TestPutRedactsBeforeHash(t *testing.T) {
	v := &Vault{Dir: t.TempDir()}
	o, err := v.Put([]byte("run\nAPI_TOKEN=hunter2hunter2\ndone\n"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Class != ClassRedacted {
		t.Fatalf("class = %s", o.Class)
	}
	for _, f := range objectFiles(t, v.Dir) {
		data, _ := os.ReadFile(f)
		if bytes.Contains(data, []byte("hunter2hunter2")) {
			t.Fatalf("secret on disk in %s", f)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != o.Hash {
			t.Fatal("hash does not cover the redacted bytes")
		}
	}
	if plain, _ := v.Put([]byte("nothing secret")); plain.Class != ClassPlain {
		t.Fatalf("plain class = %s", plain.Class)
	}
}

func TestPutSizeCap(t *testing.T) {
	v := &Vault{Dir: t.TempDir(), MaxObjectBytes: 1000}
	in := append(bytes.Repeat([]byte("a"), 4900), []byte("THE VERDICT: FAIL")...)
	o, err := v.Put(in)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated || o.OriginalSize != int64(len(in)) {
		t.Fatalf("object = %+v", o)
	}
	got, err := v.Get(o.Hash)
	if err != nil || len(got) > 1200 || !bytes.HasSuffix(got, []byte("THE VERDICT: FAIL")) {
		t.Fatalf("stored %d bytes, err %v, tail %q", len(got), err, got[max(0, len(got)-20):])
	}
}

func TestPutAtomicOnReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	v := &Vault{Dir: filepath.Join(dir, "vault")}
	if _, err := v.Put([]byte("x")); err == nil {
		t.Fatal("expected an error")
	}
	if files := objectFiles(t, v.Dir); len(files) != 0 {
		t.Fatalf("left files: %v", files)
	}
}

func TestPutFailsWhenDirIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "vault")
	os.WriteFile(blocker, []byte("not a dir"), 0o644)
	if _, err := (&Vault{Dir: blocker}).Put([]byte("x")); err == nil {
		t.Fatal("expected an error when the vault path is a file")
	}
}

func TestWalkListsObjects(t *testing.T) {
	v := &Vault{Dir: t.TempDir()}
	a, _ := v.Put([]byte("one"))
	b, _ := v.Put([]byte("two"))
	got := map[string]bool{}
	if err := v.Walk(func(h string) error { got[h] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[a.Hash] || !got[b.Hash] {
		t.Fatalf("walk = %v", got)
	}
	empty := &Vault{Dir: filepath.Join(t.TempDir(), "nothing")}
	if err := empty.Walk(func(string) error { t.Fatal("unexpected"); return nil }); err != nil {
		t.Fatalf("walk of a missing vault dir = %v", err)
	}
}
