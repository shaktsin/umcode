package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
)

// RuntimeIntegration is the outcome of one lead accept (or manual resolution)
// of a runtime agent's revision. Options is non-empty only for a conflict.
type RuntimeIntegration struct {
	Revision          store.AgentRevision
	HeadTree          string
	NeedsVerification bool
	Options           []string
}

// RuntimeIntegrationStatus summarises a task's integration: the integrated
// head tree (the base tree before any apply), how many revisions moved it,
// every revision, whether the head's workflow checks all passed, and whether
// every revision is applied or dropped.
type RuntimeIntegrationStatus struct {
	HeadTree     string
	AppliedCount int
	Revisions    []store.AgentRevision
	Verified     bool
	Settled      bool
}

// conflictOptions are the lead's choices for a conflicted revision: drop it,
// declare it resolved after fixing the parent by hand, or spawn a new agent.
func conflictOptions() []string { return []string{"drop", "resolved", "new_agent"} }

// manualResolution marks a revision the lead resolved by hand in the parent.
const manualResolution = "resolved manually by the lead"

// runtimeIntegrateHook lets tests act between integration steps. Nil in
// production.
var runtimeIntegrateHook func(stage string)

type taskLock struct {
	mu   sync.Mutex
	refs int
}

// lockTask takes the task's integration lock and returns its release. Idle
// entries are pruned, so the map only holds tasks with an operation running.
func (s *WorkspaceService) lockTask(taskID string) func() {
	s.mu.Lock()
	if s.taskLocks == nil {
		s.taskLocks = map[string]*taskLock{}
	}
	l := s.taskLocks[taskID]
	if l == nil {
		l = &taskLock{}
		s.taskLocks[taskID] = l
	}
	l.refs++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.taskLocks, taskID)
		}
		s.mu.Unlock()
	}
}

// loadTask reads the task and revalidates its parent admission.
func (s *WorkspaceService) loadTask(ctx context.Context, taskID string, requireActive bool) (protocol.AgentTask, error) {
	t, err := s.Store.AgentTask(ctx, taskID)
	if err != nil {
		return t, err
	}
	if t, err = s.task(ctx, t); err != nil {
		return t, err
	}
	if requireActive && !active(t.State) {
		return t, fmt.Errorf("task %s is %s; integration requires an active task", t.ID, t.State)
	}
	return t, nil
}

// head returns the task's integration head. Before the first accept it is the
// base tree (Version 0); create records it.
func (s *WorkspaceService) head(ctx context.Context, t protocol.AgentTask, create bool) (store.AgentIntegrationHead, error) {
	h, err := s.Store.AgentIntegrationHead(ctx, t.ID)
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return h, err
	}
	tree, err := privateGit(ctx, t.ParentRoot, "rev-parse", "--verify", t.BaseRevision+"^{tree}")
	if err != nil {
		return h, err
	}
	h = store.AgentIntegrationHead{TaskID: t.ID, HeadTree: strings.TrimSpace(tree)}
	if !create {
		return h, nil
	}
	if err = s.Store.SetAgentIntegrationHead(ctx, h, 0); err != nil && !errors.Is(err, store.ErrAgentConflict) {
		return h, err
	}
	return s.Store.AgentIntegrationHead(ctx, t.ID)
}

// headVerified reports whether the head's recorded workflow is for exactly the
// head tree and has at least one check, all passed.
func headVerified(h store.AgentIntegrationHead) bool {
	w, err := store.DecodeRuntimeWorkflow(h.Workflow)
	if err != nil || h.HeadTree == "" || w.HeadTree != h.HeadTree || len(w.Checks) == 0 {
		return false
	}
	for _, c := range w.Checks {
		if c.Status != "passed" {
			return false
		}
	}
	return true
}

// runtimeTaskAuthority is integrationAuthority's task, thread, project and
// parent-mode recheck for runtime agents. Storage only (no ParentValidator):
// it runs under the worktree manager's lock.
func (s *WorkspaceService) runtimeTaskAuthority(ctx context.Context, t protocol.AgentTask) error {
	current, err := s.Store.AgentTask(ctx, t.ID)
	if err != nil {
		return err
	}
	if !active(current.State) || current.ParentRoot != t.ParentRoot || current.BaseRoot != t.BaseRoot || current.BaseRevision != t.BaseRevision || current.ThreadID != t.ThreadID || current.ProjectID != t.ProjectID {
		return errors.New("integration task admission changed or was cancelled")
	}
	return s.parentAuthority(ctx, t)
}

