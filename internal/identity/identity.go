// Package identity detects sign-in state owned by supported model runtimes.
// It deliberately reads status only: OAuth credentials remain in the official
// Codex and Claude Code stores and are never copied into umcode.
package identity

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// List returns the managed model identities available on this machine.
func List(ctx context.Context) []protocol.ProviderIdentity {
	type result struct {
		index int
		value protocol.ProviderIdentity
	}
	checks := []func(context.Context) protocol.ProviderIdentity{inspectCodex, inspectClaude}
	out := make([]protocol.ProviderIdentity, len(checks))
	results := make(chan result, len(checks))
	for i, check := range checks {
		go func() {
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			results <- result{i, check(cctx)}
		}()
	}
	for range checks {
		r := <-results
		out[r.index] = r.value
	}
	return out
}

func inspectCodex(ctx context.Context) protocol.ProviderIdentity {
	id := protocol.ProviderIdentity{
		ID: "chatgpt", DisplayName: "ChatGPT", RuntimeName: "Codex",
		SignInCommand: "codex login --device-auth", SignOutCommand: "codex logout",
	}
	path := CodexPath()
	if path == "" {
		id.Status = "Codex is not installed"
		return id
	}
	id.Installed = true
	out, err := exec.CommandContext(ctx, path, "login", "status").CombinedOutput()
	status := strings.TrimSpace(string(out))
	if err != nil {
		id.Status = compactStatus(status, "Not signed in")
		return id
	}
	lower := strings.ToLower(status)
	if strings.Contains(lower, "chatgpt") {
		id.SignedIn = true
		id.AccountType = "ChatGPT subscription"
		id.Status = "Signed in through Codex"
		return id
	}
	if strings.Contains(lower, "api key") {
		id.AccountType = "API key"
		id.Status = "Codex is using an API key, not a ChatGPT subscription"
		return id
	}
	id.Status = compactStatus(status, "Not signed in")
	return id
}

func inspectClaude(ctx context.Context) protocol.ProviderIdentity {
	id := protocol.ProviderIdentity{
		ID: "claude_subscription", DisplayName: "Claude", RuntimeName: "Claude Code",
		SignInCommand: "claude auth login --claudeai", SignOutCommand: "claude auth logout",
	}
	path := ClaudePath()
	if path == "" {
		id.Status = "Claude Code is not installed"
		return id
	}
	id.Installed = true
	out, err := exec.CommandContext(ctx, path, "auth", "status", "--json").CombinedOutput()
	if err != nil {
		id.Status = "Not signed in"
		return id
	}
	var status struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal(out, &status) != nil || !status.LoggedIn {
		id.Status = "Not signed in"
		return id
	}
	if status.AuthMethod == "claude.ai" {
		id.SignedIn = true
		id.AccountType = "Claude subscription"
		id.Status = "Signed in through Claude Code"
		return id
	}
	id.AccountType = status.AuthMethod
	id.Status = "Claude Code is signed in, but not with a Claude subscription"
	return id
}

func findExecutable(name string, candidates ...string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return path
		}
	}
	return ""
}

func macPath(path string) string {
	if runtime.GOOS == "darwin" {
		return path
	}
	return ""
}

func compactStatus(status, fallback string) string {
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "WARNING:") {
			return line
		}
	}
	return fallback
}

// CodexPath returns the Codex executable, or "" when it is not installed.
func CodexPath() string {
	return findExecutable("codex", macPath("/Applications/ChatGPT.app/Contents/Resources/codex"))
}

// ClaudePath returns the Claude Code executable, or "" when it is not installed.
func ClaudePath() string {
	return findExecutable("claude", macPath("/opt/homebrew/bin/claude"), macPath("/usr/local/bin/claude"))
}
