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
	if err := snapshotPath(name); err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	child, err := s.Store.AgentChild(ctx, taskID, childID)
	if err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	return s.readSnapshot(ctx, taskID, childID, child.WorkspaceID, child.Revision, revision, name)
}

// ReadRuntimeSnapshotFile is ReadSnapshotFile for a runtime agent: it reads
// from the agent's retained workspace at the agent's recorded revision, with
// the same confinement, limits and binary handling. An agent with no
// recorded revision (read-only, or not finished) has no snapshot.
func (s *WorkspaceService) ReadRuntimeSnapshotFile(ctx context.Context, taskID, agentID, revision, name string) (protocol.ProjectReadFileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := snapshotPath(name); err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	a, err := s.Store.RuntimeAgent(ctx, taskID, agentID)
	if err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	return s.readSnapshot(ctx, taskID, agentID, a.WorkspaceID, a.Revision, revision, name)
}

// errSnapshotRead replaces Git failures, whose output names host paths.
var errSnapshotRead = errors.New("snapshot file could not be read")

// snapshotPath accepts only a canonical relative path outside private
// metadata.
func snapshotPath(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00\r\n") || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return errors.New("canonical relative snapshot path required")
	}
	for _, part := range strings.Split(strings.ToLower(name), "/") {
		if part == ".git" || part == ".aws" || part == ".ssh" || part == ".codex" || part == ".umcode" || part == ".env" || strings.HasPrefix(part, ".env.") || part == "credentials" {
			return errors.New("private metadata is unavailable")
		}
	}
	return nil
}

// readSnapshot reads name from the owner's workspace at revision, which must
// be the owner's recorded revision and the workspace's result revision. The
// caller holds s.mu and has checked name with snapshotPath.
func (s *WorkspaceService) readSnapshot(ctx context.Context, taskID, ownerID, workspaceID, recorded, revision, name string) (protocol.ProjectReadFileResult, error) {
	var out protocol.ProjectReadFileResult
	w, err := s.Store.AgentWorkspace(ctx, workspaceID)
	if err != nil {
		return out, errors.New("child snapshot unavailable")
	}
	if revision == "" || recorded != revision || w.ResultRevision != revision || w.TaskID != taskID || w.ChildID != ownerID || w.Cleaned || w.Cleaning || w.Root != s.Manager.Path(w.ID) {
		return out, errors.New("child snapshot unavailable for this revision")
	}
	if _, err = s.Manager.Lookup(ctx, w.ID, w.ParentRoot); err != nil {
		return out, errors.New("owned child snapshot unavailable")
	}
	// Metadata and Git failures name host paths; results and errors never do.
	if err = validatePrivate(ctx, w.Root); err != nil {
		return out, errors.New("owned child snapshot unavailable")
	}
	// ls-tree performs an exact literal lookup; symlinks and submodules are not files.
	entry, err := privateGit(ctx, w.Root, "--literal-pathspecs", "ls-tree", "-z", revision, "--", name)
	if err != nil {
		return out, errSnapshotRead
	}
	metadata, actual, ok := strings.Cut(strings.TrimSuffix(entry, "\x00"), "\t")
	fields := strings.Fields(metadata)
	if !ok || actual != name || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return out, errors.New("snapshot regular file unavailable")
	}
	size, err := privateGit(ctx, w.Root, "cat-file", "-s", fields[2])
	if err != nil {
		return out, errSnapshotRead
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
		return out, errSnapshotRead
	}
	out = protocol.ProjectReadFileResult{Path: filepath.ToSlash(name), Bytes: n, Content: content}
	if strings.ContainsRune(content, 0) || !utf8.ValidString(content) {
		out.Binary = true
		out.Content = ""
	}
	return out, nil
}
