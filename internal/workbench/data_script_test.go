package workbench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/sandbox"
)

type passthroughSandbox struct{ policy *sandbox.Policy }

func (passthroughSandbox) Name() string { return "test" }
func (s passthroughSandbox) Wrap(_ *exec.Cmd, p sandbox.Policy) error {
	if s.policy != nil {
		*s.policy = p
	}
	return nil
}

func fakePython(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func withTestSandbox(t *testing.T) {
	t.Helper()
	previous := detectSandbox
	detectSandbox = func() sandbox.Sandbox { return passthroughSandbox{} }
	t.Cleanup(func() { detectSandbox = previous })
}

func scriptRequest(root, id, python string) DataScriptRequest {
	return DataScriptRequest{ProjectRoot: root, ModelID: id, Python: python, Title: "make data", OutputName: "made", Script: "print('hi')", ExpectOutput: "dataset"}
}

func TestRunDataScriptRefusesWithoutSandbox(t *testing.T) {
	root, id := dataFixture(t, "custom")
	previous := detectSandbox
	detectSandbox = func() sandbox.Sandbox { return nil }
	t.Cleanup(func() { detectSandbox = previous })
	_, err := RunDataScript(context.Background(), scriptRequest(root, id, "/usr/bin/true"))
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("error = %v, want sandbox refusal", err)
	}
}

