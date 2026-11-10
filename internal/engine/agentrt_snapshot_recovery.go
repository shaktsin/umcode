package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shaktsin/umcode/internal/store"
)

// preparedTaskSnapshot makes a snapshot created before task admission
// discoverable after a process crash. The marker is outside the selected
// checkout and is removed only after the task/workspace transaction commits.
type preparedTaskSnapshot struct {
	TaskID      string `json:"taskId"`
	Destination string `json:"destination"`
}

func (e *Engine) markPreparedTaskSnapshot(taskID, destination string) error {
	if e.Worktrees == nil || destination != e.Worktrees.Path(taskID) {
		return errors.New("invalid prepared task snapshot destination")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(preparedTaskSnapshot{TaskID: taskID, Destination: destination})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(destination+".preparing", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func clearPreparedTaskSnapshot(destination string) { _ = os.Remove(destination + ".preparing") }

// reconcilePreparedTaskSnapshots reaps only private checkouts whose admission
// transaction never committed. A workspace record makes the checkout owned
// by the task; a malformed marker is left for manual inspection.
func (e *Engine) reconcilePreparedTaskSnapshots(ctx context.Context) {
	if e.Worktrees == nil {
		return
	}
	root := filepath.Dir(e.Worktrees.Path("task-snapshot-recovery"))
	markers, err := filepath.Glob(filepath.Join(root, "*.preparing"))
	if err != nil {
		e.Log.Warn("find prepared task snapshots", "error", err)
		return
	}
	for _, marker := range markers {
		if err := e.reconcilePreparedTaskSnapshot(ctx, root, marker); err != nil {
			e.Log.Warn("prepared task snapshot needs inspection", "marker", marker, "error", err)
		}
	}
}

func (e *Engine) reconcilePreparedTaskSnapshot(ctx context.Context, root, marker string) error {
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe snapshot marker: %v", err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	var p preparedTaskSnapshot
	if json.Unmarshal(raw, &p) != nil || p.TaskID == "" || p.Destination != e.Worktrees.Path(p.TaskID) || filepath.Dir(p.Destination) != root || marker != p.Destination+".preparing" || strings.Contains(filepath.Base(p.Destination), string(os.PathSeparator)) {
		return errors.New("prepared snapshot marker has invalid identity")
	}
	if workspace, err := e.Store.AgentTaskWorkspace(ctx, p.TaskID); err == nil {
		if workspace.ExecutionRoot != p.Destination {
			return errors.New("prepared snapshot disagrees with admitted task")
		}
		return os.Remove(marker)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if info, err := os.Lstat(p.Destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("prepared snapshot destination is not an owned directory")
		}
		if err := os.RemoveAll(p.Destination); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(p.Destination + ".ready"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Remove(marker)
}
