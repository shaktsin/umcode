package projects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// maxUndoBytes is the largest previous version kept for an undo.
const maxUndoBytes = 1 << 20

// Recorder records what one turn changed on disk. Tools call Record after every
// successful write; the engine turns each change into a fileChange item.
type Recorder struct {
	svc      *Service
	project  protocol.Project
	threadID string
	turnID   string
	emit     func(protocol.FileChangeData)
}

// NewRecorder returns a recorder for one turn. emit may be nil.
func (s *Service) NewRecorder(p protocol.Project, threadID, turnID string, emit func(protocol.FileChangeData)) *Recorder {
	return &Recorder{svc: s, project: p, threadID: threadID, turnID: turnID, emit: emit}
}

// Project is the project this recorder writes into.
func (r *Recorder) Project() protocol.Project { return r.project }

// Snapshot reads a file's current content so it can be diffed and undone after
// a write. A missing file returns nil, which marks the change as a creation.
func Snapshot(abs string) *string {
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() || st.Size() > maxUndoBytes {
		if err == nil && st.Size() > maxUndoBytes {
			// Too big to keep: record that it existed, without the content.
			big := ""
			return &big
		}
		return nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	s := string(data)
	return &s
}

// Record stores one change and emits its fileChange payload. before is the
// content before the write (nil when the file was created); after is read from
// disk, or pass a deleted marker by setting deleted.
func (r *Recorder) Record(ctx context.Context, abs string, before *string, deleted bool) protocol.FileChangeData {
	rel := Rel(r.project.Root, abs)
	after := ""
	if !deleted {
		if data, err := os.ReadFile(abs); err == nil {
			after = string(data)
		}
	}
	action := protocol.FileModified
	switch {
	case deleted:
		action = protocol.FileDeleted
	case before == nil:
		action = protocol.FileCreated
	}
	beforeText := ""
	if before != nil {
		beforeText = *before
	}
	diff, adds, dels, truncated := Unified(rel, beforeText, after)
	revertable := before != nil || action == protocol.FileCreated
	if before != nil && len(*before) == 0 && action == protocol.FileModified {
		// We only know it existed, not what it held.
		revertable = false
	}
	data := protocol.FileChangeData{
		Path: rel, Action: action, TurnID: r.turnID, Additions: adds, Deletions: dels,
		Diff: diff, Truncated: truncated, Revertable: revertable,
	}
	sum := sha256.Sum256([]byte(after))
	if _, err := r.svc.st.RecordFileChange(ctx, store.FileChange{
		ProjectID: r.project.ID, ThreadID: r.threadID, TurnID: r.turnID,
		Path: rel, Action: action, Before: before, AfterHash: hex.EncodeToString(sum[:8]),
		Additions: adds, Deletions: dels, Revertable: revertable,
	}); err != nil {
		// Recording failed (e.g. the database is busy); the item still shows
		// the change, it just cannot be undone from history.
		data.Revertable = false
	}
	if r.emit != nil {
		r.emit(data)
	}
	return data
}

// RecordPromotion persists exact operation snapshots before emitting a change.
// It never rereads the replaced path, which may already have been externally
// changed. Recovery returns the same payload without emitting a second event.
func (r *Recorder) RecordPromotion(ctx context.Context, promotionOpID, abs string, before *string) (protocol.FileChangeData, error) {
	op, err := r.svc.st.GetMemoryPromotionOp(ctx, promotionOpID)
	if err != nil {
		return protocol.FileChangeData{}, err
	}
	if op.ProjectID != r.project.ID || op.ThreadID != r.threadID || op.TurnID != r.turnID || filepath.Clean(abs) != filepath.Join(r.project.Root, filepath.FromSlash(op.TargetPath)) || filepath.Base(abs) != "UMCODE.md" {
		return protocol.FileChangeData{}, fmt.Errorf("promotion recorder provenance mismatch")
	}
	if (before == nil) != (op.BeforeBytes == nil) || before != nil && !bytes.Equal([]byte(*before), op.BeforeBytes) {
		return protocol.FileChangeData{}, fmt.Errorf("promotion recorder snapshot mismatch")
	}
	text := ""
	if before != nil {
		text = *before
	}
	action := protocol.FileModified
	if before == nil {
		action = protocol.FileCreated
	}
	diff, adds, dels, truncated := Unified(op.TargetPath, text, string(op.AfterBytes))
	data := protocol.FileChangeData{Path: op.TargetPath, Action: action, TurnID: r.turnID, Additions: adds, Deletions: dels, Diff: diff, Truncated: truncated, Revertable: true}
	sum := sha256.Sum256(op.AfterBytes)
	_, inserted, err := r.svc.st.RecordFileChangeOnce(ctx, store.FileChange{ProjectID: r.project.ID, ThreadID: r.threadID, TurnID: r.turnID, Path: op.TargetPath, Action: action, Before: before, AfterHash: hex.EncodeToString(sum[:8]), Additions: adds, Deletions: dels, Revertable: true, PromotionOpID: promotionOpID})
	if err != nil {
		return protocol.FileChangeData{}, err
	}
	if inserted && r.emit != nil {
		r.emit(data)
	}
	return data, nil
}

// Diff reports what has been changed, newest first. With a turn id it is what
// that turn did, compared against the content it found.
func (s *Service) Diff(ctx context.Context, p protocol.Project, params protocol.ProjectDiffParams) (protocol.ProjectDiffResult, error) {
	changes, err := s.st.ListFileChanges(ctx, p.ID, params.TurnID, params.Path, params.Limit)
	if err != nil {
		return protocol.ProjectDiffResult{}, err
	}
	// One row per path: the oldest "before" in the range against what is there now.
	oldest := map[string]store.FileChange{}
	order := []string{}
	for _, c := range changes { // newest first
		if _, seen := oldest[c.Path]; !seen {
			order = append(order, c.Path)
		}
		oldest[c.Path] = c
	}
	sort.Strings(order)
	out := protocol.ProjectDiffResult{}
	for _, path := range order {
		c := oldest[path]
		before := ""
		if c.Before != nil {
			before = *c.Before
		}
		after := ""
		action := c.Action
		abs, err := Resolve(p.Root, path)
		if err != nil {
			continue
		}
		if data, err := os.ReadFile(abs); err == nil {
			after = string(data)
		} else {
			action = protocol.FileDeleted
		}
		diff, adds, dels, truncated := Unified(path, before, after)
		if diff == "" && adds == 0 && dels == 0 {
			continue // reverted, or changed back by hand
		}
		out.Files = append(out.Files, protocol.FileChangeData{
			Path: path, Action: action, TurnID: c.TurnID, Additions: adds, Deletions: dels,
			Diff: diff, Truncated: truncated, Revertable: c.Revertable,
		})
	}
	return out, nil
}

// RevertTurn restores the files a turn changed to the content it found. Files
// whose previous version was too large to keep are skipped.
func (s *Service) RevertTurn(ctx context.Context, turnID string, paths []string) (protocol.ProjectRevertTurnResult, error) {
	return s.RevertTurnAt(ctx, turnID, paths, nil)
}

// RevertTurnAt is RevertTurn with an optional project-root override. Task
// workspaces use this to ensure an undo never writes to the source checkout.
func (s *Service) RevertTurnAt(ctx context.Context, turnID string, paths []string, roots map[string]string) (protocol.ProjectRevertTurnResult, error) {
	var res protocol.ProjectRevertTurnResult
	changes, err := s.st.ListFileChanges(ctx, "", turnID, "", 500)
	if err != nil {
		return res, err
	}
	if len(changes) == 0 {
		return res, fmt.Errorf("no recorded file changes for turn %s", turnID)
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	// Oldest state per path is what we restore to.
	first := map[string]store.FileChange{}
	for _, c := range changes { // newest first, so the last write wins
		first[c.Path] = c
	}
	projects := map[string]protocol.Project{}
	for path, c := range first {
		if len(want) > 0 && !want[path] {
			continue
		}
		p, ok := projects[c.ProjectID]
		if !ok {
			p, err = s.st.GetProject(ctx, c.ProjectID)
			if err != nil {
				res.Skipped = append(res.Skipped, path)
				continue
			}
			projects[c.ProjectID] = p
		}
		root := p.Root
		if taskRoot := roots[c.ProjectID]; taskRoot != "" {
			root = taskRoot
		}
		abs, err := Resolve(root, path)
		if err != nil {
			res.Skipped = append(res.Skipped, path)
			continue
		}
		switch {
		case c.Action == protocol.FileCreated:
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				res.Skipped = append(res.Skipped, path)
				continue
			}
		case c.Before != nil && c.Revertable:
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				res.Skipped = append(res.Skipped, path)
				continue
			}
			if err := os.WriteFile(abs, []byte(*c.Before), 0o644); err != nil {
				res.Skipped = append(res.Skipped, path)
				continue
			}
		default:
			res.Skipped = append(res.Skipped, path)
			continue
		}
		res.Reverted = append(res.Reverted, path)
	}
	sort.Strings(res.Reverted)
	sort.Strings(res.Skipped)
	if len(res.Skipped) > 0 {
		res.Reason = "the previous version of " + strings.Join(res.Skipped, ", ") + " was too large to keep, so it was left alone"
	}
	return res, nil
}
