package delegation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
)

type WorkspaceRef struct{ ID, Root, BaseRevision string }
type WorkspaceSnapshot struct {
	WorkspaceRef
	ResultRevision string
	ChangedPaths   []string
}

// ParentValidator checks live project membership and the already existing
// active parent checkout; it must never create or retarget a checkout.
type ParentValidator func(context.Context, protocol.AgentTask) error

type WorkspaceService struct {
	Store          *store.Store
	Manager        *worktree.Manager
	ValidateParent ParentValidator
	mu             sync.Mutex
	// taskLocks serialises runtime integration per task (guarded by mu).
	taskLocks map[string]*taskLock
	// events receives runtime agent and integration changes (WithEvents).
	events func(AgentEvent)
	// eventsMu guards integrationDirty: the tasks whose integration changed
	// under their task lock and not yet announced (see lockTask).
	eventsMu         sync.Mutex
	integrationDirty map[string]bool
}

// AgentEvent reports a persisted runtime change made by the service: Agent
// is the agent's new state (nil if only the integration changed);
// IntegrationChanged means the task's revisions or integration head changed
// and the integration status should be read again. ThreadID is the task's
// parent chat when known.
type AgentEvent struct {
	ThreadID, TaskID   string
	Agent              *protocol.RuntimeAgent
	IntegrationChanged bool
}

// WithEvents registers fn for the service's runtime changes. Agent events are
// delivered synchronously after the write, possibly with service locks held,
// so fn must not call back into the service for them; integration events
// are delivered after the task's integration lock is released, so fn may
// read the integration status. Call it before the service is used.
func (s *WorkspaceService) WithEvents(fn func(AgentEvent)) { s.events = fn }

// integrationChanged marks that the task's integration changed; the caller
// holds the task lock, whose release announces it once.
func (s *WorkspaceService) integrationChanged(taskID string) {
	if s.events == nil {
		return
	}
	s.eventsMu.Lock()
	if s.integrationDirty == nil {
		s.integrationDirty = map[string]bool{}
	}
	s.integrationDirty[taskID] = true
	s.eventsMu.Unlock()
}

// takeIntegrationChanged clears and returns the task's mark.
func (s *WorkspaceService) takeIntegrationChanged(taskID string) bool {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	dirty := s.integrationDirty[taskID]
	delete(s.integrationDirty, taskID)
	return dirty
}

// emitIntegration announces a task's integration change.
func (s *WorkspaceService) emitIntegration(taskID string) {
	if s.events == nil {
		return
	}
	ev := AgentEvent{TaskID: taskID, IntegrationChanged: true}
	if t, err := s.Store.AgentTask(context.Background(), taskID); err == nil {
		ev.ThreadID = t.ThreadID
	}
	s.events(ev)
}

func NewWorkspaceService(s *store.Store, m *worktree.Manager, parent ParentValidator) *WorkspaceService {
	return &WorkspaceService{Store: s, Manager: m, ValidateParent: parent}
}
func ref(w store.AgentWorkspace) WorkspaceRef {
	return WorkspaceRef{ID: w.ID, Root: w.Root, BaseRevision: w.BaseRevision}
}
func (s *WorkspaceService) task(ctx context.Context, supplied protocol.AgentTask) (protocol.AgentTask, error) {
	t, err := s.Store.AgentTask(ctx, supplied.ID)
	if err != nil {
		return t, err
	}
	if t.ThreadID != supplied.ThreadID || t.ProjectID != supplied.ProjectID || t.RootTurnID != supplied.RootTurnID || t.BaseRoot != supplied.BaseRoot || t.ParentRoot != supplied.ParentRoot || t.BaseRevision != supplied.BaseRevision {
		return t, errors.New("workspace admission identity changed")
	}
	if t.BaseRoot == "" || t.ParentRoot == "" || t.BaseRevision == "" {
		return t, errors.New("workspace requires explicit committed parent admission; enable a new delegation task")
	}
	if s.ValidateParent != nil {
		err = s.ValidateParent(ctx, t)
	}
	return t, err
}

