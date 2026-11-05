package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// A runtime agent may call only the tools its capabilities grant
// (runtimeToolAllowed), so plugins, MCP, scripts, hooks, sessions and unknown
// future tools cannot bypass its authority by guessing a canonical/wire name.
// An execution that is not a runtime agent's gets no tool at all.
func childToolAllowed(ctx context.Context, name string) bool {
	x := childExecutionFrom(ctx)
	if x == nil {
		return true
	}
	if x.runtime == nil {
		return false
	}
	return runtimeToolAllowed(x.runtime.agent.Capabilities, name)
}

// Recheck ownership and intersect live policy with the admitted child scope.
// Never replace its isolated root with the source project's root.
func (e *Engine) narrowChildToolAuthority(ctx context.Context, th protocol.Thread) (context.Context, protocol.Thread, error) {
	x := childExecutionFrom(ctx)
	if x == nil {
		return ctx, th, nil
	}
	if err := ctx.Err(); err != nil {
		return ctx, th, err
	}
	task, err := e.Store.AgentTask(ctx, x.task.ID)
	if err != nil || task.ThreadID != x.task.ThreadID || task.ProjectID != x.task.ProjectID || task.RootTurnID != x.task.RootTurnID || task.BaseRoot != x.task.BaseRoot || task.ParentRoot != x.task.ParentRoot || task.BaseRevision != x.task.BaseRevision || !unfinishedDelegation(task.State) {
		return ctx, th, errors.New("child task ownership or admission changed")
	}
	parent, err := e.Store.GetThread(ctx, x.task.ThreadID)
	if err != nil || parent.ProjectID != x.task.ProjectID {
		return ctx, th, errors.New("child parent project ownership changed")
	}
	project, err := e.Projects.Get(ctx, x.task.ProjectID)
	if err != nil || project.Missing || !pathutil.SameFolder(project.Root, x.task.BaseRoot) {
		return ctx, th, errors.New("child project boundary unavailable or changed")
	}
	if err = e.validateDelegationParent(ctx, task); err != nil {
		return ctx, th, err
	}
	scope := tools.ScopeFrom(ctx)
	if scope == nil || scope.ProjectID != task.ProjectID || !scope.RestrictedChild || !pathutil.SameFolder(scope.Root, x.workspace.Root) {
		return ctx, th, errors.New("isolated child tool scope unavailable")
	}
	narrowed := *tools.ScopeFrom(currentSelectionScope(ctx, &project))
	limit := func(original, current int) int {
		if original == 0 {
			return current
		}
		if current == 0 || original < current {
			return original
		}
		return current
	}
	narrowed.ComputeVCPUs = limit(scope.ComputeVCPUs, narrowed.ComputeVCPUs)
	narrowed.ComputeMemoryMiB = limit(scope.ComputeMemoryMiB, narrowed.ComputeMemoryMiB)
	narrowed.ComputeDiskMiB = limit(scope.ComputeDiskMiB, narrowed.ComputeDiskMiB)
	rank := func(mode string) int {
		switch normalizedApprovalMode(mode) {
		case "auto_all":
			return 2
		case "auto_workspace":
			return 1
		default:
			return 0
		}
	}
	if rank(parent.ApprovalMode) < rank(scope.ApprovalMode) {
		narrowed.ApprovalMode = normalizedApprovalMode(parent.ApprovalMode)
	}
	th.ApprovalMode = narrowed.ApprovalMode
	th.Channel = parent.Channel
	if x.runtime == nil {
		return ctx, th, errNotRuntimeExecution
	}
	// Re-derive from capabilities with the live project confinement.
	return tools.WithScope(ctx, e.runtimeAgentScope(&narrowed, x)), th, nil
}

// childToolRefusal explains a refused tool call: a runtime agent's authority
// is its capabilities.
func childToolRefusal(ctx context.Context, name string) error {
	return fmt.Errorf("tool %s blocked: it is not granted by this agent's capabilities", name)
}

func (e *Engine) publishTurnEvent(ctx context.Context, threadID, method string, params any) {
	if childExecutionFrom(ctx) != nil {
		return
	}
	e.Bus.Publish(threadID, method, params)
}

func registerExecutionCancellation(ctx context.Context, cancel context.CancelFunc) error {
	if childExecutionFrom(ctx) == nil {
		return nil
	}
	return delegation.RegisterChildCancellation(ctx, cancel)
}
