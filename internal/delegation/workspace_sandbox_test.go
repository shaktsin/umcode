package delegation

import (
	"github.com/shaktsin/umcode/internal/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Actual Git commands in a private child checkout require no source repository
// metadata ACLs; a real local test command and output file share that policy.
func TestDelegationWorkspaceSandboxCommands(t *testing.T) {
	sb := sandbox.Detect()
	if sb == nil {
		t.Skip("enforceable child command sandbox unavailable")
	}
	svc, _, task, child := workspaceFixture(t)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "go.mod", "module workspacefixture\n\ngo 1.24\n")
	workspaceWrite(t, ws.Root, "fixture_test.go", "package workspacefixture\nimport \"testing\"\nfunc TestReal(t *testing.T) {}\n")
	workspaceWrite(t, ws.Root, ".gitignore", ".cache/\n")
	temporary := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `git status --porcelain && printf 'sandboxed\n' > command-result && git add command-result && git diff --cached --name-only && go test ./...`)
	cmd.Dir = ws.Root
	cmd.Env = append(os.Environ(), "TMPDIR="+temporary, "GOCACHE="+filepath.Join(ws.Root, ".cache", "go"), "GOMODCACHE="+filepath.Join(ws.Root, ".cache", "mod"), "GOTOOLCHAIN=local")
	if err = sb.Wrap(cmd, sandbox.IsolatedChildPolicy(ws.Root, false, temporary)); err != nil {
		t.Fatal(err)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actual sandbox Git/test commands: %v\n%s", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(ws.Root, "command-result")); err != nil || string(b) != "sandboxed\n" {
		t.Fatalf("command file %q %v", b, err)
	}
	sentinel := filepath.Join(task.ParentRoot, "sentinel")
	cmd = exec.CommandContext(t.Context(), "/bin/sh", "-c", `printf forbidden > "$1"`, "sh", sentinel)
	cmd.Dir = ws.Root
	if err = sb.Wrap(cmd, sandbox.IsolatedChildPolicy(ws.Root, false, temporary)); err != nil {
		t.Fatal(err)
	}
	if b, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("sandbox parent write succeeded: %s", b)
	}
	if _, err = os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("sandbox wrote to parent")
	}
}
