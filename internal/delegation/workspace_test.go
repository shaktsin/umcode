package delegation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
)

func workspaceGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}
func workspaceWrite(t *testing.T, root, path, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func workspaceFixture(t *testing.T) (*WorkspaceService, *store.Store, protocol.AgentTask, protocol.AgentChild) {
	t.Helper()
	root := t.TempDir()
	workspaceGit(t, root, "init", "-q")
	workspaceGit(t, root, "config", "user.name", "Test")
	workspaceGit(t, root, "config", "user.email", "test@invalid")
	workspaceWrite(t, root, "tracked", "base\n")
	workspaceWrite(t, root, "removed", "remove\n")
	workspaceGit(t, root, "add", ".")
	workspaceGit(t, root, "commit", "-qm", "base")
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	tr := protocol.Turn{ID: "root", ThreadID: th.ID, Status: protocol.TurnCompleted, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), tr); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "task", ThreadID: th.ID, RootTurnID: tr.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRoot: root, ParentRoot: root, BaseRevision: workspaceGit(t, root, "rev-parse", "HEAD")}
	if err = s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{ID: "implementer", TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "edit"}}, "call")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWorkspaceService(s, worktree.New(t.TempDir()), nil, nil)
	return svc, s, task, child
}
func workspaceComplete(t *testing.T, s *store.Store, task protocol.AgentTask, child protocol.AgentChild, rev string) protocol.AgentChild {
	t.Helper()
	c, err := s.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.State = protocol.AgentStateRunning
	if err = s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
		t.Fatal(err)
	}
	c, err = s.AgentChild(t.Context(), task.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.State = protocol.AgentStateCompleted
	c.Revision = rev
	if err = s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
		t.Fatal(err)
	}
	return c
}

