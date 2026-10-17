package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileReadEvidence(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("verified content\n", 5000)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, sink := WithRawSink(WithScope(t.Context(), &Scope{Root: root}))
	result, err := (&fileRead{}).Call(ctx, json.RawMessage(`{"path":"large.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(body))
	if len(result) >= len(body) || sink.FileRead == nil || sink.FileRead.SHA256 != hex.EncodeToString(hash[:]) || sink.FileRead.Body != body {
		t.Fatalf("clipped read lost authoritative evidence: bytes=%d evidence=%+v", len(result), sink.FileRead)
	}
	raw, err := json.Marshal(sink.FileRead)
	if err != nil || strings.Contains(string(raw), "verified content") {
		t.Fatal("private read body leaked into metadata", err)
	}
	ctx, sink = WithRawSink(WithScope(t.Context(), &Scope{Root: root}))
	if _, err := (&fileRead{}).Call(ctx, json.RawMessage(`{"path":"../forbidden"}`)); err == nil || sink.FileRead != nil {
		t.Fatal("denied read produced verification evidence")
	}
}
