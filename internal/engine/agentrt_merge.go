package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
)

// mergeRuntimeTaskResult transfers a verified private task tree to the selected
// checkout. Old tasks without a private workspace retain their old completion
// semantics. An unresolved journal intent is never automatically reapplied.
func (e *Engine) mergeRuntimeTaskResult(ctx context.Context, taskID, headTree string) error {
	workspace, err := e.Store.AgentTaskWorkspace(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest worktree.SnapshotManifest
	if err := json.Unmarshal(workspace.Manifest, &manifest); err != nil {
		return fmt.Errorf("invalid task snapshot manifest: %w", err)
	}
	snapshot := worktree.TaskSnapshot{SourceRoot: workspace.SourceRoot, Destination: workspace.ExecutionRoot, SourceHead: workspace.SourceHead, BaseCommit: workspace.SnapshotCommit, Manifest: manifest, UntrackedPaths: manifest.Untracked}
	prior, err := e.Store.TaskMerge(ctx, taskID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		if prior.HeadTree != headTree || prior.SourceRoot != workspace.SourceRoot {
			return errors.New("merge journal does not match the verified task head")
		}
		if prior.State == "merged" {
			var saved worktree.TaskMergePlan
			if err := json.Unmarshal(prior.Plan, &saved); err != nil || worktree.InspectTaskMerge(ctx, saved) != "after" {
				return errors.New("merged task result changed in the selected checkout; inspect it before completing the task")
			}
			return nil
		}
		if prior.State == "intent" || prior.State == "ambiguous" {
			return fmt.Errorf("task merge is %s; inspect the selected checkout and private result before retrying", prior.State)
		}
	}
	plan, err := worktree.PrepareTaskMerge(ctx, workspace.SourceRoot, snapshot, headTree)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if err := e.Store.BeginTaskMerge(ctx, store.TaskMergeRecord{TaskID: taskID, SourceRoot: workspace.SourceRoot, HeadTree: headTree, Plan: raw}); err != nil {
		return err
	}
	receipt, err := worktree.ApplyTaskMerge(ctx, plan)
	if err != nil {
		status := "conflict"
		if strings.Contains(err.Error(), "rollback incomplete") {
			status = "ambiguous"
		}
		if markErr := e.Store.MarkTaskMerge(context.WithoutCancel(ctx), taskID, status, err.Error()); markErr != nil {
			return fmt.Errorf("task merge failed: %v; journal update failed: %w", err, markErr)
		}
		return err
	}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := e.Store.RecordTaskMergeReceipt(context.WithoutCancel(ctx), taskID, receiptRaw); err != nil {
		return fmt.Errorf("task merge applied but receipt was not recorded: %w", err)
	}
	return nil
}

// reconcileTaskMerges classifies interrupted merge intents without applying
// any file. An all-before state can be retried; an all-after state receives a
// durable receipt; mixed or changed states require manual review.
func (e *Engine) reconcileTaskMerges(ctx context.Context) {
	records, err := e.Store.PendingTaskMerges(ctx)
	if err != nil {
		e.Log.Warn("task merge recovery failed", "error", err)
		return
	}
	for _, record := range records {
		if record.State != "intent" {
			continue
		}
		var plan worktree.TaskMergePlan
		if err := json.Unmarshal(record.Plan, &plan); err != nil {
			_ = e.Store.MarkTaskMerge(ctx, record.TaskID, "ambiguous", "invalid saved merge plan")
			continue
		}
		switch worktree.InspectTaskMerge(ctx, plan) {
		case "before":
			_ = e.Store.MarkTaskMerge(ctx, record.TaskID, "conflict", "merge stopped before source changes; retry completion")
		case "after":
			receipt := worktree.MergeReceipt{HeadTree: plan.HeadTree, Before: map[string]worktree.SnapshotPath{}, After: map[string]worktree.SnapshotPath{}, AppliedAt: time.Now().UTC()}
			for _, change := range plan.Changes {
				receipt.Paths = append(receipt.Paths, change.Path)
				receipt.Before[change.Path] = change.Before
				receipt.After[change.Path] = change.After
			}
			raw, _ := json.Marshal(receipt)
			if err := e.Store.RecordTaskMergeReceipt(ctx, record.TaskID, raw); err != nil {
				e.Log.Warn("task merge receipt recovery failed", "task", record.TaskID, "error", err)
			}
		default:
			_ = e.Store.MarkTaskMerge(ctx, record.TaskID, "ambiguous", "selected checkout differs from both merge endpoints")
		}
	}
}
