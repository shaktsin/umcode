//go:build darwin || linux

package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/store"
	"golang.org/x/sys/unix"
)

// fileTarget pins a directory and records the original file identity. All reads,
// temp creation, cleanup and rename use that descriptor; symlinks are never
// followed. This uses Go 1.24 APIs (os.Root.Rename requires Go 1.25).
type fileTarget struct {
	root, rel string
	dir       *os.File
	info      os.FileInfo
	before    []byte
}

func openDirectory(abs string) (*os.File, error) {
	if !filepath.IsAbs(abs) || filepath.Clean(abs) != abs {
		return nil, store.ErrMemoryConflict
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if component == "" {
			continue
		}
		next, e := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			if errors.Is(e, unix.ELOOP) || errors.Is(e, unix.ENOTDIR) {
				return nil, store.ErrMemoryConflict
			}
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), abs), nil
}

func openTarget(root, rel string) (*fileTarget, error) {
	if !memoryTarget(rel) {
		return nil, store.ErrMemoryConflict
	}
	abs, err := projects.Resolve(root, rel)
	if err != nil || abs != filepath.Join(root, filepath.FromSlash(rel)) {
		return nil, store.ErrMemoryConflict
	}
	dir, err := openDirectory(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	t := &fileTarget{root: root, rel: rel, dir: dir}
	t.before, t.info, err = t.read()
	if err != nil {
		dir.Close()
		return nil, err
	}
	if t.info == nil && rel != "UMCODE.md" {
		dir.Close()
		return nil, store.ErrMemoryConflict
	}
	return t, nil
}

func (t *fileTarget) close() { t.dir.Close() }

func (t *fileTarget) read() ([]byte, os.FileInfo, error) {
	fd, err := unix.Openat(int(t.dir.Fd()), "UMCODE.md", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, nil
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, nil, store.ErrMemoryConflict
		}
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), "UMCODE.md")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, store.ErrMemoryConflict
	}
	b, err := io.ReadAll(io.LimitReader(f, 32<<10+1))
	if err != nil {
		return nil, nil, err
	}
	if len(b) > 32<<10 {
		return nil, nil, errTargetSizeLimit
	}
	return b, info, nil
}

func (t *fileTarget) check(hash string) error {
	abs, err := projects.Resolve(t.root, t.rel)
	if err != nil || abs != filepath.Join(t.root, filepath.FromSlash(t.rel)) {
		return store.ErrMemoryConflict
	}
	current, err := openDirectory(filepath.Dir(abs))
	if err != nil {
		if os.IsNotExist(err) {
			return store.ErrMemoryConflict
		}
		return err
	}
	defer current.Close()
	a, err := current.Stat()
	if err != nil {
		return err
	}
	b, err := t.dir.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) {
		return store.ErrMemoryConflict
	}
	bytes, info, err := t.read()
	if err != nil {
		return err
	}
	if (info == nil) != (t.info == nil) || info != nil && (!os.SameFile(info, t.info) || info.Mode() != t.info.Mode()) || memoryHash(bytes) != hash {
		return store.ErrMemoryConflict
	}
	return nil
}

func (t *fileTarget) replace(after []byte, step func(string) error) error {
	if t.info != nil && t.info.Mode().Perm()&0222 == 0 {
		return errors.New("memory target read only")
	}
	if err := step("create"); err != nil {
		return err
	}
	if err := t.check(memoryHash(t.before)); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".umcode-memory-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(t.dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	tempInfo, err := f.Stat()
	if err != nil {
		return err
	}
	// Cleanup only the exact temp inode this call created.
	defer func() {
		if t.sameTemp(name, tempInfo) {
			unix.Unlinkat(int(t.dir.Fd()), name, 0)
		}
	}()
	checkedStep := func(stage string) error {
		if err := step(stage); err != nil {
			return err
		}
		if err := t.check(memoryHash(t.before)); err != nil {
			return err
		}
		if !t.sameTemp(name, tempInfo) {
			return store.ErrMemoryConflict
		}
		return nil
	}
	if err := checkedStep("write"); err != nil {
		return err
	}
	if _, err := f.Write(after); err != nil {
		return err
	}
	if err := checkedStep("chmod"); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if t.info != nil {
		mode = t.info.Mode()
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := checkedStep("sync"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := step("rename"); err != nil {
		return err
	}
	// The injection seam precedes the final CAS and identity checks, just as an
	// external editor can run while the temporary file is being prepared.
	if err := t.check(memoryHash(t.before)); err != nil {
		return err
	}
	if !t.sameTemp(name, tempInfo) {
		return store.ErrMemoryConflict
	}
	if err := unix.Renameat(int(t.dir.Fd()), name, int(t.dir.Fd()), "UMCODE.md"); err != nil {
		return err
	}
	if err := step("after_rename"); err != nil {
		return err
	}
	if err := step("parent_sync"); err != nil {
		return err
	}
	if err := t.dir.Sync(); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	return nil
}

func (t *fileTarget) sameTemp(name string, want os.FileInfo) bool {
	fd, err := unix.Openat(int(t.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	got, err := f.Stat()
	return err == nil && os.SameFile(got, want)
}
