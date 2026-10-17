package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestTaskLargeRawWriteSerialization(t *testing.T) {
	e, th, turn, ctx, root := expectationEngine(t, "mutation", "file_content")
	content := strings.Repeat("# Plan\n> instructions & notes\n<div>example</div>\n", 1500) + "计划\u2028separator\u2029end"
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]string{"path": "notes.txt", "content": content}); err != nil {
		t.Fatal(err)
	}
	raw := bytes.ReplaceAll(bytes.TrimSpace(buf.Bytes()), []byte(`\u2028`), []byte("\u2028"))
	raw = bytes.ReplaceAll(raw, []byte(`\u2029`), []byte("\u2029"))
	out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "raw-write", Name: "file__write", Args: raw}, nil)
	if out.IsError {
		t.Fatalf("real write rejected after item persistence: %s", out.Output)
	}
	body, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil || string(body) != content {
		t.Fatalf("written contents differ: %v", err)
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	op := snap.Operations[0]
	// Existing references hash the compact persisted form. Their digests must
	// remain readable; no live database rewrite is needed for this fix.
	stored, err := e.Store.GetItem(ctx, op.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	var legacy bytes.Buffer
	if err := json.Compact(&legacy, stored.Tool.Args); err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256(legacy.Bytes())
	legacyOp := op
	legacyOp.ArgsSHA256 = hex.EncodeToString(oldDigest[:])
	if _, err := e.operationArgs(ctx, legacyOp); err != nil {
		t.Fatal("existing persisted digest became unreadable", err)
	}
	args, err := e.operationArgs(ctx, op)
	if err != nil {
		t.Fatal("stored argument hash could not be verified", err)
	}
	var recovered map[string]string
	if err := json.Unmarshal(args, &recovered); err != nil || recovered["content"] != content {
		t.Fatal("referenced arguments changed", err)
	}
	expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"notes.txt"}`)
	if got := expectationResult(t, e, th, turn, ctx); got.Status != "satisfied" {
		t.Fatalf("real readback did not verify write: %+v", got)
	}
	// Corruption of the archived original must still fail the digest check.
	item, err := e.Store.GetItem(ctx, op.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	item.Tool.Args, _ = json.Marshal(map[string]string{"path": "notes.txt", "content": content + "tampered"})
	item.Status = protocol.ItemCompleted
	if err := e.Store.SaveItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := e.operationArgs(ctx, op); err == nil {
		t.Fatal("tampered archived arguments passed integrity verification")
	}
}
