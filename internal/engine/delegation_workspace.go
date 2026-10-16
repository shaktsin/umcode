package engine

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
)

// activeDelegationParent only inspects an existing parent checkout. Child
// dispatch must never create/retarget the parent's managed coding workspace.
func (e *Engine) activeDelegationParent(ctx context.Context, th protocol.Thread, projectRoot string) (string, error) {
	switch th.WorkspaceMode {
	case "", "local":
		return pathutil.Resolved(projectRoot), nil
	case "worktree":
		ws, err := e.Worktrees.Lookup(ctx, th.ID, projectRoot)
		if err != nil {
			return "", errors.New("delegation requires the existing owned parent worktree; open the coding workspace and commit its inputs before enabling")
		}
		return pathutil.Resolved(ws.Path), nil
	default:
		return "", errors.New("delegation requires an explicit local or existing worktree parent")
	}
}
func (e *Engine) validateDelegationParent(ctx context.Context, t protocol.AgentTask) error {
	if t.ParentRoot == "" {
		return errors.New("delegation parent root was not admitted; explicitly enable a new task")
	}
	th, err := e.Store.GetThread(ctx, t.ThreadID)
	if err != nil {
		return err
	}
	if th.ProjectID != t.ProjectID {
		return errors.New("delegation parent project changed")
	}
	p, err := e.Projects.Get(ctx, t.ProjectID)
	if err != nil {
		return err
	}
	if p.Missing || pathutil.Resolved(p.Root) != t.BaseRoot {
		return errors.New("delegation project root changed")
	}
	root, err := e.activeDelegationParent(ctx, th, p.Root)
	if err != nil {
		return err
	}
	if root != t.ParentRoot {
		return errors.New("delegation active parent checkout or workspace mode changed")
	}
	return nil
}

type managedChildWorkspaces struct{ service *delegation.WorkspaceService }

func (m managedChildWorkspaces) Resolve(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild) (ChildWorkspace, error) {
	w, err := m.service.Prepare(ctx, t, c)
	return ChildWorkspace{ID: w.ID, Root: w.Root, Revision: w.BaseRevision}, err
}
func (m managedChildWorkspaces) Finalize(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild, w ChildWorkspace) (ChildWorkspace, error) {
	r, err := m.service.Snapshot(ctx, t, c, delegation.WorkspaceRef{ID: w.ID, Root: w.Root, BaseRevision: w.Revision})
	return ChildWorkspace{ID: r.ID, Root: r.Root, Revision: r.ResultRevision, ChangedPaths: append([]string{}, r.ChangedPaths...)}, err
}

var _ WorkspaceResolver = managedChildWorkspaces{}
var _ ChildWorkspaceFinalizer = managedChildWorkspaces{}

func privateParentRevision(ctx context.Context, root string) (string, error) {
	b, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "HEAD^{commit}").Output()
	return strings.TrimSpace(string(b)), err
}
