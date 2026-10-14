package tools

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/sandbox"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluationToolsConfined(t *testing.T) {
	if sandbox.Detect() == nil {
		t.Skip("OS sandbox unavailable; evaluation shell fails closed")
	}
	root := t.TempDir()
	source := t.TempDir()
	hidden := filepath.Join(source, "grader")
	os.WriteFile(hidden, []byte("secret"), 0600)
	cfg := config.Default(t.TempDir())
	r := NewRegistry()
	RegisterBuiltins(r, cfg, NewWorkspaces(cfg), nil)
	ConstrainEvaluation(r, root, source)
	shell, _ := r.Get("shell.run")
	ctx := WithScope(t.Context(), &Scope{Root: root})
	for _, command := range []string{"cat '" + hidden + "'", "echo bad > '" + hidden + "'"} {
		args, _ := json.Marshal(map[string]string{"command": command})
		out, err := shell.Call(ctx, args)
		if err == nil && !strings.Contains(out, "exit_code: 1") {
			t.Fatalf("escaped scope: %s %v", out, err)
		}
	}
	args, _ := json.Marshal(map[string]string{"command": "printf '%s' \"$HOME\""})
	out, err := shell.Call(ctx, args)
	if err != nil || !strings.Contains(out, ".evaluation-home") {
		t.Fatalf("real HOME leaked: %s %v", out, err)
	}
	web, _ := r.Get("web.fetch")
	if _, err := web.Call(ctx, json.RawMessage(`{"url":"https://example.com"}`)); err == nil {
		t.Fatal("host specialist dispatched")
	}
}