// Removing clean-base guards or snapshotting only HEAD loses required inputs.
func TestDelegationWorkspaceSafety(t *testing.T) {
	t.Run("isolation final tree and review", func(t *testing.T) {
		svc, s, task, child := workspaceFixture(t)
		ws, err := svc.Prepare(t.Context(), task, child)
		if err != nil {
			t.Fatal(err)
		}
		if ws.Root == task.ParentRoot || ws.ID == "" || ws.BaseRevision != task.BaseRevision {
			t.Fatalf("workspace %+v", ws)
		}
		saved, err := s.AgentChild(t.Context(), task.ID, child.ID)
		if err != nil || saved.WorkspaceID != ws.ID {
			t.Fatalf("identity not durable: %+v %v", saved, err)
		}
		workspaceWrite(t, ws.Root, "tracked", "result\n")
		workspaceWrite(t, ws.Root, "added", "new\n")
		if err = os.Remove(filepath.Join(ws.Root, "removed")); err != nil {
			t.Fatal(err)
		}
		workspaceGit(t, ws.Root, "add", "tracked")
		final, err := svc.Snapshot(t.Context(), task, child, ws)
		if err != nil {
			t.Fatal(err)
		}
		workspaceComplete(t, s, task, child, final.ResultRevision)
		if !reflect.DeepEqual(final.ChangedPaths, []string{"added", "removed", "tracked"}) {
			t.Fatalf("changed paths %+v", final)
		}
		review, err := svc.Review(t.Context(), child.ID)
		if err != nil {
			t.Fatal(err)
		}
		if review.Root == ws.Root || workspaceGit(t, review.Root, "rev-parse", "HEAD") != final.ResultRevision {
			t.Fatalf("review not exact %+v", review)
		}
		if b, _ := os.ReadFile(filepath.Join(task.ParentRoot, "tracked")); string(b) != "base\n" {
			t.Fatalf("parent changed %q", b)
		}
		// Finalizer cannot silently retarget an already submitted result.
		workspaceGit(t, ws.Root, "checkout", "--detach", task.BaseRevision)
		if _, err = svc.Review(t.Context(), child.ID); err == nil {
			t.Fatal("retargeted implementer accepted")
		}
	})
	for _, kind := range []string{"tracked", "untracked", "staged", "no commit", "non Git", "subdirectory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, task, child := workspaceFixture(t)
			switch kind {
			case "tracked":
				workspaceWrite(t, task.ParentRoot, "tracked", "dirty")
			case "untracked":
				workspaceWrite(t, task.ParentRoot, "UMCODE.md", "required")
			case "staged":
				workspaceWrite(t, task.ParentRoot, "tracked", "dirty")
				workspaceGit(t, task.ParentRoot, "add", ".")
			case "no commit":
				task.ParentRoot = t.TempDir()
				workspaceGit(t, task.ParentRoot, "init", "-q")
			case "non Git":
				task.ParentRoot = t.TempDir()
			case "subdirectory":
				task.ParentRoot = filepath.Join(task.ParentRoot, "sub")
				os.Mkdir(task.ParentRoot, 0700)
			case "symlink":
				os.Symlink(t.TempDir(), filepath.Join(task.ParentRoot, "escape"))
				workspaceGit(t, task.ParentRoot, "add", ".")
				workspaceGit(t, task.ParentRoot, "commit", "-qm", "symlink")
				task.BaseRevision = workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD")
			}
			// Arrange the unsupported/unsafe admitted target durably so this
			// exercises Git/tree guards rather than supplied-identity rejection.
			if _, err := svc.Store.DB.Exec(`UPDATE agent_tasks SET parent_root=?,base_revision=? WHERE id=?`, task.ParentRoot, task.BaseRevision, task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Prepare(t.Context(), task, child); err == nil {
				t.Fatal("unsafe base accepted")
			}
		})
	}
}
func TestDelegationWorkspaceIntegration(t *testing.T) {
	for _, kind := range []string{"eligibility", "dirty", "untracked", "diverged", "retarget", "cancelled", "success"} {
		t.Run(kind, func(t *testing.T) {
			svc, s, task, child := workspaceFixture(t)
			ws, err := svc.Prepare(t.Context(), task, child)
			if err != nil {
				t.Fatal(err)
			}
			workspaceWrite(t, ws.Root, "tracked", "result\n")
			workspaceWrite(t, ws.Root, "added", "new\n")
			os.Remove(filepath.Join(ws.Root, "removed"))
			final, err := svc.Snapshot(t.Context(), task, child, ws)
			if err != nil {
				t.Fatal(err)
			}
			workspaceComplete(t, s, task, child, final.ResultRevision)
			if kind != "eligibility" {
				svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil }
			}
			ctx := t.Context()
			switch kind {
			case "dirty":
				workspaceWrite(t, task.ParentRoot, "tracked", "user\n")
			case "untracked":
				workspaceWrite(t, task.ParentRoot, "added", "user\n")
			case "diverged":
				workspaceWrite(t, task.ParentRoot, "tracked", "user\n")
				workspaceGit(t, task.ParentRoot, "add", ".")
				workspaceGit(t, task.ParentRoot, "commit", "-qm", "user")
			case "retarget":
				workspaceGit(t, ws.Root, "checkout", "--detach", task.BaseRevision)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := workspaceGit(t, task.ParentRoot, "status", "--porcelain")
			evidence, err := svc.Integrate(ctx, task.ID, child.ID, task.BaseRevision)
			if kind == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if evidence.ResultRevision != final.ResultRevision || len(evidence.ChangedPaths) != 3 {
					t.Fatalf("evidence %+v", evidence)
				}
				if _, err = svc.Integrate(ctx, task.ID, child.ID, task.BaseRevision); err != nil {
					t.Fatalf("durable retry %v", err)
				}
				if err = svc.Cleanup(ctx, task.ID, child.ID); err == nil {
					t.Fatal("cleanup before durable verification")
				}
				if err = svc.RecordVerification(ctx, task.ID, child.ID, final.ResultRevision, []protocol.AgentCheckEvidence{{Command: "test", Status: "passed"}}); err != nil {
					t.Fatal(err)
				}
				if err = svc.Cleanup(ctx, task.ID, child.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(ws.Root); !os.IsNotExist(err) {
					t.Fatalf("cleanup %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe integration accepted")
				}
				if after := workspaceGit(t, task.ParentRoot, "status", "--porcelain"); after != before {
					t.Fatalf("partial apply %q -> %q", before, after)
				}
				if _, err = os.Stat(ws.Root); err != nil {
					t.Fatalf("lost recovery %v", err)
				}
			}
		})
	}
}

