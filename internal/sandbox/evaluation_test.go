package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEvaluationConfinement(t *testing.T) {
	sb := Detect()
	if sb == nil {
		t.Skip("OS sandbox unavailable")
	}
	root := t.TempDir()
	hidden := filepath.Join(t.TempDir(), "grader")
	if err := os.WriteFile(hidden, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "cat '"+hidden+"'")
	cmd.Dir = root
	if err := sb.Wrap(cmd, EvaluationPolicy(root, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Fatal("evaluation command read outside fixture")
	}
}
