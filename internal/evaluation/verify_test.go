package evaluation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func verifierTask(t *testing.T) (Task, string) {
	t.Helper()
	root := t.TempDir()
	trusted := filepath.Join(root, "trusted")
	os.Mkdir(trusted, 0700)
	os.WriteFile(filepath.Join(trusted, "verify.sh"), []byte("[ \"$(cat notes.txt)\" = verified ]\n"), 0600)
	h, _ := TreeHash(trusted)
	return Task{ID: "test", Artifacts: []string{"notes.txt"}, TrustedRoot: trusted, TrustedHash: h, Checks: []Check{{ID: "notes", Argv: []string{"/bin/sh", "verify.sh"}, Seconds: 2}}}, t.TempDir()
}
func TestVerifierTrustBoundary(t *testing.T) {
	task, artifacts := verifierTask(t)
	os.WriteFile(filepath.Join(artifacts, "notes.txt"), []byte("verified"), 0600)
	os.WriteFile(filepath.Join(artifacts, "verify.sh"), []byte("exit 0"), 0600)
	checks, err := Verify(t.Context(), task, artifacts, filepath.Join(t.TempDir(), "verifier"))
	if err != nil || checks[0].Status != "passed" {
		t.Fatal(checks, err)
	}
	os.Remove(filepath.Join(artifacts, "notes.txt"))
	os.Symlink(filepath.Join(task.TrustedRoot, "verify.sh"), filepath.Join(artifacts, "notes.txt"))
	if _, err := Verify(t.Context(), task, artifacts, filepath.Join(t.TempDir(), "verifier")); err == nil {
		t.Fatal("grader symlink accepted")
	}
	os.WriteFile(filepath.Join(task.TrustedRoot, "verify.sh"), []byte("exit 0"), 0600)
	if _, err := Verify(t.Context(), task, artifacts, filepath.Join(t.TempDir(), "verifier")); err == nil {
		t.Fatal("tampered trusted grader")
	}
}
func TestVerifierFailureModes(t *testing.T) {
	task, artifacts := verifierTask(t)
	os.WriteFile(filepath.Join(artifacts, "notes.txt"), []byte("wrong"), 0600)
	r, err := Verify(t.Context(), task, artifacts, filepath.Join(t.TempDir(), "verifier"))
	if err != nil || r[0].Status != "failed" {
		t.Fatal(r, err)
	}
	task.Checks[0].Argv = []string{"missing-verifier-command"}
	r, err = Verify(t.Context(), task, artifacts, filepath.Join(t.TempDir(), "verifier"))
	if err != nil || r[0].Status != "incomplete" {
		t.Fatal("missing tool treated as pass", r, err)
	}
	task.Checks[0].Argv = []string{"/bin/sh", "-c", "sleep 20 & wait"}
	task.Checks[0].Seconds = 1
	r, err = Verify(t.Context(), task, artifacts, filepath.Join(t.TempDir(), "verifier"))
	if err != nil || r[0].Status != "incomplete" || !strings.Contains(r[0].Output, "deadline") {
		t.Fatal("timeout", r, err)
	}
}
