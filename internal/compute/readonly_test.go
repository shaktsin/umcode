package compute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeRequestReadOnlyRoot(t *testing.T) {
	root := t.TempDir()
	req, err := validateRequest(Request{Root: root, Dir: root, Command: "ls", RootReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !req.RootReadOnly {
		t.Fatalf("RootReadOnly was dropped: %#v", req)
	}
	// Mounts() is the spec the libkrun call is driven from (see
	// workspaceReadOnly); only a real VM run proves the guest enforces it.
	if !req.workspaceReadOnly() {
		t.Error("the value handed to libkrun must come from the workspace mount")
	}
	mounts := req.Mounts()
	var ws, scratch *GuestMount
	for i := range mounts {
		switch mounts[i].GuestPath {
		case "/workspace":
			ws = &mounts[i]
		case "/tmp":
			scratch = &mounts[i]
		}
	}
	if ws == nil || !ws.ReadOnly || ws.HostPath != req.Root {
		t.Errorf("workspace mount must be the root, read-only: %#v", mounts)
	}
	if scratch == nil || scratch.ReadOnly {
		t.Errorf("scratch mount must be writable: %#v", mounts)
	}
	rw, err := validateRequest(Request{Root: root, Dir: root, Command: "ls"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rw.Mounts() {
		if m.ReadOnly {
			t.Errorf("default request must stay read-write: %#v", m)
		}
	}
}

// Mounts() describes the one workspace share the guest gets. A single virtiofs share cannot express a read-only .git inside a writable
// root. Until a second share is added, the guest root is either wholly
// read-only or wholly writable.
func TestComputeCannotProtectDotGitInWritableRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	req, err := validateRequest(Request{Root: root, Dir: root, Command: "ls"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Mounts() {
		if m.GuestPath == "/workspace/.git" {
			t.Fatal("a .git overlay mount now exists; extend this test to assert it is read-only")
		}
	}
	t.Log("gap: no read-only /workspace/.git mount inside a writable root")
}

// TestComputeReadOnlyRootIntegration lives in readonly_integration_test.go
// (build tag libkrun): it needs a real guest.
