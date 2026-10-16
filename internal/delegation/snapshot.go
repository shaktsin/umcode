package delegation

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/protocol"
)

// ReadSnapshotFile reads a regular blob from the exact owned result commit,
// never mutable checkout bytes or the parent checkout. It creates no workspace.
func (s *WorkspaceService) ReadSnapshotFile(ctx context.Context, taskID, childID, revision, name string) (protocol.ProjectReadFileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out protocol.ProjectReadFileResult
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00\r\n") || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return out, errors.New("canonical relative snapshot path required")
	}
	for _, part := range strings.Split(strings.ToLower(name), "/") {
		if part == ".git" || part == ".aws" || part == ".ssh" || part == ".codex" || part == ".umcode" || part == ".env" || strings.HasPrefix(part, ".env.") || part == "credentials" {
			return out, errors.New("private metadata is unavailable")
		}
	}
	child, err := s.Store.AgentChild(ctx, taskID, childID)
	if err != nil {
		return out, err
	}
	w, err := s.Store.AgentWorkspace(ctx, child.WorkspaceID)
	if err != nil {
		return out, errors.New("child snapshot unavailable")
	}
	if revision == "" || child.Revision != revision || w.ResultRevision != revision || w.TaskID != taskID || w.ChildID != childID || w.Cleaned || w.Cleaning || w.Root != s.Manager.Path(w.ID) {
		return out, errors.New("child snapshot unavailable for this revision")
	}
	if _, err = s.Manager.Lookup(ctx, w.ID, w.ParentRoot); err != nil {
		return out, errors.New("owned child snapshot unavailable")
	}
	if err = validatePrivate(ctx, w.Root); err != nil {
		return out, err
	}
	// ls-tree performs an exact literal lookup; symlinks and submodules are not files.
	entry, err := privateGit(ctx, w.Root, "--literal-pathspecs", "ls-tree", "-z", revision, "--", name)
	if err != nil {
		return out, err
	}
	metadata, actual, ok := strings.Cut(strings.TrimSuffix(entry, "\x00"), "\t")
	fields := strings.Fields(metadata)
	if !ok || actual != name || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return out, errors.New("snapshot regular file unavailable")
	}
	size, err := privateGit(ctx, w.Root, "cat-file", "-s", fields[2])
	if err != nil {
		return out, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil {
		return out, err
	}
	const max = 512 * 1024
	if n > max {
		return out, errors.New("snapshot file exceeds 512 KiB display limit")
	}
	content, err := privateGit(ctx, w.Root, "cat-file", "blob", fields[2])
	if err != nil {
		return out, err
	}
	out = protocol.ProjectReadFileResult{Path: filepath.ToSlash(name), Bytes: n, Content: content}
	if strings.ContainsRune(content, 0) || !utf8.ValidString(content) {
		out.Binary = true
		out.Content = ""
	}
	return out, nil
}
