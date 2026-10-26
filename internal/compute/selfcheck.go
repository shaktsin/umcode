package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// readOnlyProbe succeeds only if the workspace can be read and an attempt to
// create a file under it fails, with nothing left behind.
const readOnlyProbe = `cat a.txt >/dev/null && ! ( : > b.txt ) 2>/dev/null && [ ! -e b.txt ]`

// SelfCheckReadOnly proves that the guest really mounts the workspace
// read-only: it runs a probe in a throwaway root through r with RootReadOnly
// set, and the probe must read a file but fail to create one. tmp is where
// the throwaway root is made. The host also checks that no file appeared.
//
// It needs a real guest to mean anything. Tests drive it with a fake Runner;
// the bundled microVM path is unverified where no KVM or hypervisor exists.
func SelfCheckReadOnly(ctx context.Context, r Runner, tmp string) error {
	if r == nil {
		return errors.New("compute: no runner available for the read-only self-check")
	}
	root, err := os.MkdirTemp(tmp, "umcode-vm-ro-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("ok"), 0o644); err != nil {
		return err
	}
	code, err := r.Run(ctx, Request{Root: root, Dir: root, Command: readOnlyProbe, RootReadOnly: true, Timeout: 120, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return fmt.Errorf("microVM read-only self-check: %w", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "b.txt")); statErr == nil {
		return errors.New("microVM read-only self-check: a file was created in the read-only workspace")
	}
	if code != 0 {
		return fmt.Errorf("microVM read-only self-check: the guest did not enforce a read-only workspace (probe exit %d)", code)
	}
	return nil
}
