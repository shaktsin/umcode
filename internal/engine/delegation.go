package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/worktree"
)

const delegationSkillID = "superpowers:subagent-driven-development"

type delegationToken struct {
	ThreadID, ProjectID, Root, ParentRoot, Revision string
	ExpiresAt                                       time.Time
}
type delegationAdmission struct {
	delegationToken
	Activation protocol.AgentActivation
	Task       protocol.AgentTask
}
type delegationAuthorizationKey struct{}
type delegationTaskKey struct{}
type delegationChildKey struct{}

// WithDelegationChild marks internal child execution. Child contexts never
// carry parent admission authority, even when derived from the parent context.
func WithDelegationChild(ctx context.Context) context.Context {
	return context.WithValue(ctx, delegationChildKey{}, true)
}
func delegationChild(ctx context.Context) bool {
	child, _ := ctx.Value(delegationChildKey{}).(bool)
	return child
}
func withDelegationTask(ctx context.Context, task protocol.AgentTask) context.Context {
	return context.WithValue(ctx, delegationTaskKey{}, task)
}

// DelegationTaskFromContext exposes the admitted parent task to runtime/catalog
// adapters. It is absent for ordinary turns and all child execution.
func DelegationTaskFromContext(ctx context.Context) (protocol.AgentTask, bool) {
	if delegationChild(ctx) {
		return protocol.AgentTask{}, false
	}
	task, ok := ctx.Value(delegationTaskKey{}).(protocol.AgentTask)
	return task, ok && task.ID != ""
}

func (e *Engine) delegationBase(ctx context.Context, threadID string) (delegationToken, error) {
	th, err := e.Store.GetThread(ctx, threadID)
	if err != nil {
		return delegationToken{}, err
	}
	if th.ProjectID == "" {
		return delegationToken{}, protocol.Errorf(protocol.CodeInvalidParams, "delegation requires a committed Git project")
	}
	proj, err := e.Projects.Get(ctx, th.ProjectID)
	if err != nil {
		return delegationToken{}, err
	}
	if proj.Missing {
		return delegationToken{}, protocol.Errorf(protocol.CodeInvalidParams, "delegation project folder is missing")
	}
	root, err := e.activeDelegationParent(ctx, th, proj.Root)
	if err != nil {
		return delegationToken{}, err
	}
	if err = worktree.ValidateCommitted(ctx, root, ""); err != nil {
		return delegationToken{}, err
	}
	revision, err := privateParentRevision(ctx, root)
	if err != nil {
		return delegationToken{}, err
	}
	return delegationToken{ThreadID: th.ID, ProjectID: th.ProjectID, Root: pathutil.Resolved(proj.Root), ParentRoot: root, Revision: revision}, nil
}
func (e *Engine) delegationOpen() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shuttingDown {
		return protocol.Errorf(protocol.CodeConflict, "engine is shutting down")
	}
	return nil
}

// PrepareDelegation is the app's explicit, pre-submit activation action. It
// returns a one-use chat/project/root/revision-bound token, not a premature task.
func (e *Engine) PrepareDelegation(ctx context.Context, threadID string) (protocol.DelegationEnableResult, error) {
	if delegationChild(ctx) {
		return protocol.DelegationEnableResult{}, protocol.Errorf(protocol.CodeInvalidRequest, "children cannot enable delegation")
	}
	e.delegationMu.Lock()
	defer e.delegationMu.Unlock()
	if err := e.delegationOpen(); err != nil {
		return protocol.DelegationEnableResult{}, err
	}
	base, err := e.delegationBase(ctx, threadID)
	if err != nil {
		return protocol.DelegationEnableResult{}, err
	}
	if _, err = e.Store.ActiveAgentTask(ctx, base.ThreadID, base.ProjectID); err == nil {
		return protocol.DelegationEnableResult{}, protocol.Errorf(protocol.CodeConflict, "this chat already has an unfinished delegation task; continue it with its task ID")
	} else if !errors.Is(err, store.ErrNotFound) {
		return protocol.DelegationEnableResult{}, err
	}
	var bytes [32]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return protocol.DelegationEnableResult{}, err
	}
	token := hex.EncodeToString(bytes[:])
	base.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shuttingDown {
		return protocol.DelegationEnableResult{}, protocol.Errorf(protocol.CodeConflict, "engine is shutting down")
	}
	if e.delegationTokens == nil {
		e.delegationTokens = map[string]delegationToken{}
	}
	for key, entry := range e.delegationTokens {
		if !time.Now().Before(entry.ExpiresAt) {
			delete(e.delegationTokens, key)
		}
	}
	e.delegationTokens[token] = base
	return protocol.DelegationEnableResult{ActivationToken: token, ExpiresAt: base.ExpiresAt}, nil
}