// runtimeAuthority additionally requires the revision's agent to still be a
// stopped (completed) writer that submitted exactly this revision.
func (s *WorkspaceService) runtimeAuthority(ctx context.Context, t protocol.AgentTask, rev store.AgentRevision) error {
	if err := s.runtimeTaskAuthority(ctx, t); err != nil {
		return err
	}
	a, err := s.Store.RuntimeAgent(ctx, t.ID, rev.AgentID)
	if err != nil {
		return err
	}
	if a.Capabilities.Files != protocol.FilesReadWrite || a.State != protocol.AgentStateCompleted || a.Revision != rev.ResultRevision || a.WorkspaceID != rev.WorkspaceID {
		return fmt.Errorf("integration agent %s submission changed", a.ID)
	}
	return nil
}

// runtimeWorkspace loads a runtime writer's owned checkout and checks it is
// still that agent's private, uncleaned checkout of the task base at the
// manager's location, with safe Git metadata. Used before every read of an
// agent checkout.
func (s *WorkspaceService) runtimeWorkspace(ctx context.Context, t protocol.AgentTask, agentID, workspaceID string) (store.AgentWorkspace, error) {
	w, err := s.Store.AgentWorkspace(ctx, workspaceID)
	if err != nil {
		return w, err
	}
	if w.TaskID != t.ID || w.ChildID != agentID || w.Kind != "implementer" || w.Cleaned || w.Root != s.Manager.Path(w.ID) || w.BaseRevision != t.BaseRevision {
		return w, errors.New("agent revision workspace identity changed")
	}
	return w, validatePrivate(ctx, w.Root)
}

// RecordRuntimeRevision records a stopped writer's committed result as a
// submitted revision. Only a completed write-capable agent whose result
// differs from the base gets one; for any other stopped agent (read-only,
// cancelled, failed, interrupted, or no change) it records nothing and returns
// the zero revision. An agent that is still active is an error: its checkout
// is only read once the agent is stopped. The result tree and changed paths
// are computed in the agent checkout with hardened plumbing after its metadata
// is validated, and must match the workspace evidence of SnapshotRuntime.
func (s *WorkspaceService) RecordRuntimeRevision(ctx context.Context, task protocol.AgentTask, agent protocol.RuntimeAgent, ws store.AgentWorkspace) (store.AgentRevision, error) {
	defer s.lockTask(task.ID)()
	t, err := s.task(ctx, task)
	if err != nil {
		return store.AgentRevision{}, err
	}
	a, err := s.Store.RuntimeAgent(ctx, t.ID, agent.ID)
	if err != nil {
		return store.AgentRevision{}, err
	}
	if active(a.State) {
		return store.AgentRevision{}, fmt.Errorf("agent %s is %s; a revision is recorded only after it stops", a.ID, a.State)
	}
	if a.Capabilities.Files != protocol.FilesReadWrite || a.State != protocol.AgentStateCompleted {
		return store.AgentRevision{}, nil
	}
	if a.WorkspaceID != ws.ID {
		return store.AgentRevision{}, errors.New("agent revision workspace identity changed")
	}
	w, err := s.runtimeWorkspace(ctx, t, a.ID, ws.ID)
	if err != nil {
		return store.AgentRevision{}, err
	}
	if w.Root != ws.Root {
		return store.AgentRevision{}, errors.New("agent revision workspace identity changed")
	}
	if a.Revision == "" || a.Revision != w.ResultRevision {
		return store.AgentRevision{}, errors.New("agent has no recorded workspace result revision")
	}
	head, err := privateGit(ctx, w.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return store.AgentRevision{}, err
	}
	if strings.TrimSpace(head) != a.Revision {
		return store.AgentRevision{}, errors.New("agent checkout HEAD differs from its recorded result revision")
	}
	tree, err := privateGit(ctx, w.Root, "rev-parse", "--verify", a.Revision+"^{tree}")
	if err != nil {
		return store.AgentRevision{}, err
	}
	tree = strings.TrimSpace(tree)
	if w.ResultTree != "" && w.ResultTree != tree {
		return store.AgentRevision{}, errors.New("agent result tree differs from the recorded workspace evidence")
	}
	paths, err := privateGit(ctx, w.Root, "diff-tree", "-r", "-z", "--name-only", "--no-renames", w.BaseRevision, a.Revision, "--")
	if err != nil {
		return store.AgentRevision{}, err
	}
	// Re-check after the last Git call in the agent checkout.
	if err = validatePrivate(ctx, w.Root); err != nil {
		return store.AgentRevision{}, err
	}
	changed := nulPaths(paths)
	if len(changed) == 0 {
		return store.AgentRevision{}, nil
	}
	return s.Store.SubmitAgentRevision(ctx, store.AgentRevision{TaskID: t.ID, AgentID: a.ID, WorkspaceID: w.ID, BaseRevision: w.BaseRevision,
		ResultRevision: a.Revision, ResultTree: tree, ChangedPaths: changed})
}

