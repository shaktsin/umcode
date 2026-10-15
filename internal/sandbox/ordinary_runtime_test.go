package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runPolicyCommand(t *testing.T, sb Sandbox, p Policy, dir, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	if err := sb.Wrap(cmd, p); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Keep every home fixture synthetic: this test never reads actual user files.
func TestOrdinaryCommandHomeConfinement(t *testing.T) {
	sb := Detect()
	if sb == nil {
		t.Skip("native sandbox unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.MkdirTemp(home, "umcode-policy-test-")
	if err != nil {
		t.Skip("cannot create synthetic home outside shared temporary roots")
	}
	t.Cleanup(func() { os.RemoveAll(fixture) })
	t.Setenv("HOME", fixture)
	for _, rel := range []string{"project", "Documents", ".ssh", ".npm"} {
		if err := os.MkdirAll(filepath.Join(fixture, rel), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"project/input", "Documents/private", ".ssh/synthetic-key", ".npm/existing"} {
		if err := os.WriteFile(filepath.Join(fixture, rel), []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture, ".gitconfig"), []byte("[user]\n\tname = Sandbox Test\n\temail = test@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(fixture, "project")
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("all:\n\t@cat input > output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := DefaultPolicy(root, false)
	if out, err := runPolicyCommand(t, sb, p, root, "/usr/bin/git init -q && /usr/bin/git status --porcelain"); err != nil || !strings.Contains(out, "input") {
		t.Fatalf("project git workflow unavailable: %v %s", err, out)
	}
	if out, err := runPolicyCommand(t, sb, p, root, "/usr/bin/make && cat output"); err != nil || out != "synthetic" {
		t.Fatalf("project read/basic build tool unavailable: %v %s", err, out)
	}
	if goBin, err := exec.LookPath("go"); err == nil {
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() { println(\"built\") }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		script := `GOCACHE="$PWD/.go-cache" GOTOOLCHAIN=local GOPROXY=off CGO_ENABLED=0 ` + fmt.Sprintf("%q", goBin) + ` build -o hello main.go && ./hello`
		if out, err := runPolicyCommand(t, sb, p, root, script); err != nil || !strings.Contains(out, "built") {
			t.Fatalf("offline Go project build failed: %v %s", err, out)
		}
	}
	for _, script := range []string{
		`cat "$HOME/Documents/private"`,
		`ls "$HOME/Documents"`,
		`cat "$HOME/.ssh/synthetic-key"`,
		`echo denied > "$HOME/.npm/new"`,
	} {
		if out, err := runPolicyCommand(t, sb, p, root, script); err == nil {
			t.Errorf("restricted command succeeded: %s: %s", script, out)
		}
	}
	if out, err := runPolicyCommand(t, sb, p, root, `cat "$HOME/.npm/existing"`); err != nil || out != "synthetic" {
		t.Fatalf("offline dependency cache read failed: %v %s", err, out)
	}
}