func TestRunDataScriptDatasetRegistered(t *testing.T) {
	root, id := dataFixture(t, "custom")
	withTestSandbox(t)
	seed, err := ImportData(root, id, DataSource{Kind: "local", Name: "seed"}, []DataFile{upload("seed.csv", "x\n1\n")})
	if err != nil {
		t.Fatal(err)
	}
	python := fakePython(t, "ls inputs/seed/seed.csv >/dev/null || exit 3\nprintf 'x,y\\n1,2\\n3,4\\n' > out/made.csv\necho balanced")
	req := scriptRequest(root, id, python)
	req.InputDatasetIDs = []string{seed.ID}
	result, err := RunDataScript(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dataset == nil || result.Dataset.RowCount != 2 || result.Dataset.Source.Kind != "assistant" {
		t.Fatalf("dataset = %+v", result.Dataset)
	}
	prov := result.Dataset.Provenance
	if prov == nil || prov.CreatedBy != "assistant" || prov.Title != "make data" || len(prov.InputDatasetIDs) != 1 || !strings.Contains(prov.StdoutSummary, "balanced") {
		t.Fatalf("provenance = %+v", prov)
	}
	if !strings.Contains(result.Stdout, "balanced") {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func TestRunDataScriptEmptyOutFails(t *testing.T) {
	root, id := dataFixture(t, "custom")
	withTestSandbox(t)
	_, err := RunDataScript(context.Background(), scriptRequest(root, id, fakePython(t, "echo looked around")))
	if err == nil || !strings.Contains(err.Error(), "no output files") {
		t.Fatalf("error = %v, want no-output failure", err)
	}
}

func TestRunDataScriptReportSkipsImport(t *testing.T) {
	root, id := dataFixture(t, "custom")
	withTestSandbox(t)
	req := scriptRequest(root, id, fakePython(t, "printf 'x\\n1\\n' > out/extra.csv\necho report text"))
	req.ExpectOutput = "report"
	result, err := RunDataScript(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dataset != nil || !strings.Contains(result.Stdout, "report text") {
		t.Fatalf("result = %+v", result)
	}
	state, err := ReadData(root, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Datasets) != 0 {
		t.Fatalf("report run registered datasets: %+v", state.Datasets)
	}
}

func TestRunDataScriptTimeout(t *testing.T) {
	root, id := dataFixture(t, "custom")
	withTestSandbox(t)
	req := scriptRequest(root, id, fakePython(t, "echo starting\nsleep 10"))
	req.Timeout = time.Second
	_, err := RunDataScript(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "time") {
		t.Fatalf("error = %v, want timeout", err)
	}
}

func TestRunDataScriptOversizedOutput(t *testing.T) {
	root, id := dataFixture(t, "custom")
	withTestSandbox(t)
	python := fakePython(t, `awk 'BEGIN{print "x"; for(i=0;i<=100000;i++) print i}' > out/big.csv`)
	_, err := RunDataScript(context.Background(), scriptRequest(root, id, python))
	if err == nil || !strings.Contains(err.Error(), "rows") {
		t.Fatalf("error = %v, want row-bound failure", err)
	}
}

func TestRunDataScriptMissingInput(t *testing.T) {
	root, id := dataFixture(t, "custom")
	withTestSandbox(t)
	req := scriptRequest(root, id, fakePython(t, "printf 'x\\n1\\n' > out/made.csv"))
	req.InputDatasetIDs = []string{strings.Repeat("a", 64)}
	_, err := RunDataScript(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "not imported") {
		t.Fatalf("error = %v, want missing-input failure", err)
	}
}

func TestRunDataScriptSandboxPolicy(t *testing.T) {
	root, id := dataFixture(t, "custom")
	var captured sandbox.Policy
	previous := detectSandbox
	detectSandbox = func() sandbox.Sandbox { return passthroughSandbox{policy: &captured} }
	t.Cleanup(func() { detectSandbox = previous })
	base := t.TempDir()
	venvBin := filepath.Join(base, "venv", "bin")
	homeBin := filepath.Join(base, "realpython", "bin")
	for _, dir := range []string{venvBin, homeBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "venv", "pyvenv.cfg"), []byte("home = "+homeBin+"\nversion = 3.12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(venvBin, "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nprintf 'x\\n1\\n' > out/made.csv\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RunDataScript(context.Background(), scriptRequest(root, id, python)); err != nil {
		t.Fatal(err)
	}
	if captured.Network {
		t.Fatal("script sandbox allowed network access")
	}
	if captured.Root == "" || len(captured.Writable) != 0 {
		t.Fatalf("policy root %q writable %v: only the workspace may be writable", captured.Root, captured.Writable)
	}
	has := func(want string) bool {
		resolved, _ := filepath.EvalSymlinks(want)
		for _, readable := range captured.Readable {
			if readable == want || readable == resolved {
				return true
			}
		}
		return false
	}
	if !has(filepath.Join(base, "venv")) || !has(filepath.Join(base, "realpython")) {
		t.Fatalf("readable = %v, want the venv root and its base interpreter root", captured.Readable)
	}
	if !has(captured.Root) {
		t.Fatalf("readable = %v, want the workspace", captured.Readable)
	}
}

func TestCollectDataScriptOutputsRejectsHugeFileWithoutReading(t *testing.T) {
	out := t.TempDir()
	f, err := os.Create(filepath.Join(out, "big.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxDataBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := collectDataScriptOutputs(out); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want too-large error, got %v", err)
	}
}

func TestCollectDataScriptOutputsRejectsHardLinks(t *testing.T) {
	out := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.csv")
	if err := os.WriteFile(secret, []byte("a\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(out, "x.csv")); err != nil {
		t.Skip("hard links unsupported")
	}
	if _, err := collectDataScriptOutputs(out); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("want hard-link error, got %v", err)
	}
}

func TestCappedBufferKeepsMemoryBounded(t *testing.T) {
	b := &cappedBuffer{limit: 100}
	for i := 0; i < 1000; i++ {
		if n, err := b.Write([]byte(strings.Repeat("x", 100))); n != 100 || err != nil {
			t.Fatalf("write must report success so the script is not killed: %d %v", n, err)
		}
	}
	if b.Len() > 100 {
		t.Fatalf("buffer grew to %d", b.Len())
	}
}

func TestPythonReadRootsNeverIncludeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(home, "bin", "python3")
	if err := os.WriteFile(py, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, root := range pythonReadRoots(py) {
		if root == home || strings.HasPrefix(home, root+string(os.PathSeparator)) {
			t.Fatalf("root %s exposes the home folder", root)
		}
	}
}

func TestDataScriptEnvDropsSecrets(t *testing.T) {
	t.Setenv("HF_TOKEN", "hf_secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("PATH", "/usr/bin")
	env := strings.Join(dataScriptEnv(), "\n")
	if strings.Contains(env, "secret") || strings.Contains(env, "HF_TOKEN") {
		t.Fatalf("secrets leaked: %s", env)
	}
	if !strings.Contains(env, "PATH=/usr/bin") || !strings.Contains(env, "OMP_NUM_THREADS=1") {
		t.Fatalf("missing required vars: %s", env)
	}
}
