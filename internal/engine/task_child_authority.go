package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// Inherit requirements from the admitted lead task, never from a newer task
// that happened to become current in the same chat. The actual child scope is
// not widened; only a private comparison copy translates workspace-relative paths.
func (e *Engine) checkChildTaskAuthority(ctx context.Context, operation protocol.TaskOperation) (protocol.AuthorityDecision, error) {
	x := childExecutionFrom(ctx)
	if x == nil {
		return protocol.AuthorityDecision{Outcome: "allow"}, nil
	}
	parent, err := e.Store.GetThread(ctx, x.task.ThreadID)
	if err != nil {
		return protocol.AuthorityDecision{}, err
	}
	snapshot, err := e.Store.ActiveExecutionTask(ctx, parent.ID)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.AuthorityDecision{Outcome: "allow"}, nil
	}
	if err != nil {
		return protocol.AuthorityDecision{}, err
	}
	bound := ""
	for _, op := range snapshot.Operations {
		var resources struct {
			AgentTaskID string `json:"agent_task_id"`
		}
		_ = json.Unmarshal(op.Resources, &resources)
		if op.TurnID == x.task.RootTurnID || resources.AgentTaskID == x.task.ID {
			bound = snapshot.Task.ID
			break
		}
	}
	if bound == "" {
		if origin, err := e.Store.GetItem(ctx, snapshot.Task.OriginItemID); err == nil && origin.TurnID == x.task.RootTurnID {
			bound = snapshot.Task.ID
		}
	}
	if bound == "" {
		return taskDenied(operation, "policy_denied", "The admitted parent task is no longer current.", "Return to the lead chat for a new scoped assignment."), nil
	}
	actual := tools.ScopeFrom(ctx)
	if actual == nil {
		return taskDenied(operation, "policy_denied", "Child workspace scope is unavailable.", "Return to the lead chat."), nil
	}
	comparison := *actual
	comparison.Root = x.task.ParentRoot
	comparison.ThreadID = parent.ID
	comparison.ProjectID = parent.ProjectID
	comparison.ReadOnly = actual.ReadOnly || x.child.Role == protocol.AgentRoleReviewer
	if operation.Tool == "file.read" || operation.Tool == "file.write" || operation.Tool == "file.edit" {
		var args map[string]json.RawMessage
		if json.Unmarshal(operation.Args, &args) == nil {
			var path string
			if json.Unmarshal(args["path"], &path) == nil && path != "" {
				absolute, err := actual.Resolve(path)
				if err != nil {
					return taskDenied(operation, "policy_denied", err.Error(), "Keep access inside the owned child checkout."), nil
				}
				relative, err := filepath.Rel(actual.Root, absolute)
				if err != nil {
					return protocol.AuthorityDecision{}, err
				}
				args["path"], _ = json.Marshal(relative)
				operation.Args, _ = json.Marshal(args)
			}
		}
	}
	operation.TaskID = bound
	return e.checkTaskAuthority(tools.WithScope(ctx, &comparison), parent, snapshot, operation)
}