func TestDelegationWorkspaceReviewerProvenanceAndRecovery(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "tracked", "result\n")
	final, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, final.ResultRevision)
	reviewer, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{ID: "reviewer", TaskID: task.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "review"}}, "review-call")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Prepare(t.Context(), task, reviewer); err == nil {
		t.Fatal("reviewer without durable source admitted")
	}
	if err = svc.BindReviewer(t.Context(), task.ID, reviewer.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	review, err := svc.Prepare(t.Context(), task, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	source, revision, err := s.AgentReviewerSource(t.Context(), task.ID, reviewer.ID)
	if err != nil || source != child.ID || revision != final.ResultRevision {
		t.Fatalf("lost provenance %q %q %v", source, revision, err)
	}
	if review.Root == ws.Root || workspaceGit(t, review.Root, "rev-parse", "HEAD") != final.ResultRevision {
		t.Fatal("review copy differs")
	}
	workspaceWrite(t, review.Root, "tracked", "forged review")
	if _, err = svc.Snapshot(t.Context(), task, reviewer, review); err == nil {
		t.Fatal("modified reviewer copy finalized")
	}
	workspaceGit(t, review.Root, "checkout", "--", "tracked")
	// A new service recovers identities without creating new workspace paths.
	recovered := NewWorkspaceService(s, svc.Manager, nil, func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil })
	again, err := recovered.Prepare(t.Context(), task, child)
	if err != nil || again.ID != ws.ID || again.Root != ws.Root {
		t.Fatalf("recovery %+v %v", again, err)
	}
	evidence, err := recovered.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = recovered.RecordVerification(t.Context(), task.ID, child.ID, evidence.ResultRevision, []protocol.AgentCheckEvidence{{Command: "verify", Status: "passed"}}); err != nil {
		t.Fatal(err)
	}
	// Cleanup must retain implementer artifacts if any review ownership marker
	// cannot be validated, then succeed on a retry after the marker is repaired.
	marker := review.Root + ".ready"
	original, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(marker, []byte("foreign\n"), 0600)
	if err = recovered.Cleanup(t.Context(), task.ID, child.ID); err == nil {
		t.Fatal("cleanup ignored invalid review ownership")
	}
	if _, err = os.Stat(ws.Root); err != nil {
		t.Fatal("failed cleanup lost implementer")
	}
	os.WriteFile(marker, original, 0600)
	if err = recovered.Cleanup(t.Context(), task.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(review.Root); !os.IsNotExist(err) {
		t.Fatal("verified reviewer workspace not cleaned")
	}
}
func TestDelegationWorkspaceIntegrationRechecks(t *testing.T) {
	for _, kind := range []string{"parent after review", "child retarget", "failed evidence"} {
		t.Run(kind, func(t *testing.T) {
			svc, s, task, child := workspaceFixture(t)
			ws, err := svc.Prepare(t.Context(), task, child)
			if err != nil {
				t.Fatal(err)
			}
			workspaceWrite(t, ws.Root, "tracked", "result\n")
			final, err := svc.Snapshot(t.Context(), task, child, ws)
			if err != nil {
				t.Fatal(err)
			}
			workspaceComplete(t, s, task, child, final.ResultRevision)
			if _, err = svc.Review(t.Context(), child.ID); err != nil {
				t.Fatal(err)
			}
			calls := 0
			svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error {
				calls++
				if calls == 2 {
					switch kind {
					case "parent after review":
						workspaceWrite(t, task.ParentRoot, "tracked", "user after review\n")
					case "child retarget":
						workspaceGit(t, ws.Root, "checkout", "--detach", task.BaseRevision)
					}
				}
				return nil
			}
			if kind == "failed evidence" {
				s.DB.Exec(`CREATE TRIGGER reject_integration BEFORE INSERT ON agent_integrations BEGIN SELECT RAISE(ABORT,'storage failed'); END;`)
			}
			if _, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); err == nil {
				t.Fatal("boundary mutation accepted")
			}
			b, err := os.ReadFile(filepath.Join(task.ParentRoot, "tracked"))
			if err != nil {
				t.Fatal(err)
			}
			expected := "base\n"
			if kind == "parent after review" {
				expected = "user after review\n"
			}
			if string(b) != expected {
				t.Fatalf("overwrote parent %q", b)
			}
			if _, err = os.Stat(ws.Root); err != nil {
				t.Fatal("lost workspace")
			}
		})
	}
}
func TestDelegationWorkspaceMetadataEscape(t *testing.T) {
	for _, kind := range []string{"file symlink", "git symlink", "config hook", "partial checkout"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, task, child := workspaceFixture(t)
			if kind == "partial checkout" {
				id := "delegation-" + task.ID + "-" + child.ID
				root := svc.Manager.Path(id)
				os.MkdirAll(root, 0700)
				workspaceWrite(t, root, "recover", "preserve")
				if _, err := svc.Prepare(t.Context(), task, child); err == nil {
					t.Fatal("partial checkout recovered destructively")
				}
				if b, _ := os.ReadFile(filepath.Join(root, "recover")); string(b) != "preserve" {
					t.Fatal("partial work lost")
				}
				return
			}
			ws, err := svc.Prepare(t.Context(), task, child)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file symlink":
				os.Symlink(t.TempDir(), filepath.Join(ws.Root, "escape"))
			case "git symlink":
				os.Symlink(t.TempDir(), filepath.Join(ws.Root, ".git", "escape"))
			case "config hook":
				workspaceGit(t, ws.Root, "config", "core.fsmonitor", "touch /tmp/umcode-workspace-escape")
			}
			if _, err = svc.Snapshot(t.Context(), task, child, ws); err == nil {
				t.Fatal("unsafe child tree/metadata accepted")
			}
			if _, err = os.Stat(ws.Root); err != nil {
				t.Fatal("unsafe workspace discarded")
			}
		})
	}
}