// ownedCheckout loads (or first records) the planned workspace w, attaches it
// to its owner and ensures its private checkout of the task's base revision
// with ensure. The checkout must lie outside the parent and registered
// project roots.
func (s *WorkspaceService) ownedCheckout(ctx context.Context, t protocol.AgentTask, planned store.AgentWorkspace, attach func(id string) error, ensure func(id string) (worktree.Workspace, error)) (WorkspaceRef, error) {
	id := planned.ID
	if pathutil.Within(t.ParentRoot, planned.Root) || pathutil.Within(planned.Root, t.ParentRoot) || pathutil.Within(t.BaseRoot, planned.Root) || pathutil.Within(planned.Root, t.BaseRoot) {
		return WorkspaceRef{}, errors.New("owned child checkout must be outside parent and registered project roots")
	}
	w, err := s.Store.AgentWorkspace(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		w = planned
		if err = s.Store.SaveAgentWorkspace(ctx, w); err != nil {
			return WorkspaceRef{}, err
		}
	} else if err != nil {
		return WorkspaceRef{}, err
	}
	if w.Cleaned {
		return ref(w), errors.New("workspace was cleaned after verified integration")
	}
	if err = attach(w.ID); err != nil {
		return ref(w), err
	}
	ws, err := ensure(id)
	if err != nil {
		return ref(w), err
	}
	if ws.Path != w.Root {
		return ref(w), errors.New("owned workspace root changed")
	}
	if err = validatePrivate(ctx, w.Root); err != nil {
		return ref(w), err
	}
	return ref(w), nil
}

