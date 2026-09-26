//go:build darwin

package sandbox

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runSandboxed(t *testing.T, sb Sandbox, p Policy, dir, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	if err := sb.Wrap(cmd, p); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSeatbeltConfinesWritesReadsAndNetwork(t *testing.T) {
	sb := Detect()
	if sb == nil {
		t.Skip("sandbox-exec is not available")
	}
	root := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	outside, err := os.MkdirTemp(home, "umcode-sandbox-outside-")
	if err != nil {
		t.Skip("cannot create a directory outside the policy")
	}
	defer os.RemoveAll(outside)

	p := DefaultPolicy(root, false)
	if out, err := runSandboxed(t, sb, p, root, "echo hi > inside.txt && cat inside.txt"); err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("write inside project failed: %v %s", err, out)
	}
	if out, err := runSandboxed(t, sb, p, root, "echo no > "+filepath.Join(outside, "x.txt")); err == nil {
		t.Fatalf("write outside the project succeeded: %s", out)
	}

	// Network: a loopback HTTP server is reachable only when the policy allows it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "pong") })}
	go srv.Serve(ln)
	defer srv.Close()
	curl := "/usr/bin/curl -s --max-time 3 http://" + ln.Addr().String() + "/"
	if out, err := runSandboxed(t, sb, DefaultPolicy(root, false), root, curl); err == nil && strings.Contains(out, "pong") {
		t.Fatalf("network reachable with network off: %s", out)
	}
	if out, err := runSandboxed(t, sb, DefaultPolicy(root, true), root, curl); err != nil || !strings.Contains(out, "pong") {
		t.Fatalf("network unreachable with network on: %v %s", err, out)
	}

	// Credential folders cannot be read.
	ssh := filepath.Join(home, ".ssh")
	if _, err := os.Stat(ssh); err == nil {
		if out, err := runSandboxed(t, sb, p, root, "ls "+ssh); err == nil {
			t.Fatalf("~/.ssh readable in sandbox: %s", out)
		}
	}
}
