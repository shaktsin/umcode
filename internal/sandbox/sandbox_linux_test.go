//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, sb Sandbox, p Policy, dir, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	if err := sb.Wrap(cmd, p); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestBwrapConfinesWritesAndNetwork(t *testing.T) {
	sb := Detect()
	if sb == nil {
		t.Skip("bubblewrap is not usable here")
	}
	root := t.TempDir()
	outside, err := os.MkdirTemp(os.Getenv("HOME"), "sbx-outside-")
	if err != nil {
		outside, err = os.MkdirTemp("/var", "sbx-outside-")
		if err != nil {
			t.Skip("no writable location outside the sandbox policy")
		}
	}
	defer os.RemoveAll(outside)
	p := DefaultPolicy(root, false)

	if out, err := run(t, sb, p, root, "echo hi > inside.txt && cat inside.txt"); err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("write inside project failed: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "inside.txt")); err != nil {
		t.Fatal("file written inside the sandbox is not visible on the host")
	}
	if out, err := run(t, sb, p, root, "echo no > "+filepath.Join(outside, "x.txt")); err == nil {
		t.Fatalf("write outside the project succeeded: %s", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Fatal("file outside the project was created")
	}
	// A fresh network namespace has no interfaces other than loopback.
	if out, err := run(t, sb, p, root, "cat /proc/net/dev"); err != nil || strings.Contains(out, "eth") || strings.Contains(out, "ens") {
		t.Fatalf("network not isolated: %v\n%s", err, out)
	}
}
