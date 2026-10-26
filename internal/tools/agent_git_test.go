package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A write-capable runtime agent's repository metadata stays read-only through
// the file tools too (the shell is confined by the sandbox policy).
func TestRuntimeAgentFileToolsCannotWriteGit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644)
	ctx := agentScope(root, func(s *Scope) { s.ReadOnly = false })
	w, e := &fileWrite{}, &fileEdit{}
	for _, path := range []string{".git/config", ".git/hooks/pre-commit", ".GIT/config", "./.git/x"} {
		if _, err := w.Call(ctx, json.RawMessage(`{"path":"`+path+`","content":"x"}`)); err == nil || !strings.Contains(err.Error(), ".git") {
			t.Fatalf("file.write %s: %v", path, err)
		}
	}
	if _, err := e.Call(ctx, json.RawMessage(`{"path":".git/config","old_string":"[core]","new_string":"[x]"}`)); err == nil {
		t.Fatal("file.edit wrote .git/config")
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".git", "config")); string(data) != "[core]\n" {
		t.Fatalf(".git/config changed: %q", data)
	}
	if _, err := w.Call(ctx, json.RawMessage(`{"path":"src/.gitignore","content":"x"}`)); err != nil {
		t.Fatalf("ordinary write refused: %v", err)
	}
	lead := WithScope(t.Context(), &Scope{Root: root, ProjectName: "demo"})
	if _, err := w.Call(lead, json.RawMessage(`{"path":".git/info/exclude","content":"x"}`)); err != nil {
		t.Fatalf("lead write changed behaviour: %v", err)
	}
}
