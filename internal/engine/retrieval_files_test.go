//go:build darwin || linux

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/shaktsin/umcode/internal/retrieval"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func excerptFixture(t *testing.T) (string, retrieval.Candidate) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("func BuildPacket() {}\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), body, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	rh := sha256.Sum256([]byte(filepath.Clean(root)))
	return root, retrieval.Candidate{ID: "excerpt:one", Kind: "excerpt", Path: "main.go", Body: string(body), StartLine: 1, EndLine: 1, ContentHash: hex.EncodeToString(hash[:]), WorkspaceRootHash: hex.EncodeToString(rh[:])}
}
func TestRetrievalFileRevalidation(t *testing.T) {
	root, c := excerptFixture(t)
	got, err := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{c})
	if err != nil || len(got) != 1 {
		t.Fatalf("current excerpt=%+v %v", got, err)
	}
	c.WorkspaceRootHash = "foreign"
	if got, _ := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{c}); len(got) != 0 {
		t.Fatal("foreign workspace")
	}
	c.WorkspaceRootHash = retrieval.WorkspaceHash(root)
	for _, body := range []string{"changed", "binary\x00", strings.Repeat("x", 256*1024+1)} {
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		bad := c
		if body != "changed" {
			hash := sha256.Sum256([]byte(body))
			bad.ContentHash = hex.EncodeToString(hash[:])
		}
		if got, _ := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{bad}); len(got) != 0 {
			t.Fatal("changed or invalid source accepted")
		}
	}
	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	if got, _ := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{c}); len(got) != 0 {
		t.Fatal("missing source accepted")
	}
}
func TestRetrievalContainment(t *testing.T) {
	root, c := excerptFixture(t)
	for _, path := range []string{"../main.go", "/tmp/main.go", ".env", ".aws/config", "AGENTS.md", "nested/CLAUDE.md", "UMCODE.md", "id_rsa", "credentials.json", "cert.pem", "main.go/child"} {
		bad := c
		bad.Path = path
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "../") && path != "main.go/child" {
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(c.Body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if got, _ := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{bad}); len(got) != 0 {
			t.Fatalf("forbidden path %q", path)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(root, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, path := range []string{"directory", "socket"} {
		bad := c
		bad.Path = path
		if got, _ := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{bad}); len(got) != 0 {
			t.Fatal("special file read")
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "main.go"), []byte(c.Body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "main.go"), filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	if got, _ := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{c}); len(got) != 0 {
		t.Fatal("final symlink followed")
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	c.Path = "link/main.go"
	if got, _ := validateRetrievalFiles(t.Context(), root, []retrieval.Candidate{c}); len(got) != 0 {
		t.Fatal("intermediate symlink followed")
	}
}
func TestRetrievalFileLimits(t *testing.T) {
	root, c := excerptFixture(t)
	var cs []retrieval.Candidate
	for i := 0; i < 6; i++ {
		n := c
		n.ID = fmt.Sprint("excerpt:", i)
		n.Path = fmt.Sprint(i, ".go")
		if err := os.WriteFile(filepath.Join(root, n.Path), []byte(c.Body), 0600); err != nil {
			t.Fatal(err)
		}
		cs = append(cs, n)
	}
	got, err := validateRetrievalFiles(t.Context(), root, cs)
	if err != nil || len(got) != 4 {
		t.Fatalf("file ceiling=%d %v", len(got), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := validateRetrievalFiles(ctx, root, cs); err == nil {
		t.Fatal("cancellation ignored")
	}
}
