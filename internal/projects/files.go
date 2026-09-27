package projects

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/protocol"
)

// skipDirs are never walked or listed: noise that would bury the tree.
// .umcode holds this app's own bookkeeping for the project (Computer Use and
// Visual QA screenshots, worktrees) — not something to show as a project file.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true, ".next": true,
	".gradle": true, ".idea": true, "dist": true, "build": true, "target": true,
	".DS_Store": true, ".terraform": true, ".tox": true, ".umcode": true,
}

const (
	defaultFileLimit = 2000
	maxReadBytes     = 1 << 20 // 1 MB
)

// Files lists a directory inside the project, optionally a few levels deep.
func (s *Service) Files(ctx context.Context, p protocol.Project, params protocol.ProjectFilesParams) (protocol.ProjectFilesResult, error) {
	start, err := Resolve(p.Root, params.Path)
	if err != nil {
		return protocol.ProjectFilesResult{}, err
	}
	st, err := os.Stat(start)
	if err != nil {
		return protocol.ProjectFilesResult{}, err
	}
	if !st.IsDir() {
		return protocol.ProjectFilesResult{}, fmt.Errorf("%s is a file; use project/readFile", params.Path)
	}
	depth := params.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}
	limit := params.Limit
	if limit <= 0 || limit > 10000 {
		limit = defaultFileLimit
	}
	res := protocol.ProjectFilesResult{Path: Rel(p.Root, start)}
	if res.Path == "." {
		res.Path = ""
	}
	var walk func(dir string, level int) error
	walk = func(dir string, level int) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return entries[i].Name() < entries[j].Name()
		})
		for _, e := range entries {
			if len(res.Entries) >= limit {
				res.Truncated = true
				return nil
			}
			name := e.Name()
			if skipDirs[name] {
				continue
			}
			full := filepath.Join(dir, name)
			info, err := e.Info()
			if err != nil {
				continue
			}
			entry := protocol.FileEntry{
				Name: name, Path: Rel(p.Root, full), Dir: e.IsDir(),
				Symlink: info.Mode()&os.ModeSymlink != 0, ModTime: info.ModTime(),
			}
			if !e.IsDir() {
				entry.Size = info.Size()
			}
			res.Entries = append(res.Entries, entry)
			if e.IsDir() && level < depth {
				if err := walk(full, level+1); err != nil {
					continue
				}
			}
		}
		return nil
	}
	if err := walk(start, 1); err != nil {
		return res, err
	}
	return res, nil
}

// ReadFile returns a text file's contents, truncated at maxBytes.
func (s *Service) ReadFile(ctx context.Context, p protocol.Project, params protocol.ProjectReadFileParams) (protocol.ProjectReadFileResult, error) {
	abs, err := Resolve(p.Root, params.Path)
	if err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	if st.IsDir() {
		return protocol.ProjectReadFileResult{}, fmt.Errorf("%s is a folder", params.Path)
	}
	max := params.MaxBytes
	if max <= 0 || max > maxReadBytes {
		max = maxReadBytes
	}
	f, err := os.Open(abs)
	if err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	defer f.Close()
	buf := make([]byte, max)
	n, _ := f.Read(buf)
	buf = buf[:n]
	res := protocol.ProjectReadFileResult{
		Path: Rel(p.Root, abs), Bytes: st.Size(), ModTime: st.ModTime(),
		Truncated: st.Size() > int64(n),
	}
	if !utf8.Valid(buf) {
		res.Binary = true
		return res, nil
	}
	res.Content = string(buf)
	return res, nil
}

// ReadArtifact returns a bounded browser screenshot using an explicit image
// allowlist. Reports and traces remain listed by path and are never executed.
func (s *Service) ReadArtifact(ctx context.Context, p protocol.Project, params protocol.ProjectReadArtifactParams) (protocol.ProjectReadArtifactResult, error) {
	abs, err := Resolve(p.Root, params.Path)
	if err != nil {
		return protocol.ProjectReadArtifactResult{}, err
	}
	mime := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp"}[strings.ToLower(filepath.Ext(abs))]
	if mime == "" {
		return protocol.ProjectReadArtifactResult{}, fmt.Errorf("%s is not a supported screenshot", params.Path)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return protocol.ProjectReadArtifactResult{}, err
	}
	if st.IsDir() || st.Size() > 8<<20 {
		return protocol.ProjectReadArtifactResult{}, fmt.Errorf("screenshot must be a file no larger than 8 MiB")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return protocol.ProjectReadArtifactResult{}, err
	}
	return protocol.ProjectReadArtifactResult{Path: Rel(p.Root, abs), MimeType: mime, DataB64: base64.StdEncoding.EncodeToString(data), Bytes: st.Size()}, nil
}

// gitInfo reads the branch, remote and dirty count of a checkout. It returns
// nil when the folder is not a git repository or git is unavailable.
func gitInfo(root string) *protocol.VCSInfo {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return &protocol.VCSInfo{Kind: "git"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	v := &protocol.VCSInfo{Kind: "git", HasCommit: run("rev-parse", "--verify", "HEAD^{commit}") != "",
		Branch: run("rev-parse", "--abbrev-ref", "HEAD"), Remote: run("remote", "get-url", "origin")}
	if status := run("status", "--porcelain"); status != "" {
		v.Dirty = len(strings.Split(status, "\n"))
	}
	return v
}

// countLines is used by the diff summary.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// humanBytes formats a size for messages.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}
