package engine

import (
	"context"
	"errors"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func (e *Engine) ownedDelegation(ctx context.Context, threadID, taskID string) (protocol.AgentTask, error) {
	task, err := e.Store.AgentTask(ctx, taskID)
	if err != nil {
		return task, err
	}
	parent, err := e.Store.GetThread(ctx, threadID)
	if err != nil {
		return task, err
	}
	if delegationChild(ctx) || threadID == "" || task.ThreadID != parent.ID {
		return task, protocol.Errorf(protocol.CodeInvalidParams, "owning parent chat required")
	}
	return task, nil
}

// ListDelegation is hydration only: no admission, retries, workspace creation or
// get-or-create budget call. Historical recovery stays readable after a move.
func (e *Engine) ListDelegation(ctx context.Context, p protocol.DelegationListParams) (protocol.DelegationListResult, error) {
	result := protocol.DelegationListResult{Tasks: []protocol.AgentSnapshot{}}
	if delegationChild(ctx) || p.ThreadID == "" {
		return result, protocol.Errorf(protocol.CodeInvalidParams, "owning parent chat required")
	}
	if _, err := e.Store.GetThread(ctx, p.ThreadID); err != nil {
		return result, err
	}
	tasks, err := e.Store.AgentTasksForThread(ctx, p.ThreadID)
	if err != nil {
		return result, err
	}
	if p.TaskID != "" {
		task, err := e.ownedDelegation(ctx, p.ThreadID, p.TaskID)
		if err != nil {
			return result, err
		}
		tasks = []protocol.AgentTask{task}
	}
	for _, task := range tasks {
		children, errs, sequences, err := e.DelegationCoordinator.Snapshot(ctx, task.ID)
		if err != nil {
			return result, err
		}
		// Public UI data never needs host roots or workspace IDs.
		task.BaseRoot = ""
		task.ParentRoot = ""
		for i := range children {
			children[i].WorkspaceID = ""
		}
		snapshot := protocol.AgentSnapshot{Task: task, Children: children, ChildErrors: errs, ChildSequences: sequences}
		workflow, err := e.Store.AgentWorkflow(ctx, task.ID)
		if err == nil {
			snapshot.Workflow = &workflow
		} else if !errors.Is(err, store.ErrNotFound) {
			return result, err
		}
		_, snapshot.Usage, err = e.Store.ReadAgentTaskBudget(ctx, task.ID)
		if err == nil {
			snapshot.BudgetAvailable = true
		} else if !errors.Is(err, store.ErrNotFound) {
			return result, err
		}
		if usage, stop, live := e.DelegationCoordinator.LiveBudget(task.ID); live {
			snapshot.Usage = usage
			snapshot.StopReason = stop
			snapshot.BudgetAvailable = true
		}
		result.Tasks = append(result.Tasks, snapshot)
	}
	return result, nil
}
func (e *Engine) ReadDelegationFile(ctx context.Context, p protocol.DelegationFileParams) (protocol.ProjectReadFileResult, error) {
	if _, err := e.ownedDelegation(ctx, p.ThreadID, p.TaskID); err != nil {
		return protocol.ProjectReadFileResult{}, err
	}
	return e.DelegationWorkspaceService.ReadSnapshotFile(ctx, p.TaskID, p.ChildID, p.Revision, p.Path)
}