// IntegrateRuntime applies one agent's submitted revision onto the integrated
// parent state (lead-owned, one at a time per task). The previous head must be
// verified; an open conflict of another agent blocks it. A conflict is a
// recorded outcome (state conflict, Options set, nil error) and leaves the
// parent unchanged. Calling it again for an applied revision returns the same
// evidence without applying anything.
func (s *WorkspaceService) IntegrateRuntime(ctx context.Context, taskID, agentID string) (RuntimeIntegration, error) {
	defer s.lockTask(taskID)()
	t, err := s.loadTask(ctx, taskID, true)
	if err != nil {
		return RuntimeIntegration{}, err
	}
	if err = s.reconcileLocked(ctx, t, ""); err != nil {
		return RuntimeIntegration{}, err
	}
	rev, err := s.Store.AgentRevision(ctx, taskID, agentID)
	if errors.Is(err, store.ErrNotFound) {
		return RuntimeIntegration{}, fmt.Errorf("agent %s has no submitted revision to integrate", agentID)
	}
	if err != nil {
		return RuntimeIntegration{}, err
	}
	h, err := s.head(ctx, t, true)
	if err != nil {
		return RuntimeIntegration{}, err
	}
	switch rev.State {
	case store.RevisionApplied:
		return RuntimeIntegration{Revision: rev, HeadTree: rev.TreeAfter, NeedsVerification: h.HeadTree == rev.TreeAfter && !headVerified(h)}, nil
	case store.RevisionSubmitted, store.RevisionConflict:
	default:
		return RuntimeIntegration{Revision: rev}, fmt.Errorf("agent %s revision is %s and cannot be integrated", agentID, rev.State)
	}
	revs, err := s.Store.AgentRevisions(ctx, taskID)
	if err != nil {
		return RuntimeIntegration{}, err
	}
	for _, other := range revs {
		if other.ID != rev.ID && other.State == store.RevisionConflict {
			return RuntimeIntegration{}, fmt.Errorf("agent %s revision is in conflict on %s; drop it, resolve it, or replace it with a new agent before accepting another", other.AgentID, strings.Join(other.Conflict, ", "))
		}
	}
	if h.AppliedCount > 0 && !headVerified(h) {
		return RuntimeIntegration{}, fmt.Errorf("the integrated head %s is not verified; record passing workflow checks for it before the next accept", h.HeadTree)
	}
	if err = s.runtimeAuthority(ctx, t, rev); err != nil {
		return RuntimeIntegration{}, err
	}
	// Refuse a changed parent before recording any intent.
	if err = s.Manager.CheckParentState(ctx, t.ParentRoot, t.BaseRevision, h.HeadTree); err != nil {
		return RuntimeIntegration{Revision: rev}, fmt.Errorf("integration refused: %w", err)
	}
	if _, err = s.runtimeWorkspace(ctx, t, rev.AgentID, rev.WorkspaceID); err != nil {
		return RuntimeIntegration{Revision: rev}, err
	}
	// Conflict paths are kept until an apply succeeds, so a failed retry of a
	// conflicted revision restores it as it was. A stale reason (for example
	// an interrupted manual resolution) must not mark this intent as manual.
	prior := rev.State
	rev.State, rev.TreeBefore, rev.TreeAfter, rev.Reason = store.RevisionApplying, h.HeadTree, "", ""
	if rev, err = s.updateRevision(ctx, rev); err != nil {
		return RuntimeIntegration{}, err
	}
	if runtimeIntegrateHook != nil {
		runtimeIntegrateHook("intent")
	}
	// ApplyOnIntegrated runs the guards once before any check and once right
	// before git starts writing the parent. Fewer than two passes therefore
	// means the parent was never mutated by this attempt.
	passes := 0
	guard := func() error {
		if runtimeIntegrateHook != nil {
			runtimeIntegrateHook(fmt.Sprintf("guard%d", passes+1))
		}
		if err := s.runtimeAuthority(ctx, t, rev); err != nil {
			return err
		}
		cur, err := s.Store.AgentIntegrationHead(ctx, t.ID)
		if err != nil {
			return err
		}
		if cur.Version != h.Version || cur.HeadTree != h.HeadTree {
			return errors.New("integration head changed")
		}
		latest, err := s.Store.AgentRevision(ctx, t.ID, agentID)
		if err != nil {
			return err
		}
		if latest.ID != rev.ID || latest.Version != rev.Version || latest.State != store.RevisionApplying {
			return errors.New("integration revision changed")
		}
		passes++
		if runtimeIntegrateHook != nil {
			runtimeIntegrateHook(fmt.Sprintf("passed%d", passes))
		}
		return nil
	}
	res, applyErr := s.Manager.ApplyOnIntegrated(ctx, rev.WorkspaceID, t.ParentRoot, t.BaseRevision, h.HeadTree, rev.ResultRevision, guard)
	// Record the outcome even if the caller went away.
	done := context.WithoutCancel(ctx)
	var conflict *worktree.ConflictError
	switch {
	case applyErr == nil:
		rev.Conflict = nil
		return s.finishApply(done, t, rev, h, res.TreeAfter)
	case errors.As(applyErr, &conflict):
		rev.State, rev.Conflict = store.RevisionConflict, conflict.Paths
		if rev, err = s.updateRevision(done, rev); err != nil {
			return RuntimeIntegration{}, errors.Join(applyErr, err)
		}
		return RuntimeIntegration{Revision: rev, HeadTree: h.HeadTree, Options: conflictOptions()}, nil
	case passes < 2:
		rev.State = prior
		if rev, err = s.updateRevision(done, rev); err != nil {
			return RuntimeIntegration{}, errors.Join(applyErr, err)
		}
		return RuntimeIntegration{Revision: rev, HeadTree: h.HeadTree}, fmt.Errorf("integration refused: %w", applyErr)
	default:
		// Git may have written the parent: decide from the actual parent tree.
		if recErr := s.reconcileRevision(done, t, rev); recErr != nil {
			return RuntimeIntegration{Revision: rev, HeadTree: h.HeadTree}, errors.Join(applyErr, recErr)
		}
		if latest, readErr := s.Store.AgentRevision(done, t.ID, agentID); readErr == nil {
			rev = latest
		}
		return RuntimeIntegration{Revision: rev, HeadTree: h.HeadTree}, fmt.Errorf("integration failed: %w", applyErr)
	}
}