func TestDelegationWorkspaceCleanupCrashRecovery(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "tracked", "result\n")
	final, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, final.ResultRevision)
	svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil }
	if _, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordVerification(t.Context(), task.ID, child.ID, final.ResultRevision, []protocol.AgentCheckEvidence{{Command: "verify", Status: "passed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`CREATE TRIGGER reject_cleaned BEFORE UPDATE ON agent_workspaces WHEN json_extract(NEW.data_json,'$.Cleaned')=1 BEGIN SELECT RAISE(ABORT,'storage failure after discard'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = svc.Cleanup(t.Context(), task.ID, child.ID); err == nil {
		t.Fatal("cleanup storage failure hidden")
	}
	if _, err = os.Stat(ws.Root); !os.IsNotExist(err) {
		t.Fatal("fixture did not reach post-discard failure")
	}
	s.DB.Exec(`DROP TRIGGER reject_cleaned`)
	dbPath := s.Path
	s.Close()
	reopened, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	svc = NewWorkspaceService(reopened, svc.Manager, nil, nil)
	recovered, err := reopened.AgentTask(t.Context(), task.ID)
	if err != nil || recovered.ParentRoot != task.ParentRoot {
		t.Fatalf("persisted parent %+v %v", recovered, err)
	}
	if err = svc.Cleanup(t.Context(), task.ID, child.ID); err != nil {
		t.Fatalf("cleanup recovery failed: %v", err)
	}
	saved, err := reopened.AgentWorkspace(t.Context(), ws.ID)
	if err != nil || !saved.Cleaned {
		t.Fatalf("cleanup not durable %+v %v", saved, err)
	}
}

func TestDelegationWorkspaceCleanupPreservesNewEdits(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "tracked", "result\n")
	final, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, final.ResultRevision)
	svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil }
	if _, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordVerification(t.Context(), task.ID, child.ID, final.ResultRevision, []protocol.AgentCheckEvidence{{Command: "verify", Status: "passed"}}); err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "later-work", "retain")
	if err = svc.Cleanup(t.Context(), task.ID, child.ID); err == nil {
		t.Fatal("cleanup deleted later workspace edits")
	}
	if b, _ := os.ReadFile(filepath.Join(ws.Root, "later-work")); string(b) != "retain" {
		t.Fatal("later work lost")
	}
}

