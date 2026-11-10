package compute

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGuest stands in for the microVM: it records the request and answers like
// a guest whose workspace mount is (or is not) really read-only.
type fakeGuest struct {
	enforce bool
	err     error
	req     Request
	sawFile bool
}

func (f *fakeGuest) Run(_ context.Context, r Request) (int, error) {
	f.req = r
	if f.err != nil {
		return -1, f.err
	}
	_, statErr := os.Stat(filepath.Join(r.Root, "a.txt"))
	f.sawFile = statErr == nil
	if f.enforce && r.RootReadOnly {
		return 0, nil // the probe's write failed inside the guest, as required
	}
	// A guest that let the write through: the file shows up on the host and
	// the probe command exits non-zero.
	_ = os.WriteFile(filepath.Join(r.Root, "b.txt"), []byte("x"), 0o644)
	return 1, nil
}

func TestSelfCheckReadOnlyPassesWhenGuestMountIsReadOnly(t *testing.T) {
	g := &fakeGuest{enforce: true}
	if err := SelfCheckReadOnly(t.Context(), g, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !g.req.RootReadOnly || !g.sawFile || g.req.Command == "" || g.req.Timeout <= 0 {
		t.Fatalf("probe request = %+v (file seen: %v)", g.req, g.sawFile)
	}
	if _, err := os.Stat(g.req.Root); !os.IsNotExist(err) {
		t.Fatalf("probe root not cleaned up: %v", err)
	}
}

func TestSelfCheckReadOnlyFailsWhenGuestWritesThrough(t *testing.T) {
	err := SelfCheckReadOnly(t.Context(), &fakeGuest{enforce: false}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("a guest that accepts writes must fail the self-check, got %v", err)
	}
}

func TestSelfCheckReadOnlyFailsWhenGuestCannotRun(t *testing.T) {
	err := SelfCheckReadOnly(t.Context(), &fakeGuest{err: errors.New("no kvm")}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no kvm") {
		t.Fatalf("run failure must surface: %v", err)
	}
	if err := SelfCheckReadOnly(t.Context(), nil, t.TempDir()); err == nil {
		t.Fatal("nil runner must fail")
	}
}