func (s *WorkspaceService) updateRevision(ctx context.Context, rev store.AgentRevision) (store.AgentRevision, error) {
	if err := s.Store.UpdateAgentRevision(ctx, rev, rev.Version); err != nil {
		return rev, err
	}
	return s.Store.AgentRevision(ctx, rev.TaskID, rev.AgentID)
}

// finishApply records an applying revision as applied with treeAfter, then
// moves the head to it and clears its verification state. A crash between
// the two writes is repaired by repairHead.
func (s *WorkspaceService) finishApply(ctx context.Context, t protocol.AgentTask, rev store.AgentRevision, h store.AgentIntegrationHead, treeAfter string) (RuntimeIntegration, error) {
	if h.HeadTree != rev.TreeBefore {
		return RuntimeIntegration{Revision: rev}, errors.New("integration head moved while a revision was applying")
	}
	rev.State, rev.TreeAfter = store.RevisionApplied, treeAfter
	rev, err := s.updateRevision(ctx, rev)
	if err != nil {
		return RuntimeIntegration{Revision: rev}, err
	}
	if err = s.advanceHead(ctx, h, treeAfter); err != nil {
		return RuntimeIntegration{Revision: rev, HeadTree: treeAfter}, err
	}
	return RuntimeIntegration{Revision: rev, HeadTree: treeAfter, NeedsVerification: true}, nil
}

// advanceHead moves the head to tree, counts one more applied revision and
// clears the workflow: the new head has not been verified (Task 4 plans it).
func (s *WorkspaceService) advanceHead(ctx context.Context, h store.AgentIntegrationHead, tree string) error {
	next := h
	next.HeadTree, next.AppliedCount, next.Workflow = tree, h.AppliedCount+1, nil
	return s.Store.SetAgentIntegrationHead(ctx, next, h.Version)
}