// snapshotOwned validates a stopped owned workspace and returns its result:
// a read-only (reviewer kind) checkout must still be its exact committed
// revision; any other is committed privately unless recorded says its result
// revision was already recorded by its owner.
func (s *WorkspaceService) snapshotOwned(ctx context.Context, w store.AgentWorkspace, recorded bool) (WorkspaceSnapshot, error) {
	source := w.ParentRoot
	if _, err := s.Manager.Lookup(ctx, w.ID, source); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if err := validatePrivate(ctx, w.Root); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if w.Kind == "reviewer" {
		if err := worktree.ValidateCommitted(ctx, w.Root, w.ResultRevision); err != nil {
			return WorkspaceSnapshot{}, err
		}
		return WorkspaceSnapshot{WorkspaceRef: ref(w), ResultRevision: w.ResultRevision}, nil
	}
	if err := worktree.ValidateIndexFlags(ctx, w.Root); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if err := worktree.ValidatePaths(ctx, w.Root); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if recorded && w.ResultRevision != "" {
		if err := worktree.ValidateCommitted(ctx, w.Root, w.ResultRevision); err != nil {
			return WorkspaceSnapshot{}, err
		}
		return WorkspaceSnapshot{WorkspaceRef: ref(w), ResultRevision: w.ResultRevision, ChangedPaths: w.ChangedPaths}, nil
	}
	if _, err := privateGit(ctx, w.Root, "add", "--all", "--"); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if _, err := privateGit(ctx, w.Root, "-c", "user.name=UMCode", "-c", "user.email=umcode@localhost", "commit", "--quiet", "--allow-empty", "-m", "UMCode delegated result"); err != nil {
		return WorkspaceSnapshot{}, err
	}
	revision, err := privateGit(ctx, w.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	revision = strings.TrimSpace(revision)
	paths, err := privateGit(ctx, w.Root, "diff", "--name-only", "-z", w.BaseRevision, revision, "--")
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	tree, err := privateGit(ctx, w.Root, "rev-parse", revision+"^{tree}")
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	w.ResultTree = strings.TrimSpace(tree)
	w.ResultRevision = strings.TrimSpace(revision)
	w.ChangedPaths = nulPaths(paths)
	if err = worktree.ValidateCommitted(ctx, w.Root, w.ResultRevision); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if err = s.Store.SaveAgentWorkspace(ctx, w); err != nil {
		return WorkspaceSnapshot{}, err
	}
	return WorkspaceSnapshot{WorkspaceRef: ref(w), ResultRevision: w.ResultRevision, ChangedPaths: w.ChangedPaths}, nil
}
func nulPaths(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parentAuthority is the storage-only part of integrationAuthority that does
// not depend on the integrated agent: the task's thread, project and parent
// workspace mode still admit its parent checkout. Shared by runtime agents.
func (s *WorkspaceService) parentAuthority(ctx context.Context, t protocol.AgentTask) error {
	th, err := s.Store.GetThread(ctx, t.ThreadID)
	if err != nil {
		return err
	}
	if th.ProjectID != t.ProjectID {
		return errors.New("integration parent project changed")
	}
	if t.Activation == protocol.AgentActivationPlanApproval {
		workspace, err := s.Store.AgentTaskWorkspace(ctx, t.ID)
		if err != nil {
			return err
		}
		if workspace.ExecutionRoot != t.ParentRoot || workspace.SnapshotCommit != t.BaseRevision || workspace.State != "active" || workspace.SourceRoot == "" ||
			pathutil.Within(workspace.SourceRoot, t.ParentRoot) || pathutil.Within(t.ParentRoot, workspace.SourceRoot) {
			return errors.New("integration private parent identity changed")
		}
		switch th.WorkspaceMode {
		case "", "local":
			if workspace.SourceRoot != t.BaseRoot {
				return errors.New("integration source checkout changed")
			}
		case "worktree":
			if workspace.SourceRoot != s.Manager.Path(th.ID) {
				return errors.New("integration source worktree changed")
			}
		default:
			return errors.New("integration source workspace mode unsupported")
		}
		if t.ProjectID != "" {
			p, err := s.Store.GetProject(ctx, t.ProjectID)
			if err != nil {
				return err
			}
			if pathutil.Resolved(p.Root) != t.BaseRoot {
				return errors.New("integration project root changed")
			}
		}
		return nil
	}
	switch th.WorkspaceMode {
	case "", "local":
		if t.ParentRoot != t.BaseRoot {
			return errors.New("integration parent workspace mode changed")
		}
	case "worktree":
		if t.ParentRoot != s.Manager.Path(th.ID) {
			return errors.New("integration parent checkout changed")
		}
	default:
		return errors.New("integration parent workspace mode unsupported")
	}
	if t.ProjectID != "" {
		p, err := s.Store.GetProject(ctx, t.ProjectID)
		if err != nil {
			return err
		}
		if pathutil.Resolved(p.Root) != t.BaseRoot {
			return errors.New("integration project root changed")
		}
	}
	return nil
}

func (s *WorkspaceService) discardVerified(ctx context.Context, w store.AgentWorkspace) error {
	if _, err := os.Lstat(w.Root); err == nil {
		if err = validatePrivate(ctx, w.Root); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) || !w.Cleaning {
		return errors.New("workspace disappeared without a durable cleanup intent")
	}
	if !w.Cleaning {
		w.Cleaning = true
		if err := s.Store.SaveAgentWorkspace(ctx, w); err != nil {
			return err
		}
	}
	if err := s.Manager.DiscardCommitted(ctx, w.ID, w.ParentRoot, w.ResultRevision); err != nil {
		return err
	}
	w.Cleaned = true
	return s.Store.SaveAgentWorkspace(ctx, w)
}

// privateGit and validatePrivate delegate to the single hardened runner and
// metadata validator in internal/worktree.
func privateGit(ctx context.Context, root string, args ...string) (string, error) {
	out, stderr, err := worktree.HardenedGit(ctx, root, nil, args...)
	if err != nil {
		return "", fmt.Errorf("workspace git %s: %w: %s%s", strings.Join(args, " "), err, out, stderr)
	}
	return string(out), nil
}
func validatePrivate(ctx context.Context, root string) error {
	return worktree.ValidateOwnedMetadata(ctx, root)
}

// runtimeBaseSource marks a runtime agent's read-only checkout of the task's
// base revision (nothing integrated yet); runtimeHeadSource marks one of the
// integrated head (a view commit on top of the base, see
// worktree.ApplyRuntimeView).
const (
	runtimeBaseSource = "runtime_base"
	runtimeHeadSource = "runtime_head"
)

// IntegrationApplyingText refuses a new agent workspace while a revision of
// the task is applying: the project checkout may be mid-write.
const IntegrationApplyingText = "an integration is being applied to the project checkout"

// IntegrationApplyingAdvice is the refusal for rev, a revision that is
// applying. Under the task lock an applying revision is an interrupted or
// blocked apply, which waiting never settles, so it tells the lead how to
// settle it: accept again, or resolved again for a manual resolution.
func IntegrationApplyingAdvice(rev store.AgentRevision) string {
	if rev.Reason == manualResolution {
		return fmt.Sprintf("%s (agent %s's manual resolution is being recorded); if no agent.integrate call is running, it was interrupted: call agent.integrate {action:\"resolved\", agent:%q} again to record it, and if it stays blocked check agent.integrate {action:\"status\"}", IntegrationApplyingText, rev.AgentID, rev.AgentID)
	}
	return fmt.Sprintf("%s (agent %s's revision is applying); if no accept is running, its apply was interrupted: accept agent %s again (agent.integrate {action:\"accept\", agent:%q}) to settle it, and if it stays blocked check agent.integrate {action:\"status\"}", IntegrationApplyingText, rev.AgentID, rev.AgentID, rev.AgentID)
}

// PrepareRuntime gives a runtime agent its own private checkout. A
// write-capable agent gets an isolated writable checkout of the task's
// committed base revision (snapshotted by SnapshotRuntime; its revision is
// integrated onto the current head like any other). Any other agent gets a
// read-only view (PrepareRuntimeView). The workspace is recorded on the agent
// (RuntimeAgent.WorkspaceID). Runtime agents are inert to the legacy
// delegation paths: their mirror rows are hidden from AgentChild.
//
// Workspaces can be prepared after revisions were integrated: the project
// checkout must then be HEAD == base with its index exactly the recorded
// integration head (worktree.ParentStateForRuntime), and it is only read.
//
// Serialisation: preparation holds the task's integration lock (taken before
// the service lock, the order CleanupRuntime uses), the lock IntegrateRuntime,
// ResolveRuntimeManually and reconcile hold for their whole run. So the head
// read here, the parent check and the clone or view never interleave with an
// accept: a preparation started during an accept waits for it and then sees
// the new head. A revision still applying under the lock is an interrupted
// apply; preparation is refused until it is reconciled.
func (s *WorkspaceService) PrepareRuntime(ctx context.Context, task protocol.AgentTask, agent protocol.RuntimeAgent, writable bool) (WorkspaceRef, error) {
	defer s.lockTask(task.ID)()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.task(ctx, task)
	if err != nil {
		return WorkspaceRef{}, err
	}
	a, err := s.Store.RuntimeAgent(ctx, t.ID, agent.ID)
	if err != nil {
		return WorkspaceRef{}, err
	}
	if agent.TaskID != t.ID || a.Capabilities != agent.Capabilities {
		return WorkspaceRef{}, errors.New("agent workspace ownership changed")
	}
	return s.prepareRuntimeLocked(ctx, t, a, writable)
}

// PrepareRuntimeView gives a read-only runtime agent (files read or none) its
// private view: before any accept a checkout of the base revision; after one,
// a checkout of the integrated head, built by applying the trusted parent's
// base→head diff inside the private checkout and verified to be exactly the
// head tree before the agent starts (worktree.ApplyRuntimeView). Reviewers
// therefore see the integrated result. Same locking as PrepareRuntime.
func (s *WorkspaceService) PrepareRuntimeView(ctx context.Context, taskID, agentID string) (WorkspaceRef, error) {
	defer s.lockTask(taskID)()
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.Store.AgentTask(ctx, taskID)
	if err != nil {
		return WorkspaceRef{}, err
	}
	t, err := s.task(ctx, stored)
	if err != nil {
		return WorkspaceRef{}, err
	}
	a, err := s.Store.RuntimeAgent(ctx, t.ID, agentID)
	if err != nil {
		return WorkspaceRef{}, err
	}
	return s.prepareRuntimeLocked(ctx, t, a, false)
}

// prepareRuntimeLocked is PrepareRuntime for a caller holding the task lock
// and the service lock.
func (s *WorkspaceService) prepareRuntimeLocked(ctx context.Context, t protocol.AgentTask, a protocol.RuntimeAgent, writable bool) (WorkspaceRef, error) {
	if !writable && a.Capabilities.Files == protocol.FilesReadWrite {
		return WorkspaceRef{}, fmt.Errorf("agent %s can write files; it gets a writable checkout, not a read-only view", a.ID)
	}
	revs, err := s.Store.AgentRevisions(ctx, t.ID)
	if err != nil {
		return WorkspaceRef{}, err
	}
	for _, r := range revs {
		if r.State == store.RevisionApplying {
			return WorkspaceRef{}, errors.New(IntegrationApplyingAdvice(r))
		}
	}
	h, err := s.head(ctx, t, false)
	if err != nil {
		return WorkspaceRef{}, err
	}
	// Refuse an unusable project checkout before any workspace identity is
	// recorded, so a refused first preparation leaves nothing stale behind
	// (EnsureRuntime checks again under the manager lock).
	if err = worktree.ParentStateForRuntime(ctx, t.ParentRoot, t.BaseRevision, h.HeadTree); err != nil {
		return WorkspaceRef{}, err
	}
	// A view shows the recorded integration head (built from its objects in
	// the parent), not whatever the parent currently holds: the parent check
	// also accepts an index reset to the base tree, and a parent whose
	// integration was discarded with reset --hard still gets the recorded
	// head while its objects exist (after gc --prune the view fails closed).
	// The next accept refuses such a parent anyway.
	integrated := h.AppliedCount > 0
	id := "agent-" + t.ID + "-" + a.ID
	planned := store.AgentWorkspace{ID: id, TaskID: t.ID, ChildID: a.ID, Kind: "implementer", Root: s.Manager.Path(id), ParentRoot: t.ParentRoot, BaseRevision: t.BaseRevision}
	viewCommit := ""
	if !writable {
		id = "agent-view-" + t.ID + "-" + a.ID
		planned = store.AgentWorkspace{ID: id, TaskID: t.ID, ChildID: a.ID, Kind: "reviewer", SourceID: runtimeBaseSource, Root: s.Manager.Path(id), ParentRoot: t.ParentRoot, BaseRevision: t.BaseRevision, ResultRevision: t.BaseRevision}
		if integrated {
			if viewCommit, err = worktree.RuntimeViewCommit(ctx, t.ParentRoot, t.BaseRevision, h.HeadTree); err != nil {
				return WorkspaceRef{}, err
			}
			planned.SourceID, planned.ResultRevision, planned.ResultTree = runtimeHeadSource, viewCommit, h.HeadTree
		}
	}
	if existing, readErr := s.Store.AgentWorkspace(ctx, id); readErr == nil {
		if existing.TaskID != t.ID || existing.ChildID != a.ID || existing.Kind != planned.Kind || (writable && existing.SourceID != planned.SourceID) {
			return ref(existing), errors.New("agent workspace ownership changed")
		}
		if !writable && (existing.SourceID != planned.SourceID || existing.ResultRevision != planned.ResultRevision) {
			_, statErr := os.Lstat(s.Manager.Path(id))
			// A view whose checkout was never created (its first
			// preparation failed after the identity was recorded) is
			// re-planned for the current head; the store keeps SourceID
			// fixed, so only a head-to-head re-plan is possible.
			if !errors.Is(statErr, os.ErrNotExist) || existing.SourceID != planned.SourceID || existing.Cleaned || existing.Cleaning {
				return ref(existing), fmt.Errorf("agent %s's view was prepared for an earlier integrated state; cancel it and spawn a new agent", a.ID)
			}
			if err = s.Store.SaveAgentWorkspace(ctx, planned); err != nil {
				return ref(existing), err
			}
		}
	} else if !errors.Is(readErr, store.ErrNotFound) {
		return WorkspaceRef{}, readErr
	}
	r, err := s.ownedCheckout(ctx, t, planned, func(id string) error { return s.attachRuntime(ctx, t, a.ID, id) },
		func(id string) (worktree.Workspace, error) {
			return s.Manager.EnsureRuntime(ctx, id, t.ParentRoot, t.BaseRevision, h.HeadTree)
		})
	if err != nil || writable {
		return r, err
	}
	if !integrated {
		return r, worktree.ValidateCommitted(ctx, r.Root, t.BaseRevision)
	}
	if err = s.Manager.ApplyRuntimeView(ctx, id, t.ParentRoot, t.BaseRevision, h.HeadTree, viewCommit); err != nil {
		return r, fmt.Errorf("agent %s: integrated view not usable: %w", a.ID, err)
	}
	if err = validatePrivate(ctx, r.Root); err != nil {
		return r, err
	}
	return r, worktree.ValidateCommitted(ctx, r.Root, viewCommit)
}

// attachRuntime records workspace id on the agent; an agent never changes
// workspace once it owns one.
func (s *WorkspaceService) attachRuntime(ctx context.Context, t protocol.AgentTask, agentID, id string) error {
	for i := 0; i < 8; i++ {
		latest, err := s.Store.RuntimeAgent(ctx, t.ID, agentID)
		if err != nil {
			return err
		}
		if latest.WorkspaceID != "" && latest.WorkspaceID != id {
			return errors.New("agent already owns a different workspace")
		}
		if latest.WorkspaceID == id {
			return nil
		}
		latest.WorkspaceID = id
		err = s.Store.UpdateRuntimeAgent(ctx, latest, latest.Version)
		if errors.Is(err, store.ErrAgentConflict) {
			continue
		}
		if err == nil && s.events != nil {
			latest.Version++
			s.events(AgentEvent{ThreadID: t.ThreadID, TaskID: t.ID, Agent: &latest})
		}
		return err
	}
	return store.ErrAgentConflict
}

// DiscardRuntimeView removes the read-only view checkout of a stopped runtime
// agent (completed, failed or cancelled). It refuses anything but a runtime
// view owned by that agent: a write-capable agent's workspace is kept for
// integration and is never touched. The checkout must still be exactly the
// revision it was prepared at (the base, or its integrated view commit) and
// clean (DiscardCommitted), else it is retained.
func (s *WorkspaceService) DiscardRuntimeView(ctx context.Context, taskID, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.Store.RuntimeAgent(ctx, taskID, agentID)
	if err != nil {
		return err
	}
	if a.WorkspaceID == "" {
		return nil
	}
	w, err := s.Store.AgentWorkspace(ctx, a.WorkspaceID)
	if err != nil {
		return err
	}
	if w.TaskID != taskID || w.ChildID != agentID || w.Kind != "reviewer" || (w.SourceID != runtimeBaseSource && w.SourceID != runtimeHeadSource) {
		return errors.New("workspace is not a runtime view checkout of this agent")
	}
	if w.Cleaned {
		return nil
	}
	return s.discardVerified(ctx, w)
}

// SnapshotRuntime is SnapshotOwned for a stopped runtime agent: a writable
// checkout is committed privately and reports its changed paths; a read-only
// checkout must still be exactly the revision it was prepared at (the base
// or its integrated view commit) and reports none.
func (s *WorkspaceService) SnapshotRuntime(ctx context.Context, task protocol.AgentTask, agent protocol.RuntimeAgent, workspace WorkspaceRef) (WorkspaceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.task(ctx, task)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	a, err := s.Store.RuntimeAgent(ctx, t.ID, agent.ID)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	w, err := s.Store.AgentWorkspace(ctx, workspace.ID)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	if w.TaskID != t.ID || w.ChildID != a.ID || w.Root != workspace.Root || a.WorkspaceID != w.ID || w.Cleaned {
		return WorkspaceSnapshot{}, errors.New("finalization workspace identity changed")
	}
	return s.snapshotOwned(ctx, w, a.Revision != "")
}
