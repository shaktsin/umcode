package delegation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
type IntegrationEvidence = store.AgentIntegration

// ParentValidator checks live project membership and the already existing
// active parent checkout; it must never create or retarget a checkout.
type ParentValidator func(context.Context, protocol.AgentTask) error

// IntegrationEligibility is supplied by Task7, which owns review verdicts and
// check policy. Nil always blocks integration; text summaries grant no approval.
type IntegrationEligibility func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error

type WorkspaceService struct {
	Store          *store.Store
	Manager        *worktree.Manager
	ValidateParent ParentValidator
	Eligibility    IntegrationEligibility
	mu             sync.Mutex
}

func NewWorkspaceService(s *store.Store, m *worktree.Manager, parent ParentValidator, eligible IntegrationEligibility) *WorkspaceService {
	return &WorkspaceService{Store: s, Manager: m, ValidateParent: parent, Eligibility: eligible}
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
func (s *WorkspaceService) attach(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild, id string) error {
	for i := 0; i < 8; i++ {
		latest, err := s.Store.AgentChild(ctx, t.ID, c.ID)
		if err != nil {
			return err
		}
		if latest.WorkspaceID != "" && latest.WorkspaceID != id {
			return errors.New("child already owns a different workspace")
		}
		if latest.WorkspaceID == id {
			return nil
		}
		latest.WorkspaceID = id
		err = s.Store.UpdateAgentChild(ctx, latest, latest.Version)
		if errors.Is(err, store.ErrAgentConflict) {
			continue
		}
		return err
	}
	return store.ErrAgentConflict
}
func (s *WorkspaceService) Prepare(ctx context.Context, task protocol.AgentTask, child protocol.AgentChild) (WorkspaceRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepare(ctx, task, child)
}
func (s *WorkspaceService) prepare(ctx context.Context, task protocol.AgentTask, child protocol.AgentChild) (WorkspaceRef, error) {
	t, err := s.task(ctx, task)
	if err != nil {
		return WorkspaceRef{}, err
	}
	c, err := s.Store.AgentChild(ctx, t.ID, child.ID)
	if err != nil {
		return WorkspaceRef{}, err
	}
	if child.TaskID != t.ID || child.Role != c.Role {
		return WorkspaceRef{}, errors.New("child workspace ownership changed")
	}
	if c.Role == protocol.AgentRoleReviewer {
		return s.prepareReviewer(ctx, t, c)
	}
	if err = worktree.ValidateCommitted(ctx, t.ParentRoot, t.BaseRevision); err != nil {
		return WorkspaceRef{}, err
	}
	id := "delegation-" + t.ID + "-" + c.ID
	planned := s.Manager.Path(id)
	if pathutil.Within(t.ParentRoot, planned) || pathutil.Within(planned, t.ParentRoot) || pathutil.Within(t.BaseRoot, planned) || pathutil.Within(planned, t.BaseRoot) {
		return WorkspaceRef{}, errors.New("owned child checkout must be outside parent and registered project roots")
	}
	w, err := s.Store.AgentWorkspace(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		w = store.AgentWorkspace{ID: id, TaskID: t.ID, ChildID: c.ID, Kind: "implementer", Root: s.Manager.Path(id), ParentRoot: t.ParentRoot, BaseRevision: t.BaseRevision}
		if err = s.Store.SaveAgentWorkspace(ctx, w); err != nil {
			return WorkspaceRef{}, err
		}
	} else if err != nil {
		return WorkspaceRef{}, err
	}
	if w.Cleaned {
		return ref(w), errors.New("workspace was cleaned after verified integration")
	}
	if err = s.attach(ctx, t, c, w.ID); err != nil {
		return ref(w), err
	}
	ws, err := s.Manager.EnsureCommitted(ctx, id, t.ParentRoot, t.BaseRevision)
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

// Snapshot is only called after execution stops. It creates an engine-owned
// private commit of the actual tree, including staged, untracked and deletions.
// Parent changes do not prevent saving recoverable stopped work.
func (s *WorkspaceService) Snapshot(ctx context.Context, task protocol.AgentTask, child protocol.AgentChild, workspace WorkspaceRef) (WorkspaceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.task(ctx, task)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	c, err := s.Store.AgentChild(ctx, t.ID, child.ID)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	w, err := s.Store.AgentWorkspace(ctx, workspace.ID)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	if w.TaskID != t.ID || w.Root != workspace.Root || c.WorkspaceID != w.ID || w.Cleaned {
		return WorkspaceSnapshot{}, errors.New("finalization workspace identity changed")
	}
	source := w.ParentRoot
	if _, err = s.Manager.Lookup(ctx, w.ID, source); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if err = validatePrivate(ctx, w.Root); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if w.Kind == "reviewer" {
		if err = worktree.ValidateCommitted(ctx, w.Root, w.ResultRevision); err != nil {
			return WorkspaceSnapshot{}, err
		}
		return WorkspaceSnapshot{WorkspaceRef: ref(w), ResultRevision: w.ResultRevision}, nil
	}
	if err = worktree.ValidateIndexFlags(ctx, w.Root); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if err = worktree.ValidatePaths(ctx, w.Root); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if c.Revision != "" && w.ResultRevision != "" {
		if err = worktree.ValidateCommitted(ctx, w.Root, w.ResultRevision); err != nil {
			return WorkspaceSnapshot{}, err
		}
		return WorkspaceSnapshot{WorkspaceRef: ref(w), ResultRevision: w.ResultRevision, ChangedPaths: w.ChangedPaths}, nil
	}
	if _, err = privateGit(ctx, w.Root, "add", "--all", "--"); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if _, err = privateGit(ctx, w.Root, "-c", "user.name=UMCode", "-c", "user.email=umcode@localhost", "commit", "--quiet", "--allow-empty", "-m", "UMCode delegated result"); err != nil {
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
func (s *WorkspaceService) submitted(ctx context.Context, childID string) (protocol.AgentTask, protocol.AgentChild, store.AgentWorkspace, error) {
	w, err := s.Store.AgentWorkspaceForChild(ctx, childID, "implementer")
	if err != nil {
		return protocol.AgentTask{}, protocol.AgentChild{}, w, err
	}
	t, err := s.Store.AgentTask(ctx, w.TaskID)
	if err != nil {
		return t, protocol.AgentChild{}, w, err
	}
	if _, err = s.task(ctx, t); err != nil {
		return t, protocol.AgentChild{}, w, err
	}
	c, err := s.Store.AgentChild(ctx, t.ID, childID)
	if err != nil {
		return t, c, w, err
	}
	if c.Role != protocol.AgentRoleImplementer || c.State != protocol.AgentStateCompleted || c.Revision == "" || c.Revision != w.ResultRevision || c.WorkspaceID != w.ID || w.Cleaned {
		return t, c, w, errors.New("implementer has no stopped submitted workspace revision")
	}
	if _, err = s.Manager.Lookup(ctx, w.ID, w.ParentRoot); err != nil {
		return t, c, w, err
	}
	if err = validatePrivate(ctx, w.Root); err != nil {
		return t, c, w, err
	}
	err = worktree.ValidateCommitted(ctx, w.Root, w.ResultRevision)
	return t, c, w, err
}

// Review returns a distinct exact revision copy. BindReviewer records which
// result a reviewer child is authorized to inspect before paid execution.
func (s *WorkspaceService) Review(ctx context.Context, implementerID string) (WorkspaceRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, c, w, err := s.submitted(ctx, implementerID)
	if err != nil {
		return WorkspaceRef{}, err
	}
	return s.reviewCopy(ctx, t, c, w, c.ID, "inspection")
}
func (s *WorkspaceService) BindReviewer(ctx context.Context, taskID, reviewerID, implementerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _, w, err := s.submitted(ctx, implementerID)
	if err != nil {
		return err
	}
	if t.ID != taskID {
		return store.ErrNotFound
	}
	old, rev, err := s.Store.AgentReviewerSource(ctx, taskID, reviewerID)
	if err == nil {
		if old == implementerID && rev == w.ResultRevision {
			return nil
		}
		return errors.New("reviewer already bound to a different submitted revision")
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return s.Store.BindAgentReviewer(ctx, taskID, reviewerID, implementerID, w.ResultRevision)
}
func (s *WorkspaceService) prepareReviewer(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild) (WorkspaceRef, error) {
	id, rev, err := s.Store.AgentReviewerSource(ctx, t.ID, c.ID)
	if err != nil {
		return WorkspaceRef{}, fmt.Errorf("reviewer requires an explicit submitted revision binding: %w", err)
	}
	_, impl, w, err := s.submitted(ctx, id)
	if err != nil {
		return WorkspaceRef{}, err
	}
	if w.ResultRevision != rev {
		return WorkspaceRef{}, errors.New("reviewer binding is stale after implementer correction")
	}
	r, err := s.reviewCopy(ctx, t, impl, w, c.ID, "reviewer")
	if err != nil {
		return r, err
	}
	return r, s.attach(ctx, t, c, r.ID)
}
func (s *WorkspaceService) reviewCopy(ctx context.Context, t protocol.AgentTask, impl protocol.AgentChild, source store.AgentWorkspace, owner, suffix string) (WorkspaceRef, error) {
	id := "delegation-review-" + owner + "-" + suffix
	kind := "reviewer"
	if suffix == "inspection" {
		id += "-" + source.ResultRevision
		kind = "inspection"
	}
	w, err := s.Store.AgentWorkspace(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		w = store.AgentWorkspace{ID: id, TaskID: t.ID, ChildID: owner, Kind: kind, SourceID: impl.ID, Root: s.Manager.Path(id), ParentRoot: source.Root, BaseRevision: source.ResultRevision, ResultRevision: source.ResultRevision}
		if err = s.Store.SaveAgentWorkspace(ctx, w); err != nil {
			return WorkspaceRef{}, err
		}
	} else if err != nil {
		return WorkspaceRef{}, err
	}
	if w.ResultRevision != source.ResultRevision || w.SourceID != impl.ID || w.Cleaned {
		return ref(w), errors.New("review copy bound to a different revision")
	}
	if _, err = s.Manager.EnsureCommitted(ctx, id, source.Root, source.ResultRevision); err != nil {
		return ref(w), err
	}
	if err = validatePrivate(ctx, w.Root); err != nil {
		return ref(w), err
	}
	if err = worktree.ValidateCommitted(ctx, w.Root, source.ResultRevision); err != nil {
		return ref(w), err
	}
	return ref(w), nil
}

func (s *WorkspaceService) Integrate(ctx context.Context, taskID, childID, expectedBase string) (IntegrationEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, c, w, err := s.submitted(ctx, childID)
	if err != nil {
		return IntegrationEvidence{}, err
	}
	if t.ID != taskID || expectedBase != t.BaseRevision || !active(t.State) {
		return IntegrationEvidence{}, errors.New("integration base or task changed")
	}
	if s.Eligibility == nil {
		return IntegrationEvidence{}, errors.New("integration requires resolved review eligibility")
	}
	if err = s.Eligibility(ctx, t, c, w.ResultRevision); err != nil {
		return IntegrationEvidence{}, err
	}
	evidence, err := s.Store.AgentIntegration(ctx, taskID, childID)
	if err == nil {
		if evidence.BaseRevision != expectedBase {
			return evidence, errors.New("saved integration refers to another base")
		}
		if evidence.ResultRevision != w.ResultRevision {
			if evidence.Applied || evidence.Verified {
				return evidence, errors.New("previous integration was applied; retain its evidence and workspace")
			}
			prior := evidence
			next := IntegrationEvidence{BaseRevision: expectedBase, ResultRevision: w.ResultRevision, ChangedPaths: w.ChangedPaths}
			err = s.Manager.WithCommittedBase(ctx, t.ParentRoot, expectedBase,
				func() error { return s.integrationAuthority(ctx, t, c, w) },
				func() error {
					var saveErr error
					next, saveErr = s.Store.SupersedeAgentIntegration(ctx, taskID, childID, prior.ResultRevision, next)
					return saveErr
				})
			if err != nil {
				return prior, err
			}
			evidence = next
		} else if err = s.verifyApplied(ctx, t, w); err == nil {
			if !evidence.Applied {
				evidence.Applied = true
				err = s.Store.SaveAgentIntegration(ctx, taskID, childID, evidence)
			}
			return evidence, err
		}
		if evidence.Applied {
			return evidence, errors.New("previously integrated parent tree changed; workspace retained")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return evidence, err
	}
	if err = worktree.ValidateCommitted(ctx, t.ParentRoot, expectedBase); err != nil {
		return evidence, err
	}
	evidence = IntegrationEvidence{BaseRevision: expectedBase, ResultRevision: w.ResultRevision, ChangedPaths: w.ChangedPaths, PreviousAttempts: evidence.PreviousAttempts}
	if err = s.Store.SaveAgentIntegration(ctx, taskID, childID, evidence); err != nil {
		return evidence, err
	}
	// Recheck live eligibility after the durable intent and immediately before the
	// manager acquires its integration lock; manager repeats base/tree guards.
	if _, err = s.task(ctx, t); err != nil {
		return evidence, err
	}
	if err = s.Eligibility(ctx, t, c, w.ResultRevision); err != nil {
		return evidence, err
	}
	if _, err = s.Manager.KeepCommitted(ctx, w.ID, t.ParentRoot, expectedBase, w.ResultRevision, func() error { return s.integrationAuthority(ctx, t, c, w) }); err != nil {
		return evidence, err
	}
	if err = s.verifyApplied(ctx, t, w); err != nil {
		return evidence, err
	}
	evidence.Applied = true
	err = s.Store.SaveAgentIntegration(ctx, taskID, childID, evidence)
	return evidence, err
}

// This storage-only recheck runs under the manager's integration lock. It must
// not call ParentValidator (which may itself look up a managed parent checkout).
func (s *WorkspaceService) integrationAuthority(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild, w store.AgentWorkspace) error {
	current, err := s.Store.AgentTask(ctx, t.ID)
	if err != nil {
		return err
	}
	if !active(current.State) || current.ParentRoot != t.ParentRoot || current.BaseRoot != t.BaseRoot || current.BaseRevision != t.BaseRevision || current.ThreadID != t.ThreadID || current.ProjectID != t.ProjectID {
		return errors.New("integration task admission changed or was cancelled")
	}
	latest, err := s.Store.AgentChild(ctx, t.ID, c.ID)
	if err != nil {
		return err
	}
	if latest.State != protocol.AgentStateCompleted || latest.Revision != w.ResultRevision || latest.WorkspaceID != w.ID {
		return errors.New("integration child submission changed")
	}
	th, err := s.Store.GetThread(ctx, t.ThreadID)
	if err != nil {
		return err
	}
	if th.ProjectID != t.ProjectID {
		return errors.New("integration parent project changed")
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
	return s.Eligibility(ctx, current, latest, w.ResultRevision)
}

func (s *WorkspaceService) verifyApplied(ctx context.Context, t protocol.AgentTask, w store.AgentWorkspace) error {
	if err := worktree.ValidateIndexFlags(ctx, t.ParentRoot); err != nil {
		return err
	}
	head, err := privateGit(ctx, t.ParentRoot, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != t.BaseRevision {
		return errors.New("integration parent HEAD changed")
	}
	tree, err := privateGit(ctx, t.ParentRoot, "write-tree")
	if err != nil {
		return err
	}
	expected := w.ResultTree
	if expected == "" {
		return errors.New("integration result tree evidence is missing")
	}
	if strings.TrimSpace(tree) != strings.TrimSpace(expected) {
		return errors.New("integration parent index differs from submitted result")
	}
	dirty, err := privateGit(ctx, t.ParentRoot, "diff", "--name-only", "--")
	if err != nil || dirty != "" {
		return errors.New("integration parent working tree differs from result")
	}
	other, err := privateGit(ctx, t.ParentRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil || other != "" {
		return errors.New("integration parent has untracked edits")
	}
	return worktree.ValidatePaths(ctx, t.ParentRoot)
}

// RecordVerification is a trusted lead seam, never a model tool. Task7 decides
// which actual checks are sufficient before recording verified evidence.
func (s *WorkspaceService) RecordVerification(ctx context.Context, taskID, childID, revision string, checks []protocol.AgentCheckEvidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _, w, err := s.submitted(ctx, childID)
	if err != nil {
		return err
	}
	if t.ID != taskID || revision != w.ResultRevision || len(checks) == 0 {
		return errors.New("verification must identify integrated revision and actual checks")
	}
	for _, check := range checks {
		if check.Command == "" || check.Status != "passed" {
			return errors.New("verification requires successful recorded checks")
		}
	}
	e, err := s.Store.AgentIntegration(ctx, taskID, childID)
	if err != nil {
		return err
	}
	if !e.Applied {
		return errors.New("integration has not been durably recorded")
	}
	if err = s.verifyApplied(ctx, t, w); err != nil {
		return err
	}
	e.Verified = true
	e.CheckResults = checks
	return s.Store.SaveAgentIntegration(ctx, taskID, childID, e)
}
func (s *WorkspaceService) Cleanup(ctx context.Context, taskID, childID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.Store.AgentIntegration(ctx, taskID, childID)
	if err != nil {
		return err
	}
	if !e.Applied || !e.Verified {
		return errors.New("retain workspace until integration and verification are durable")
	}
	w, err := s.Store.AgentWorkspaceForChild(ctx, childID, "implementer")
	if err != nil {
		return err
	}
	if w.TaskID != taskID || e.ResultRevision != w.ResultRevision {
		return errors.New("cleanup integration identity changed")
	}
	if w.Cleaned {
		return nil
	}
	t, err := s.Store.AgentTask(ctx, taskID)
	if err != nil {
		return err
	}
	if _, err = s.task(ctx, t); err != nil {
		return err
	}
	if err = s.verifyApplied(ctx, t, w); err != nil {
		return err
	}
	reviews, err := s.Store.AgentWorkspaces(ctx, taskID)
	if err != nil {
		return err
	}
	for _, review := range reviews {
		if (review.Kind != "reviewer" && review.Kind != "inspection") || review.SourceID != childID || review.Cleaned {
			continue
		}
		if err = s.discardVerified(ctx, review); err != nil {
			return err
		}
	}
	return s.discardVerified(ctx, w)
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

func privateGit(ctx context.Context, root string, args ...string) (string, error) {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null", "-C", root}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("workspace git %s: %w: %s", strings.Join(args, " "), err, b)
	}
	return string(b), nil
}
func validatePrivate(ctx context.Context, root string) error {
	metadata := filepath.Join(root, ".git")
	if err := filepath.WalkDir(metadata, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("private Git metadata contains a symlink")
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	for _, p := range []string{"objects/info/alternates", "objects/info/http-alternates", "commondir", "info/grafts", "refs/replace"} {
		if _, err := os.Lstat(filepath.Join(metadata, p)); err == nil {
			return errors.New("private Git metadata redirects outside its owned snapshot")
		}
	}
	config, err := privateGit(ctx, root, "config", "--local", "--no-includes", "--list")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(config), "\n") {
		key, _, _ := strings.Cut(line, "=")
		switch key {
		case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "extensions.objectformat", "user.name", "user.email":
		default:
			return fmt.Errorf("unsupported private Git configuration %q; retain workspace for inspection", key)
		}
	}
	return nil
}

// ValidateIntegrated brackets actual lead tool execution and completion. It
// checks exact parent bytes and durable review without accepting model evidence.
func (s *WorkspaceService) ValidateIntegrated(ctx context.Context, taskID, childID, revision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, c, w, err := s.submitted(ctx, childID)
	if err != nil {
		return err
	}
	if t.ID != taskID || w.ResultRevision != revision {
		return errors.New("integrated revision changed")
	}
	if s.Eligibility == nil {
		return errors.New("review eligibility unavailable")
	}
	if err = s.Eligibility(ctx, t, c, revision); err != nil {
		return err
	}
	i, err := s.Store.AgentIntegration(ctx, taskID, childID)
	if err != nil {
		return err
	}
	if !i.Applied || i.ResultRevision != revision {
		return errors.New("integration not applied")
	}
	return s.verifyApplied(ctx, t, w)
}