// submittedDelegationSkill only examines the current submitted user command,
// resolved through the installed plugin snapshot, never matching prose/history.
func (e *Engine) submittedDelegationSkill(ctx context.Context, text string) bool {
	name, ok := skills.Invocation(text)
	if !ok || e.Skills == nil {
		return false
	}
	skill, ok := e.Skills.GetContext(ctx, name)
	return ok && skill.Error == "" && skill.Name == delegationSkillID
}
func (e *Engine) prepareDelegationAdmission(ctx context.Context, p protocol.TurnStartParams) (*delegationAdmission, error) {
	if err := e.delegationOpen(); err != nil {
		return nil, err
	}
	if delegationChild(ctx) {
		if p.DelegationTaskID != "" || p.DelegationActivationToken != "" {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "children cannot activate or inherit delegation")
		}
		return nil, nil
	}
	if p.DelegationTaskID != "" && p.DelegationActivationToken != "" {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "provide a delegation task ID or activation token, not both")
	}
	var token delegationToken
	if p.DelegationActivationToken != "" {
		e.mu.Lock()
		token = e.delegationTokens[p.DelegationActivationToken]
		e.mu.Unlock()
		if token.ThreadID != p.ThreadID || !time.Now().Before(token.ExpiresAt) {
			return nil, protocol.Errorf(protocol.CodeInvalidParams, "delegation activation token is invalid or expired")
		}
	}
	skill := false
	if _, explicit := skills.Invocation(p.Text); explicit && p.DelegationTaskID == "" && p.DelegationActivationToken == "" && e.Plugins != nil {
		th, err := e.Store.GetThread(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		snapshot, err := e.Plugins.Acquire(ctx, th.ProjectID)
		if err != nil {
			return nil, err
		}
		skill = e.submittedDelegationSkill(skills.WithSnapshot(ctx, snapshot.SkillSnapshot()), p.Text)
		snapshot.Release()
	}
	if p.DelegationTaskID == "" && p.DelegationActivationToken == "" && !skill {
		return nil, nil
	}
	base, err := e.delegationBase(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if p.DelegationTaskID != "" {
		task, err := e.Store.AgentTask(ctx, p.DelegationTaskID)
		if err != nil {
			return nil, err
		}
		if task.ThreadID != base.ThreadID || task.ProjectID != base.ProjectID {
			return nil, protocol.Errorf(protocol.CodeInvalidParams, "delegation task does not belong to this chat/project")
		}
		if !unfinishedDelegation(task.State) {
			return nil, protocol.Errorf(protocol.CodeInvalidParams, "delegation task is finished or interrupted; explicitly enable a new task")
		}
		if task.BaseRoot != base.Root || task.ParentRoot != base.ParentRoot {
			return nil, protocol.Errorf(protocol.CodeConflict, "delegation project root changed")
		}
		if task.BaseRevision != base.Revision {
			return nil, protocol.Errorf(protocol.CodeConflict, "delegation project revision changed")
		}
		return &delegationAdmission{delegationToken: base, Task: task}, nil
	}
	if token.ThreadID != "" && (token.ProjectID != base.ProjectID || token.Root != base.Root || token.ParentRoot != base.ParentRoot || token.Revision != base.Revision) {
		return nil, protocol.Errorf(protocol.CodeConflict, "delegation activation project or revision changed; enable again")
	}
	if _, err = e.Store.ActiveAgentTask(ctx, base.ThreadID, base.ProjectID); err == nil {
		return nil, protocol.Errorf(protocol.CodeConflict, "this chat already has an unfinished delegation task")
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	activation := protocol.AgentActivationExplicitUser
	if skill {
		activation = protocol.AgentActivationInstalledSkill
	}
	return &delegationAdmission{delegationToken: base, Activation: activation}, nil
}
func unfinishedDelegation(state protocol.AgentState) bool {
	return state == protocol.AgentStateQueued || state == protocol.AgentStateRunning || state == protocol.AgentStateWaitingApproval
}

// EnableDelegation persists permission only for an engine-validated admission
// after the actual root turn exists. A bare engine/tool caller has no authority.
func (e *Engine) EnableDelegation(ctx context.Context, threadID, rootTurnID string) (protocol.AgentTask, error) {
	admission, ok := ctx.Value(delegationAuthorizationKey{}).(*delegationAdmission)
	if delegationChild(ctx) || !ok || admission.ThreadID != threadID || admission.Activation == "" {
		return protocol.AgentTask{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit delegation admission is required")
	}
	if err := e.delegationOpen(); err != nil {
		return protocol.AgentTask{}, err
	}
	base, err := e.delegationBase(ctx, threadID)
	if err != nil {
		return protocol.AgentTask{}, err
	}
	if base.ProjectID != admission.ProjectID || base.Root != admission.Root || base.ParentRoot != admission.ParentRoot || base.Revision != admission.Revision {
		return protocol.AgentTask{}, protocol.Errorf(protocol.CodeConflict, "delegation project changed during admission")
	}
	now := time.Now().UTC()
	task := protocol.AgentTask{
		ID: store.NewID("agtask"), ThreadID: threadID, RootTurnID: rootTurnID,
		Activation: admission.Activation, State: protocol.AgentStateRunning,
		BaseRevision: base.Revision, BaseRoot: base.Root, ParentRoot: base.ParentRoot, ProjectID: base.ProjectID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err = e.Store.CreateAgentTask(ctx, task); err != nil {
		return protocol.AgentTask{}, err
	}
	return task, nil
}

func isDelegationTool(name string) bool {
	name = tools.FromWire(name)
	return name != "agent.review_report" && strings.HasPrefix(name, "agent.") || name == "delegation.enable"
}

// CancelDelegation remains available when a project folder is unavailable, so
// recovering a stopped task never depends on re-admitting its filesystem.
func (e *Engine) CancelDelegation(ctx context.Context, p protocol.DelegationCancelParams) (protocol.DelegationCancelResult, error) {
	var result protocol.DelegationCancelResult
	if delegationChild(ctx) || p.ThreadID == "" || p.TaskID == "" {
		return result, protocol.Errorf(protocol.CodeInvalidParams, "owning parent chat and task required")
	}
	task, err := e.Store.AgentTask(ctx, p.TaskID)
	if err != nil {
		return result, err
	}
	parent, err := e.Store.GetThread(ctx, p.ThreadID)
	if err != nil {
		return result, err
	}
	if task.ThreadID != parent.ID {
		return result, protocol.Errorf(protocol.CodeInvalidParams, "delegation task belongs to another chat")
	}
	if p.ChildID != "" {
		if _, err = e.Store.AgentChild(ctx, task.ID, p.ChildID); err != nil {
			return result, err
		}
		err = e.DelegationCoordinator.Cancel(ctx, task.ID, p.ChildID)
	} else {
		err = e.DelegationCoordinator.CancelTask(ctx, task.ID)
	}
	if err != nil {
		return result, err
	}
	result.Task, err = e.Store.AgentTask(ctx, task.ID)
	if err != nil {
		return result, err
	}
	result.Children, err = e.DelegationCoordinator.List(ctx, task.ID)
	if err == nil {
		e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: task.ID, Task: &result.Task})
	}
	return result, err
}

// authorizeDelegationTool revalidates the admitted context against durable
// chat/project ownership. Arguments never select a task or grant admission.
// Terminal tasks remain readable; the coordinator admits active mutations.
func (e *Engine) authorizeDelegationTool(ctx context.Context) (protocol.AgentTask, error) {
	admitted, ok := DelegationTaskFromContext(ctx)
	scope := tools.ScopeFrom(ctx)
	if !ok || scope == nil || scope.ThreadID != admitted.ThreadID || scope.ProjectID != admitted.ProjectID {
		return protocol.AgentTask{}, errors.New("delegation requires an admitted parent task")
	}
	task, err := e.Store.AgentTask(ctx, admitted.ID)
	if err != nil {
		return task, err
	}
	if task.ThreadID != admitted.ThreadID || task.ProjectID != admitted.ProjectID || task.RootTurnID != admitted.RootTurnID || task.BaseRoot != admitted.BaseRoot || task.ParentRoot != admitted.ParentRoot || task.BaseRevision != admitted.BaseRevision || task.Activation != admitted.Activation {
		return task, errors.New("delegation task ownership changed")
	}
	thread, err := e.Store.GetThread(ctx, task.ThreadID)
	if err != nil {
		return task, err
	}
	if thread.ProjectID != task.ProjectID {
		return task, errors.New("delegation parent project changed")
	}
	project, err := e.Projects.Get(ctx, task.ProjectID)
	if err != nil {
		return task, err
	}
	if project.Missing || pathutil.Resolved(project.Root) != task.BaseRoot || pathutil.Resolved(scope.Root) != task.ParentRoot {
		return task, errors.New("delegation project root changed")
	}
	if err = e.validateDelegationParent(ctx, task); err != nil {
		return task, err
	}
	return task, nil
}