// ReconcileRuntimeIntegration settles revisions left `applying` by a crash and
// repairs a head that missed its last applied revision.
func (s *WorkspaceService) ReconcileRuntimeIntegration(ctx context.Context, taskID string) error {
	defer s.lockTask(taskID)()
	t, err := s.loadTask(ctx, taskID, false)
	if err != nil {
		return err
	}
	return s.reconcileLocked(ctx, t, "")
}

// reconcileLocked reconciles every applying revision except a manual
// resolution of resumeAgent (whose caller completes it), then the head.
func (s *WorkspaceService) reconcileLocked(ctx context.Context, t protocol.AgentTask, resumeAgent string) error {
	revs, err := s.Store.AgentRevisions(ctx, t.ID)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range revs {
		if r.State != store.RevisionApplying || (r.AgentID == resumeAgent && r.Reason == manualResolution) {
			continue
		}
		errs = append(errs, s.reconcileRevision(ctx, t, r))
	}
	if err = errors.Join(errs...); err != nil {
		return err
	}
	return s.repairHead(ctx, t)
}

// reconcileRevision decides an applying revision from the actual parent index
// tree, compared exactly with the tree before and with the tree the apply
// produces (worktree.PredictIntegration, computed read-only from the agent's
// revision and TreeBefore, so a merge into a file an earlier revision changed
// is recognised too):
//   - parent tree == TreeBefore: nothing was applied; the revision returns to
//     the state it had before the intent (conflict when it has conflict paths
//     or was a manual resolution, else submitted) and can be retried.
//   - parent tree == predicted tree: the apply finished; mark it applied and
//     move the head.
//   - anything else: left applying (blocked) with an error naming both trees.
//
// An interrupted manual resolution with a changed parent stays applying until
// ResolveRuntimeManually is called again.
func (s *WorkspaceService) reconcileRevision(ctx context.Context, t protocol.AgentTask, rev store.AgentRevision) error {
	tree, err := s.Manager.ParentTree(ctx, t.ParentRoot)
	if err != nil {
		return fmt.Errorf("agent %s revision is blocked in applying: %w", rev.AgentID, err)
	}
	manual := rev.Reason == manualResolution
	if tree == rev.TreeBefore {
		rev.State = store.RevisionSubmitted
		if manual || len(rev.Conflict) > 0 {
			rev.State = store.RevisionConflict
		}
		if manual {
			rev.Reason = "manual resolution interrupted before the parent changed"
		}
		_, err = s.updateRevision(ctx, rev)
		return err
	}
	if manual {
		return fmt.Errorf("agent %s manual resolution was interrupted; call resolved again to record the parent tree %s (head was %s)", rev.AgentID, tree, rev.TreeBefore)
	}
	predicted, err := s.predictRevision(ctx, t, rev)
	if err != nil {
		return fmt.Errorf("agent %s revision is blocked in applying: the parent tree %s is not the tree before %s and the applied tree cannot be computed: %w", rev.AgentID, tree, rev.TreeBefore, err)
	}
	if tree != predicted {
		where := ""
		if diff, diffErr := privateGit(ctx, t.ParentRoot, "diff-tree", "-r", "-z", "--name-only", "--no-renames", predicted, tree, "--"); diffErr == nil {
			where = " (it differs from the applied tree at " + strings.Join(nulPaths(diff), ", ") + ")"
		}
		return fmt.Errorf("agent %s revision is blocked in applying: the parent tree %s is neither the tree before %s nor the tree after %s%s; "+
			"restore the project checkout so HEAD is the task base and its staged state is the integrated head tree %s "+
			"(for a checkout with only staged changes: git read-tree -m -u %s), then retry", rev.AgentID, tree, rev.TreeBefore, predicted, where, rev.TreeBefore, rev.TreeBefore)
	}
	h, err := s.head(ctx, t, true)
	if err != nil {
		return err
	}
	rev.Conflict = nil
	_, err = s.finishApply(ctx, t, rev, h, tree)
	return err
}

// predictRevision returns the tree applying rev onto its TreeBefore produces.
func (s *WorkspaceService) predictRevision(ctx context.Context, t protocol.AgentTask, rev store.AgentRevision) (string, error) {
	if _, err := s.runtimeWorkspace(ctx, t, rev.AgentID, rev.WorkspaceID); err != nil {
		return "", err
	}
	return s.Manager.PredictIntegration(ctx, rev.WorkspaceID, t.ParentRoot, t.BaseRevision, rev.TreeBefore, rev.ResultRevision)
}

