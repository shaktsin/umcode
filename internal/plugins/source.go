package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type materializedSource struct {
	root      string
	canonical string
	cleanup   func()
}

func materializeSource(ctx context.Context, source string) (materializedSource, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return materializedSource{}, pluginError("inspect", "inspect/empty_source", "source", "plugin source is empty", "Choose a local plugin directory or Git URL.", nil)
	}
	if !isGitSource(source) {
		path := source
		if parsed, err := url.Parse(source); err == nil && parsed.Scheme == "file" {
			path = parsed.Path
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return materializedSource{}, pluginError("inspect", "inspect/invalid_source", "source", "invalid local plugin source", "Choose a readable plugin directory.", err)
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return materializedSource{}, pluginError("inspect", "inspect/invalid_source", "source", "invalid local plugin source", "Choose a readable plugin directory.", err)
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() {
			return materializedSource{}, pluginError("inspect", "inspect/invalid_source", "source", "local plugin source is not a directory", "Choose a readable plugin directory.", err)
		}
		return materializedSource{root: absolute, canonical: absolute, cleanup: func() {}}, nil
	}

	canonical, err := canonicalGitSource(source)
	if err != nil {
		return materializedSource{}, pluginError("inspect", "inspect/invalid_source", "source", "invalid Git plugin source", "Choose a valid Git URL.", err)
	}
	tmp, err := os.MkdirTemp("", "umcode-plugin-source-")
	if err != nil {
		return materializedSource{}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	root := filepath.Join(tmp, "plugin")
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--", source, root)
	if output, cloneErr := cmd.CombinedOutput(); cloneErr != nil {
		cleanup()
		return materializedSource{}, pluginError("inspect", "inspect/git_clone", "source", "failed to clone plugin source", "Check the Git URL and repository access.", fmt.Errorf("%w: %s", cloneErr, strings.TrimSpace(string(output))))
	}
	return materializedSource{root: root, canonical: canonical, cleanup: cleanup}, nil
}

func isGitSource(source string) bool {
	if strings.HasPrefix(source, "git@") || strings.HasPrefix(source, "ssh://") || strings.HasPrefix(source, "git://") {
		return true
	}
	parsed, err := url.Parse(source)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func canonicalGitSource(source string) (string, error) {
	if strings.HasPrefix(source, "git@") {
		return strings.TrimSuffix(source, "/"), nil
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Git URL")
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed.String(), nil
}

func sourceID(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:16]
}

func digestTree(root string) (string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return pathEscapeError(filepath.ToSlash(rel), fmt.Errorf("symbolic links are not installable"))
		}
		if entry.IsDir() && ignoredDigestDir(entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, rel := range paths {
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(rel))
		_, _ = io.WriteString(hash, "\x00"+info.Mode().Perm().String()+"\x00")
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = io.WriteString(hash, "\x00")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func ignoredDigestDir(name string) bool {
	switch name {
	case ".git", ".venv", "node_modules", ".staging", ".umcode":
		return true
	default:
		return false
	}
}

func copyTreeNoLinks(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return pathEscapeError(filepath.ToSlash(rel), fmt.Errorf("symbolic links are not installable"))
		}
		if entry.IsDir() && ignoredDigestDir(entry.Name()) {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		return closeErr
	})
}
