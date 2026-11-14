package workbench

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestDataScriptSandboxDenies runs a real script under the real OS sandbox and
// proves network and out-of-workspace reads fail while writing to out/ works.
// It is skipped where Python or a sandbox mechanism is unavailable.
func TestDataScriptSandboxDenies(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	if detectSandbox() == nil {
		t.Skip("no OS sandbox (Seatbelt or bubblewrap) is available")
	}
	root, id := dataFixture(t, "custom")
	script := `
import urllib.request
results = []
try:
    urllib.request.urlopen("http://example.com", timeout=3)
    results.append("network:allowed")
except Exception:
    results.append("network:denied")
try:
    open("/etc/hosts").read()
    results.append("read:allowed")
except Exception:
    results.append("read:denied")
open("out/ok.csv", "w").write("x\n1\n")
print(" ".join(results))
`
	req := scriptRequest(root, id, python)
	req.Script = script
	result, err := RunDataScript(context.Background(), req)
	if err != nil {
		t.Fatalf("script failed: %v\nstderr: %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "network:denied") {
		t.Fatalf("network was not denied: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "read:denied") {
		t.Fatalf("out-of-workspace read was not denied: %q", result.Stdout)
	}
	if result.Dataset == nil || result.Dataset.RowCount != 1 {
		t.Fatalf("out/ file was not registered: %+v", result.Dataset)
	}
}
