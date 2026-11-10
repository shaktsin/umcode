package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/sandbox"
)

func TestDelegationShellBoundary(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	os.WriteFile(target, []byte("original"), 0600)
	// No sandbox: even auto_all and network enabled cannot authorize a child escape.
	shell := &shellRun{}
	for _, readonly := range []bool{false, true} {
		ctx := WithScope(context.Background(), &Scope{Root: root, ApprovalMode: "auto_all", AllowNet: true, RestrictedChild: true, ReadOnly: readonly})
		if _, err := shell.Call(ctx, json.RawMessage(`{"command":"echo changed > target"}`)); err == nil {
			t.Fatalf("unconfined child shell permitted, readonly=%t", readonly)
		}
	}
	data, _ := os.ReadFile(target)
	if string(data) != "original" {
		t.Fatal("child changed sentinel")
	}
}

// Rejecting the wrapper must not fall back to host execution. The captured
// policy tests the real shell adapter's confinement contract at its OS boundary.
type rejectingChildSandbox struct{ policy sandbox.Policy }

func (*rejectingChildSandbox) Name() string { return "unavailable" }
func (s *rejectingChildSandbox) Wrap(_ *exec.Cmd, p sandbox.Policy) error {
	s.policy = p
	return errors.New("sandbox unavailable")
}

func TestDelegationShellWrapperFailure(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	target := filepath.Join(parent, "target")
	os.WriteFile(target, []byte("parent"), 0600)
	sb := &rejectingChildSandbox{}
	sh := &shellRun{sandbox: sb}
	ctx := WithScope(t.Context(), &Scope{Root: root, RestrictedChild: true, ApprovalMode: "auto_all"})
	args, _ := json.Marshal(map[string]string{"command": "echo changed > " + shellQuote(target)})
	if _, err := sh.Call(ctx, args); err == nil {
		t.Fatal("wrapper failure allowed command")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "parent" {
		t.Fatal("failed sandbox fell back to host")
	}
	if !pathutil.SameFolder(sb.policy.Root, root) || sb.policy.Network || len(sb.policy.Writable) != 1 {
		t.Fatalf("child confinement lost %+v", sb.policy)
	}
	for _, path := range append(append([]string{}, sb.policy.Readable...), sb.policy.Writable...) {
		if pathutil.Within(path, parent) || pathutil.SameFolder(path, os.TempDir()) || pathutil.SameFolder(path, "/tmp") {
			t.Fatalf("child policy exposes parent/shared temp: %s", path)
		}
	}
	if _, err := os.Stat(sb.policy.Writable[0]); !os.IsNotExist(err) {
		t.Error("command-private temp not removed")
	}
}

// Direct built-in calls cannot bypass the reviewer engine allowlist.
func TestDelegationReviewerDirectMutationDenied(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	os.WriteFile(target, []byte("original"), 0600)
	ctx := WithScope(t.Context(), &Scope{Root: root, ReadOnly: true, RestrictedChild: true, ApprovalMode: "auto_all", AllowNet: true, UseCompute: true})
	for _, tool := range []Tool{&fileWrite{}, &fileEdit{}, &shellRun{}, &verificationRun{}, &verificationPlan{}, &execStart{}, &execWrite{}, &execStop{}} {
		if _, err := tool.Call(ctx, json.RawMessage(`{"path":"target","content":"changed","command":"echo changed > target"}`)); err == nil {
			t.Errorf("reviewer allowed direct %s", tool.Name())
		}
	}
	data, _ := os.ReadFile(target)
	if string(data) != "original" {
		t.Fatal("reviewer changed sentinel")
	}
}
