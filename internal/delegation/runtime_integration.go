package delegation

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	w, err := s.Store.AgentWorkspace(ctx, ws.ID)
	if err != nil {
		return store.AgentRevision{}, err
	}
	if w.TaskID != t.ID || w.ChildID != a.ID || a.WorkspaceID != w.ID || w.Kind != "implementer" || w.Cleaned || w.Root != ws.Root || w.Root != s.Manager.Path(w.ID) || w.BaseRevision != t.BaseRevision {
		return store.AgentRevision{}, errors.New("agent revision workspace identity changed")
	}
	if a.Revision == "" || a.Revision != w.ResultRevision {
		return store.AgentRevision{}, errors.New("agent has no recorded workspace result revision")
	}
	if err = validatePrivate(ctx, w.Root); err != nil {
		return store.AgentRevision{}, err
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
	rev.State, rev.TreeBefore, rev.TreeAfter, rev.Conflict = store.RevisionApplying, h.HeadTree, "", nil
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
		return nil
	}
	res, applyErr := s.Manager.ApplyOnIntegrated(ctx, rev.WorkspaceID, t.ParentRoot, t.BaseRevision, h.HeadTree, rev.ResultRevision, guard)
	// Record the outcome even if the caller went away.
	done := context.WithoutCancel(ctx)
	var conflict *worktree.ConflictError
	switch {
	case applyErr == nil:
		return s.finishApply(done, t, rev, h, res.TreeAfter)
	case errors.As(applyErr, &conflict):
		rev.State, rev.Conflict = store.RevisionConflict, conflict.Paths
		if rev, err = s.updateRevision(done, rev); err != nil {
			return RuntimeIntegration{}, errors.Join(applyErr, err)
		}
		return RuntimeIntegration{Revision: rev, HeadTree: h.HeadTree, Options: conflictOptions()}, nil
	case passes < 2:
		rev.State = store.RevisionSubmitted
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
// tree. ApplyOnIntegrated does not expose its predicted tree before it writes,
// so the tree after is not known in advance; instead:
//   - parent tree == TreeBefore: nothing was applied; back to submitted so the
//     accept can be retried.
//   - the parent is a clean integrated state whose difference from TreeBefore
//     is exactly the revision's changed paths, each with the mode and content
//     of the revision's result tree: the apply finished; mark it applied with
//     that tree and advance the head. (A revision that merged into a file an
//     earlier revision also changed cannot be proven this way and is blocked.)
//   - anything else: left applying, and an error names the mismatch; the lead
//     restores the parent to TreeBefore (then retries) or inspects it.
//
// An interrupted manual resolution is only returned to submitted when the
// parent is still TreeBefore; otherwise ResolveRuntimeManually must be called
// again to finish it.
func (s *WorkspaceService) reconcileRevision(ctx context.Context, t protocol.AgentTask, rev store.AgentRevision) error {
	tree, err := s.Manager.ParentTree(ctx, t.ParentRoot)
	if err != nil {
		return fmt.Errorf("agent %s revision is blocked in applying: %w", rev.AgentID, err)
	}
	if tree == rev.TreeBefore {
		rev.State = store.RevisionSubmitted
		_, err = s.updateRevision(ctx, rev)
		return err
	}
	if rev.Reason == manualResolution {
		return fmt.Errorf("agent %s manual resolution was interrupted; call resolved again to record the parent tree %s", rev.AgentID, tree)
	}
	if mismatch := s.appliedMismatch(ctx, t, rev, tree); mismatch != "" {
		return fmt.Errorf("agent %s revision is blocked in applying: the parent tree %s is neither the tree before (%s) nor that tree with the revision applied: %s; restore the parent to the tree before to retry", rev.AgentID, tree, rev.TreeBefore, mismatch)
	}
	h, err := s.head(ctx, t, true)
	if err != nil {
		return err
	}
	_, err = s.finishApply(ctx, t, rev, h, tree)
	return err
}

// appliedMismatch returns "" when tree is TreeBefore with exactly the
// revision applied, else a description of the first difference found.
func (s *WorkspaceService) appliedMismatch(ctx context.Context, t protocol.AgentTask, rev store.AgentRevision, tree string) string {
	if err := s.Manager.CheckParentState(ctx, t.ParentRoot, t.BaseRevision, tree); err != nil {
		return err.Error()
	}
	diff, err := privateGit(ctx, t.ParentRoot, "diff-tree", "-r", "-z", "--name-only", "--no-renames", rev.TreeBefore, tree, "--")
	if err != nil {
		return err.Error()
	}
	want := map[string]bool{}
	for _, p := range rev.ChangedPaths {
		want[p] = true
	}
	got := map[string]bool{}
	var extra, missing []string
	for _, p := range nulPaths(diff) {
		got[p] = true
		if !want[p] {
			extra = append(extra, p)
		}
	}
	for _, p := range rev.ChangedPaths {
		if !got[p] {
			missing = append(missing, p)
		}
	}
	var parts []string
	if len(extra) > 0 {
		parts = append(parts, "the parent also differs at "+strings.Join(extra, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "revision paths not applied: "+strings.Join(missing, ", "))
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; ")
	}
	w, err := s.Store.AgentWorkspace(ctx, rev.WorkspaceID)
	if err != nil {
		return err.Error()
	}
	if err = validatePrivate(ctx, w.Root); err != nil {
		return err.Error()
	}
	parent, err := treeEntries(ctx, t.ParentRoot, tree)
	if err != nil {
		return err.Error()
	}
	result, err := treeEntries(ctx, w.Root, rev.ResultTree)
	if err != nil {
		return err.Error()
	}
	var differ []string
	for _, p := range rev.ChangedPaths {
		if parent[p] != result[p] {
			differ = append(differ, p)
		}
	}
	if len(differ) > 0 {
		sort.Strings(differ)
		return "content differs from the revision at " + strings.Join(differ, ", ")
	}
	return ""
}

// treeEntries lists tree recursively as path -> "mode id" (hardened plumbing).
func treeEntries(ctx context.Context, root, tree string) (map[string]string, error) {
	out, err := privateGit(ctx, root, "ls-tree", "-r", "-z", "--full-tree", tree)
	if err != nil {
		return nil, err
	}
	entries := map[string]string{}
	for _, line := range nulPaths(out) {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return nil, errors.New("invalid tree listing")
		}
		entries[name] = f[0] + " " + f[2]
	}
	return entries, nil
}

// repairHead advances a head that missed the write after its last applied
// revision (crash inside finishApply).
func (s *WorkspaceService) repairHead(ctx context.Context, t protocol.AgentTask) error {
	revs, err := s.Store.AgentRevisions(ctx, t.ID)
	if err != nil {
		return err
	}
	var applied []store.AgentRevision
	for _, r := range revs {
		if r.State == store.RevisionApplied {
			applied = append(applied, r)
		}
	}
	if len(applied) == 0 {
		return nil
	}
	h, err := s.head(ctx, t, true)
	if err != nil {
		return err
	}
	switch {
	case len(applied) == h.AppliedCount:
		return nil
	case len(applied) == h.AppliedCount+1:
		for _, r := range applied {
			if r.TreeBefore == h.HeadTree && r.TreeAfter != "" && (r.TreeAfter != h.HeadTree || r.Reason == manualResolution) {
				return s.advanceHead(ctx, h, r.TreeAfter)
			}
		}
	}
	return fmt.Errorf("integration head %s (%d applied) is inconsistent with %d applied revisions", h.HeadTree, h.AppliedCount, len(applied))
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
	h, err := s.head(ctx, t, false)
	if err != nil {
		return err
	}
	if h.HeadTree != headTree {
		return fmt.Errorf("integration head is %s, not %s", h.HeadTree, headTree)
	}
	return s.Manager.CheckParentState(ctx, t.ParentRoot, t.BaseRevision, headTree)
}
