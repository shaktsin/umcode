//go:build libkrun

package compute

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestComputeReadOnlyRootIntegration runs a real microVM guest over a
// read-only workspace and checks, from inside the guest, that a write, a
// delete, an append and a read-write remount of /workspace all fail (and
// that writes still fail after the remount attempt), and, from the host, that
// the workspace is byte-identical afterwards (content digest, not a
// listing). Run it on a host with the libkrun bridge:
//
//	go test -tags libkrun ./internal/compute/ -run TestComputeReadOnlyRootIntegration -v
func TestComputeReadOnlyRootIntegration(t *testing.T) {
	if err := CheckHostAvailability(); err != nil {
		t.Skipf("no microVM on this host: %v", err)
	}
	root := t.TempDir()
	for name, body := range map[string]string{"a.txt": "alpha\n", "sub/b.txt": "beta\n", ".git/HEAD": "ref: refs/heads/main\n"} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before, err := TreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	// Each attempt that succeeds is named on stdout; a correct guest names none.
	const script = `cd /workspace || exit 97
cat a.txt sub/b.txt >/dev/null || exit 98
ok=""
( : > new.txt ) 2>/dev/null && ok="$ok write"
( rm -f a.txt ) 2>/dev/null && ok="$ok rm"
( rm -rf sub ) 2>/dev/null && ok="$ok rm-dir"
( echo x >> a.txt ) 2>/dev/null && ok="$ok append"
( echo x > .git/HEAD ) 2>/dev/null && ok="$ok git"
mount -o remount,rw /workspace 2>/dev/null && ok="$ok remount"
( : > after-remount.txt ) 2>/dev/null && ok="$ok write-after-remount"
( echo x >> a.txt ) 2>/dev/null && ok="$ok append-after-remount"
( rm -f sub/b.txt ) 2>/dev/null && ok="$ok rm-after-remount"
echo "succeeded:$ok"
`
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	code, err := NewBundledRunner().Run(ctx, Request{Root: root, Dir: root, Command: script, RootReadOnly: true, Timeout: 120, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("guest run: %v (stderr %q)", err, stderr.String())
	}
	if code != 0 {
		t.Fatalf("guest exit %d: stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if out != "succeeded:" {
		t.Errorf("the guest changed or could re-enable the read-only workspace: %q", out)
	}
	after, err := TreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("the host workspace is not byte-identical after the guest run")
	}
	// The production self-check agrees.
	if err := SelfCheckReadOnly(ctx, NewBundledRunner(), t.TempDir()); err != nil {
		t.Errorf("SelfCheckReadOnly on a real guest: %v", err)
	}
}
