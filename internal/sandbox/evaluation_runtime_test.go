package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEvaluationAllowsOwnedRuntime(t *testing.T) {
	sb := Detect()
	if sb == nil {
		t.Skip("OS confinement unavailable")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("verified"), 0600)
	cmd := exec.Command("/bin/sh", "-c", "cat notes.txt")
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
	if err := sb.Wrap(cmd, EvaluationPolicy(root, nil)); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "verified" {
		t.Fatal(string(out), err)
	}
}
