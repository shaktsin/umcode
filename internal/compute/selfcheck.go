package compute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// readOnlyProbe succeeds only if the workspace can be read and, after trying
// to undo a guest-side read-only flag (mount -o remount,rw; the guest runs as
// root, so a flag it can clear is no protection), every attempt to change it
// fails: create, delete and append. The host also compares the workspace's
// contents before and after, so a guest whose exit code lies is still caught.
const readOnlyProbe = `cat a.txt >/dev/null && ` +
	`{ mount -o remount,rw /workspace 2>/dev/null; true; } && ` +
	`! ( : > b.txt ) 2>/dev/null && [ ! -e b.txt ] && ` +
	`! ( rm -f a.txt ) 2>/dev/null && [ -e a.txt ] && ` +
	`! ( echo x >> a.txt ) 2>/dev/null && [ "$(cat a.txt)" = ok ]`

// SelfCheckReadOnly proves that the guest really mounts the workspace
// read-only: it runs a probe in a throwaway root through r with RootReadOnly
// set; the probe must read a file and fail to create, delete or append even
// after attempting a read-write remount. tmp is where the throwaway root is
// made. The host checks the root is byte-identical afterwards. Any run error
// fails the check (closed).
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
	before, err := TreeDigest(root)
	if err != nil {
		return err
	}
	code, err := r.Run(ctx, Request{Root: root, Dir: root, Command: readOnlyProbe, RootReadOnly: true, Timeout: 120, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return fmt.Errorf("microVM read-only self-check: %w", err)
	}
	after, err := TreeDigest(root)
	if err != nil {
		return fmt.Errorf("microVM read-only self-check: read the workspace back: %w", err)
	}
	if after != before {
		return errors.New("microVM read-only self-check: the guest changed the read-only workspace")
	}
	if code != 0 {
		return fmt.Errorf("microVM read-only self-check: the guest did not enforce a read-only workspace (probe exit %d)", code)
	}
	return nil
}

// TreeDigest hashes every entry under root (relative path, type, mode and,
// for files, content; for symlinks, the target), so two digests are equal
// only if the tree is byte-identical.
func TreeDigest(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%q %v\n", filepath.ToSlash(rel), info.Mode())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "-> %q\n", target)
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			sum := sha256.New()
			_, err = io.Copy(sum, f)
			f.Close()
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "= %x\n", sum.Sum(nil))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