// Git rejects the complete binary patch when even one newly added path collides
// with an ignored user file; it must not apply the other tracked path first.
func TestDelegationWorkspaceConflictIsNotPartial(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	workspaceWrite(t, task.ParentRoot, ".gitignore", "collision\n")
	workspaceGit(t, task.ParentRoot, "add", ".")
	workspaceGit(t, task.ParentRoot, "commit", "-qm", "ignore user output")
	task.BaseRevision = workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD")
	if _, err := s.DB.Exec(`UPDATE agent_tasks SET base_revision=? WHERE id=?`, task.BaseRevision, task.ID); err != nil {
		t.Fatal(err)
	}
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "tracked", "result\n")
	workspaceWrite(t, ws.Root, "collision", "child output")
	workspaceGit(t, ws.Root, "add", "-f", "collision")
	final, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, final.ResultRevision)
	workspaceWrite(t, task.ParentRoot, "collision", "user ignored output")
	svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil }
	if _, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); err == nil {
		t.Fatal("ignored collision overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(task.ParentRoot, "tracked")); string(b) != "base\n" {
		t.Fatal("partial tracked apply")
	}
	if b, _ := os.ReadFile(filepath.Join(task.ParentRoot, "collision")); string(b) != "user ignored output" {
		t.Fatal("ignored user input overwritten")
	}
	if got := workspaceGit(t, task.ParentRoot, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("partial index apply %s", got)
	}
	if e, err := s.AgentIntegration(t.Context(), task.ID, child.ID); err != nil || e.Applied {
		t.Fatalf("lost blocked intent %+v %v", e, err)
	}
}
func TestDelegationWorkspaceConcurrentIntegration(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "tracked", "result\n")
	final, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, final.ResultRevision)
	svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil }
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); errs <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(task.ParentRoot, "tracked")); string(b) != "result\n" {
		t.Fatal("integration result changed")
	}
}

func TestDelegationWorkspaceCancelledTaskCannotIntegrate(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "tracked", "result\n")
	final, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, final.ResultRevision)
	svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil }
	if err = s.UpdateAgentTaskState(t.Context(), task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); err == nil {
		t.Fatal("cancelled task integrated stopped work")
	}
	if b, _ := os.ReadFile(filepath.Join(task.ParentRoot, "tracked")); string(b) != "base\n" {
		t.Fatal("cancelled task changed parent")
	}
}

// Snapshot links must read the bound committed child bytes, even when its
// checkout or the parent changes; guessed paths/owners cannot expose host data.
func TestWorkspaceReadSnapshotScope(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	ref, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ref.Root, "tracked", "child snapshot\n")
	if err = os.Symlink("/etc/passwd", filepath.Join(ref.Root, "escape")); err != nil {
		t.Fatal(err)
	}
	// Snapshot correctly refuses an outside symlink; remove it before finalizing.
	if _, err = svc.Snapshot(t.Context(), task, child, ref); err == nil {
		t.Fatal("outside symlink snapshot accepted")
	}
	os.Remove(filepath.Join(ref.Root, "escape"))
	snap, err := svc.Snapshot(t.Context(), task, child, ref)
	if err != nil {
		t.Fatal(err)
	}
	child = workspaceComplete(t, s, task, child, snap.ResultRevision)
	workspaceWrite(t, ref.Root, "tracked", "later mutable checkout\n")
	got, err := svc.ReadSnapshotFile(t.Context(), task.ID, child.ID, child.Revision, "tracked")
	if err != nil || got.Content != "child snapshot\n" {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
	for _, path := range []string{"../tracked", "/etc/passwd", "./tracked", "a/../tracked", ".git/config", ".aws/credentials", ".env", "tracked:other", "tracked\\other"} {
		if _, err = svc.ReadSnapshotFile(t.Context(), task.ID, child.ID, child.Revision, path); err == nil {
			t.Fatalf("unsafe path %q accepted", path)
		}
	}
	for _, ids := range [][3]string{{"foreign", child.ID, child.Revision}, {task.ID, "foreign", child.Revision}, {task.ID, child.ID, task.BaseRevision}, {task.ID, child.ID, "HEAD"}} {
		if _, err = svc.ReadSnapshotFile(t.Context(), ids[0], ids[1], ids[2], "tracked"); err == nil {
			t.Fatalf("foreign snapshot %v accepted", ids)
		}
	}
	w, _ := s.AgentWorkspace(t.Context(), ref.ID)
	w.Cleaned = true
	if err = s.SaveAgentWorkspace(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ReadSnapshotFile(t.Context(), task.ID, child.ID, child.Revision, "tracked"); err == nil {
		t.Fatal("cleaned snapshot substituted parent")
	}
}