// repairHead advances a head that missed the write after its last applied
// revision (crash inside finishApply). The last applied revision is the one
// most recently updated: applied is terminal, so its update time is when it
// was applied (submission order, seq, need not be apply order).
func (s *WorkspaceService) repairHead(ctx context.Context, t protocol.AgentTask) error {
	revs, err := s.Store.AgentRevisions(ctx, t.ID)
	if err != nil {
		return err
	}
	var last *store.AgentRevision
	applied := 0
	for i, r := range revs {
		if r.State != store.RevisionApplied {
			continue
		}
		applied++
		if last == nil || r.UpdatedAt.After(last.UpdatedAt) || (r.UpdatedAt.Equal(last.UpdatedAt) && r.Seq > last.Seq) {
			last = &revs[i]
		}
	}
	if applied == 0 {
		return nil
	}
	h, err := s.head(ctx, t, true)
	if err != nil {
		return err
	}
	if applied == h.AppliedCount {
		return nil
	}
	if applied == h.AppliedCount+1 && last.TreeBefore == h.HeadTree && last.TreeAfter != "" {
		return s.advanceHead(ctx, h, last.TreeAfter)
	}
	return fmt.Errorf("integration head %s (%d applied) is inconsistent with %d applied revisions", h.HeadTree, h.AppliedCount, applied)
}

// DropRuntime explicitly drops an agent's submitted or conflicted revision
// with a reason. The head and parent are unchanged and the agent's workspace
// is retained. Dropping a dropped revision again returns it unchanged.
func (s *WorkspaceService) DropRuntime(ctx context.Context, taskID, agentID, reason string) (store.AgentRevision, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return store.AgentRevision{}, errors.New("dropping a revision requires a reason")
	}
	defer s.lockTask(taskID)()
	if _, err := s.loadTask(ctx, taskID, true); err != nil {
		return store.AgentRevision{}, err
	}
	rev, err := s.Store.AgentRevision(ctx, taskID, agentID)
	if err != nil {
		return rev, err
	}
	switch rev.State {
	case store.RevisionDropped:
		return rev, nil
	case store.RevisionSubmitted, store.RevisionConflict:
	default:
		return rev, fmt.Errorf("agent %s revision is %s and cannot be dropped", agentID, rev.State)
	}
	rev.State, rev.Reason = store.RevisionDropped, reason
	return s.updateRevision(ctx, rev)
}

// ResolveRuntimeManually records the lead's hand-made resolution of a
// conflicted revision: the parent must be a clean staged state (HEAD == base,
// working tree == index, no untracked files), and its index tree becomes the
// new head, which needs verification before the next accept.
func (s *WorkspaceService) ResolveRuntimeManually(ctx context.Context, taskID, agentID string) (RuntimeIntegration, error) {
	defer s.lockTask(taskID)()
	t, err := s.loadTask(ctx, taskID, true)
	if err != nil {
		return RuntimeIntegration{}, err
	}
	if err = s.reconcileLocked(ctx, t, agentID); err != nil {
		return RuntimeIntegration{}, err
	}
	rev, err := s.Store.AgentRevision(ctx, taskID, agentID)
	if err != nil {
		return RuntimeIntegration{}, err
	}
	resuming := rev.State == store.RevisionApplying && rev.Reason == manualResolution
	if rev.State != store.RevisionConflict && !resuming {
		return RuntimeIntegration{Revision: rev}, fmt.Errorf("agent %s revision is %s; only a conflicted revision can be resolved manually", agentID, rev.State)
	}
	if err = s.runtimeTaskAuthority(ctx, t); err != nil {
		return RuntimeIntegration{Revision: rev}, err
	}
	h, err := s.head(ctx, t, true)
	if err != nil {
		return RuntimeIntegration{}, err
	}
	tree, err := s.Manager.ParentTree(ctx, t.ParentRoot)
	if err != nil {
		return RuntimeIntegration{Revision: rev}, err
	}
	if err = s.Manager.CheckParentState(ctx, t.ParentRoot, t.BaseRevision, tree); err != nil {
		return RuntimeIntegration{Revision: rev}, fmt.Errorf("manual resolution requires the fix staged in the parent with nothing else changed: %w", err)
	}
	if !resuming {
		rev.State, rev.Reason, rev.TreeBefore, rev.TreeAfter = store.RevisionApplying, manualResolution, h.HeadTree, ""
		if rev, err = s.updateRevision(ctx, rev); err != nil {
			return RuntimeIntegration{}, err
		}
	}
	return s.finishApply(context.WithoutCancel(ctx), t, rev, h, tree)
}

