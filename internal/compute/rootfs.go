package compute

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// prepareGuestRoot creates a disposable writable copy of the bundled guest
// filesystem. The host-side project directory is mounted separately and is
// the only user workspace made visible inside the guest.
func prepareGuestRoot(base string) (string, func(), error) {
	for _, required := range []string{"bin/sh", "bin/mount"} {
		if err := validateGuestExecutable(base, required); err != nil {
			return "", nil, fmt.Errorf("bundled Linux guest root is missing required %s at %s", required, base)
		}
	}
	temp, err := os.MkdirTemp("", "umcode-guest-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary guest root: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	if info, statErr := os.Stat(base); statErr == nil {
		if err := os.Chmod(temp, preservedMode(info.Mode())); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("preserve guest root permissions: %w", err)
		}
	}
	if err := copyTree(base, temp); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("prepare disposable guest root: %w", err)
	}
	return temp, cleanup, nil
}

// validateGuestExecutable resolves Linux absolute symlinks relative to the
// guest root, not the host root. Minimized distributions commonly point /bin/sh
// at /bin/busybox; a host-side os.Stat would follow that link on the host.
func validateGuestExecutable(root, guestPath string) error {
	resolved, err := resolveGuestPath(root, guestPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("guest path %s is not an executable file", guestPath)
	}
	return nil
}

func resolveGuestPath(root, guestPath string) (string, error) {
	if filepath.IsAbs(guestPath) {
		return "", fmt.Errorf("guest path must be relative to root: %s", guestPath)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(guestPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("guest path escapes root: %s", guestPath)
	}
	current := root
	pending := strings.Split(clean, string(filepath.Separator))
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			rel, err := filepath.Rel(root, current)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("guest symlink escapes root: %s", guestPath)
			}
			continue
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		links++
		if links > 40 {
			return "", fmt.Errorf("too many guest symlinks: %s", guestPath)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			current = root
			target = strings.TrimLeft(target, string(filepath.Separator))
		}
		pending = append(strings.Split(filepath.Clean(target), string(filepath.Separator)), pending...)
	}
	rel, err := filepath.Rel(root, current)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("guest path escapes root: %s", guestPath)
	}
	return current, nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, preservedMode(info.Mode()))
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if cloned, err := cloneRegularFile(path, target, preservedMode(info.Mode())); err != nil {
				return err
			} else if cloned {
				return nil
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, in)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			return os.Chmod(target, preservedMode(info.Mode()))
		default:
			// Device nodes, sockets, and FIFOs are not needed in the guest's
			// root filesystem and are deliberately not copied from the bundle.
			return nil
		}
	})
}

func preservedMode(mode os.FileMode) os.FileMode {
	return mode.Perm() | mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
}
