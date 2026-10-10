//go:build darwin || linux

package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/retrieval"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func readRetrievalFile(ctx context.Context, root, rel string) ([]byte, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !retrieval.AllowedExcerptPath(rel) {
		return nil, errors.New("invalid retrieval path")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Join(root, filepath.FromSlash(rel)), "/"), "/")
	for i, part := range parts {
		if ctx.Err() != nil {
			unix.Close(fd)
			return nil, ctx.Err()
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, e := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > retrieval.MaxFileBytes {
		return nil, errors.New("ineligible retrieval file")
	}
	b, err := io.ReadAll(io.LimitReader(f, retrieval.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > retrieval.MaxFileBytes {
		return nil, errors.New("oversized retrieval file")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return b, nil
}