// RuntimeIntegrationStatus reports the task's integration head and revisions.
func (s *WorkspaceService) RuntimeIntegrationStatus(ctx context.Context, taskID string) (RuntimeIntegrationStatus, error) {
	defer s.lockTask(taskID)()
	t, err := s.loadTask(ctx, taskID, false)
	if err != nil {
		return RuntimeIntegrationStatus{}, err
	}
	h, err := s.head(ctx, t, false)
	if err != nil {
		return RuntimeIntegrationStatus{}, err
	}
	revs, err := s.Store.AgentRevisions(ctx, taskID)
	if err != nil {
		return RuntimeIntegrationStatus{}, err
	}
	st := RuntimeIntegrationStatus{HeadTree: h.HeadTree, AppliedCount: h.AppliedCount, Revisions: revs, Verified: h.Version > 0 && headVerified(h), Settled: true}
	for _, r := range revs {
		if r.State != store.RevisionApplied && r.State != store.RevisionDropped {
			st.Settled = false
		}
	}
	return st, nil
}

// ValidateRuntimeHead checks that headTree is the recorded integration head
// and that the parent checkout is exactly that state.
func (s *WorkspaceService) ValidateRuntimeHead(ctx context.Context, taskID, headTree string) error {
	defer s.lockTask(taskID)()
	t, err := s.loadTask(ctx, taskID, false)
	if err != nil {
		return err
	}
	_, err = s.validateHeadLocked(ctx, t, headTree)
	return err
}

// validateHeadLocked is ValidateRuntimeHead for a caller holding the task
// lock; it returns the head it validated.
func (s *WorkspaceService) validateHeadLocked(ctx context.Context, t protocol.AgentTask, headTree string) (store.AgentIntegrationHead, error) {
	h, err := s.head(ctx, t, false)
	if err != nil {
		return h, err
	}
	if h.HeadTree != headTree {
		return h, fmt.Errorf("integration head is %s, not %s", h.HeadTree, headTree)
	}
	return h, s.Manager.CheckParentState(ctx, t.ParentRoot, t.BaseRevision, headTree)
}

// UpdateRuntimeWorkflow replaces the verification workflow stored in the
// task's integration head. Under the task lock (so it never races
// advanceHead's version-checked write) it requires an active task, a recorded
// head whose tree is headTree and a parent checkout that is exactly that tree
// (ValidateRuntimeHead's rule); update receives the stored workflow and must
// return one for headTree. If the version-checked write still loses to a
// writer outside the lock, everything is reloaded and rechecked once.
func (s *WorkspaceService) UpdateRuntimeWorkflow(ctx context.Context, taskID, headTree string, update func(store.RuntimeWorkflow) (store.RuntimeWorkflow, error)) error {
	defer s.lockTask(taskID)()
	for attempt := 0; ; attempt++ {
		err := s.updateWorkflowLocked(ctx, taskID, headTree, update)
		if attempt == 0 && errors.Is(err, store.ErrAgentConflict) {
			continue
		}
		return err
	}
}

func (s *WorkspaceService) updateWorkflowLocked(ctx context.Context, taskID, headTree string, update func(store.RuntimeWorkflow) (store.RuntimeWorkflow, error)) error {
	t, err := s.loadTask(ctx, taskID, true)
	if err != nil {
		return err
	}
	h, err := s.validateHeadLocked(ctx, t, headTree)
	if err != nil {
		return err
	}
	if h.Version == 0 {
		return errors.New("no integrated head to verify; accept a revision first")
	}
	current, err := store.DecodeRuntimeWorkflow(h.Workflow)
	if err != nil {
		return err
	}
	next, err := update(current)
	if err != nil {
		return err
	}
	if next.HeadTree != headTree {
		return fmt.Errorf("workflow is for %s, not the integration head %s", next.HeadTree, headTree)
	}
	if h.Workflow, err = json.Marshal(next); err != nil {
		return err
	}
	return s.Store.SetAgentIntegrationHead(ctx, h, h.Version)
}
