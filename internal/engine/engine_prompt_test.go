package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemPromptIgnoresGlobalAgentMD(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	if err := os.WriteFile(filepath.Join(e.Cfg.Home, "AGENT.md"), []byte("SECRET-GLOBAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := e.systemPrompt(t.Context(), "hello", nil, "", nil)
	if strings.Contains(prompt, "SECRET-GLOBAL") || strings.Contains(prompt, "User context (AGENT.md)") {
		t.Fatalf("global AGENT.md reached the prompt: %s", prompt)
	}
}
