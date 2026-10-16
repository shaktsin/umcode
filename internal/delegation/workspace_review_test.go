package delegation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func workspaceReady(t *testing.T, svc *WorkspaceService, s *store.Store, task protocol.AgentTask, child protocol.AgentChild, ws WorkspaceRef) WorkspaceSnapshot {
	t.Helper()
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
	return final
}
func workspaceVerified(t *testing.T, svc *WorkspaceService, task protocol.AgentTask, child protocol.AgentChild, rev string) {
	t.Helper()
	if err := svc.RecordVerification(t.Context(), task.ID, child.ID, rev, []protocol.AgentCheckEvidence{{Command: "verify", Status: "passed"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationWorkspaceRejectsHiddenIndexFlags(t *testing.T) {
	for _, flag := range []string{"assume-unchanged", "skip-worktree"} {
		for _, phase := range []string{"admission", "snapshot", "verification", "cleanup"} {
			t.Run(flag+"/"+phase, func(t *testing.T) {
				svc, s, task, child := workspaceFixture(t)
				root := task.ParentRoot
				var ws WorkspaceRef
				var final WorkspaceSnapshot
				var err error
				if phase != "admission" {
					ws, err = svc.Prepare(t.Context(), task, child)
					if err != nil {
						t.Fatal(err)
					}
					root = ws.Root
				}
				if phase == "verification" || phase == "cleanup" {
					final = workspaceReady(t, svc, s, task, child, ws)
					if phase == "verification" {
						root = task.ParentRoot
					} else {
						workspaceVerified(t, svc, task, child, final.ResultRevision)
					}
				}
				workspaceGit(t, root, "update-index", "--"+flag, "tracked")
				workspaceWrite(t, root, "tracked", "hidden required bytes\n")
				before := workspaceGit(t, root, "ls-files", "-v", "--", "tracked")
				if phase != "verification" && workspaceGit(t, root, "status", "--porcelain") != "" {
					t.Fatal("fixture did not hide edited bytes")
				}
				switch phase {
				case "admission":
					_, err = svc.Prepare(t.Context(), task, child)
				case "snapshot":
					_, err = svc.Snapshot(t.Context(), task, child, ws)
				case "verification":
					err = svc.RecordVerification(t.Context(), task.ID, child.ID, final.ResultRevision, []protocol.AgentCheckEvidence{{Command: "verify", Status: "passed"}})
				case "cleanup":
					err = svc.Cleanup(t.Context(), task.ID, child.ID)
				}
				if err == nil {
					t.Fatalf("%s accepted hidden %s edits", phase, flag)
				}
				if !strings.Contains(err.Error(), "index flag") {
					t.Fatalf("missing actionable index flag blocker: %v", err)
				}
				if after := workspaceGit(t, root, "ls-files", "-v", "--", "tracked"); after != before {
					t.Fatalf("user flags changed %q -> %q", before, after)
				}
				if b, _ := os.ReadFile(filepath.Join(root, "tracked")); string(b) != "hidden required bytes\n" {
					t.Fatal("hidden bytes lost")
				}
			})
		}
	}
}
func workspaceIgnoreRule(t *testing.T, s *store.Store, task *protocol.AgentTask) {
	t.Helper()
	workspaceWrite(t, task.ParentRoot, ".gitignore", "valuable-*\ncollision\n")
	workspaceGit(t, task.ParentRoot, "add", ".")
	workspaceGit(t, task.ParentRoot, "commit", "-qm", "ignore generated files")
	task.BaseRevision = workspaceGit(t, task.ParentRoot, "rev-parse", "HEAD")
	if _, err := s.DB.Exec(`UPDATE agent_tasks SET base_revision=? WHERE id=?`, task.BaseRevision, task.ID); err != nil {
		t.Fatal(err)
	}
}
func TestDelegationWorkspaceCleanupPreservesIgnoredFiles(t *testing.T) {
	for _, kind := range []string{"implementer", "reviewer"} {
		t.Run(kind, func(t *testing.T) {
			svc, s, task, child := workspaceFixture(t)
			workspaceIgnoreRule(t, s, &task)
			ws, err := svc.Prepare(t.Context(), task, child)
			if err != nil {
				t.Fatal(err)
			}
			final := workspaceReady(t, svc, s, task, child, ws)
			root := ws.Root
			if kind == "reviewer" {
				reviewer, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "review"}}, "ignored-review")
				if err != nil {
					t.Fatal(err)
				}
				if err = svc.BindReviewer(t.Context(), task.ID, reviewer.ID, child.ID); err != nil {
					t.Fatal(err)
				}
				review, err := svc.Prepare(t.Context(), task, reviewer)
				if err != nil {
					t.Fatal(err)
				}
				root = review.Root
			}
			workspaceVerified(t, svc, task, child, final.ResultRevision)
			workspaceWrite(t, root, "valuable-later", "user bytes")
			if workspaceGit(t, root, "status", "--porcelain") != "" {
				t.Fatal("fixture did not create ignored bytes")
			}
			if err = svc.Cleanup(t.Context(), task.ID, child.ID); err == nil {
				t.Fatal("cleanup deleted later ignored work")
			} else if !strings.Contains(err.Error(), "ignored") {
				t.Fatalf("missing ignored-content blocker: %v", err)
			}
			if b, _ := os.ReadFile(filepath.Join(root, "valuable-later")); string(b) != "user bytes" {
				t.Fatal("ignored bytes lost")
			}
			if _, err = os.Stat(ws.Root); err != nil {
				t.Fatal("cleanup failure lost implementer workspace")
			}
		})
	}
}

// A correction keeps its child identity; a conflict-only R1 intent may not
// prevent a new eligible R2 after the parent is proven unchanged under lock.
func TestDelegationWorkspaceCorrectedIntent(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	workspaceIgnoreRule(t, s, &task)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Root, "tracked", "first result\n")
	workspaceWrite(t, ws.Root, "collision", "child collision")
	workspaceGit(t, ws.Root, "add", "-f", "collision")
	first, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, first.ResultRevision)
	review1, err := svc.Review(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, task.ParentRoot, "collision", "user ignored bytes")
	eligible := first.ResultRevision
	svc.Eligibility = func(_ context.Context, _ protocol.AgentTask, _ protocol.AgentChild, revision string) error {
		if revision != eligible {
			return context.Canceled
		}
		return nil
	}
	if _, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); err == nil {
		t.Fatal("R1 conflict did not block")
	}
	old, err := s.AgentIntegration(t.Context(), task.ID, child.ID)
	if err != nil || old.Applied || old.ResultRevision != first.ResultRevision {
		t.Fatalf("R1 intent %+v %v", old, err)
	}
	latest, err := s.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest.State = protocol.AgentStateQueued
	latest.CorrectionRounds++
	if err = s.UpdateAgentChild(t.Context(), latest, latest.Version); err != nil {
		t.Fatal(err)
	}
	latest, err = s.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest.State = protocol.AgentStateRunning
	latest.Revision = ""
	if err = s.UpdateAgentChild(t.Context(), latest, latest.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Prepare(t.Context(), task, latest); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(ws.Root, "collision"))
	workspaceWrite(t, ws.Root, "tracked", "corrected result\n")
	second, err := svc.Snapshot(t.Context(), task, latest, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, latest, second.ResultRevision)
	eligible = second.ResultRevision
	result, err := svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision)
	if err != nil {
		t.Fatalf("corrected stable child blocked: %v", err)
	}
	if result.ResultRevision != second.ResultRevision || !result.Applied {
		t.Fatalf("wrong corrected result %+v", result)
	}
	if len(result.PreviousAttempts) != 1 || result.PreviousAttempts[0].ResultRevision != first.ResultRevision || result.PreviousAttempts[0].Applied {
		t.Fatalf("old recovery intent lost %+v", result)
	}
	saved, err := s.AgentIntegration(t.Context(), task.ID, child.ID)
	if err != nil || len(saved.PreviousAttempts) != 1 || saved.PreviousAttempts[0].ResultRevision != first.ResultRevision {
		t.Fatalf("old intent not durable %+v %v", saved, err)
	}
	if _, err = s.SupersedeAgentIntegration(t.Context(), task.ID, child.ID, first.ResultRevision, IntegrationEvidence{BaseRevision: task.BaseRevision, ResultRevision: "stale-replacement"}); err == nil {
		t.Fatal("stale CAS superseded current applied result")
	}

	review2, err := svc.Review(t.Context(), child.ID)
	if err != nil {
		t.Fatalf("fresh inspection blocked: %v", err)
	}
	if review1.ID == review2.ID || review1.Root == review2.Root {
		t.Fatal("inspection retargeted existing copy")
	}
	if workspaceGit(t, review1.Root, "rev-parse", "HEAD") != first.ResultRevision || workspaceGit(t, review2.Root, "rev-parse", "HEAD") != second.ResultRevision {
		t.Fatal("inspection revisions lost")
	}
	if b, _ := os.ReadFile(filepath.Join(task.ParentRoot, "collision")); string(b) != "user ignored bytes" {
		t.Fatal("R2 overwrote ignored user data")
	}
}

func workspaceCorrect(t *testing.T, svc *WorkspaceService, s *store.Store, task protocol.AgentTask, child protocol.AgentChild, ws WorkspaceRef) WorkspaceSnapshot {
	t.Helper()
	c, err := s.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.State = protocol.AgentStateQueued
	c.CorrectionRounds++
	if err = s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
		t.Fatal(err)
	}
	c, err = s.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.State = protocol.AgentStateRunning
	c.Revision = ""
	if err = s.UpdateAgentChild(t.Context(), c, c.Version); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(ws.Root, "collision"))
	workspaceWrite(t, ws.Root, "tracked", "corrected result\n")
	second, err := svc.Snapshot(t.Context(), task, c, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, c, second.ResultRevision)
	return second
}
func TestDelegationWorkspaceInspectionPerRevision(t *testing.T) {
	svc, s, task, child := workspaceFixture(t)
	ws, err := svc.Prepare(t.Context(), task, child)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Snapshot(t.Context(), task, child, ws)
	if err != nil {
		t.Fatal(err)
	}
	workspaceComplete(t, s, task, child, first.ResultRevision)
	one, err := svc.Review(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := workspaceCorrect(t, svc, s, task, child, ws)
	two, err := svc.Review(t.Context(), child.ID)
	if err != nil {
		t.Fatalf("new result cannot be inspected: %v", err)
	}
	if one.ID == two.ID || workspaceGit(t, one.Root, "rev-parse", "HEAD") != first.ResultRevision || workspaceGit(t, two.Root, "rev-parse", "HEAD") != second.ResultRevision {
		t.Fatal("prior inspection changed or fresh revision missing")
	}
}
func TestDelegationWorkspaceIntentSupersedeRefusesUnsafeParent(t *testing.T) {
	for _, kind := range []string{"dirty", "partial", "applied", "persistence failure"} {
		t.Run(kind, func(t *testing.T) {
			svc, s, task, child := workspaceFixture(t)
			workspaceIgnoreRule(t, s, &task)
			ws, err := svc.Prepare(t.Context(), task, child)
			if err != nil {
				t.Fatal(err)
			}
			workspaceWrite(t, ws.Root, "tracked", "first result\n")
			if kind != "applied" {
				workspaceWrite(t, ws.Root, "collision", "child collision")
				workspaceGit(t, ws.Root, "add", "-f", "collision")
				workspaceWrite(t, task.ParentRoot, "collision", "user ignored bytes")
			}
			first, err := svc.Snapshot(t.Context(), task, child, ws)
			if err != nil {
				t.Fatal(err)
			}
			workspaceComplete(t, s, task, child, first.ResultRevision)
			svc.Eligibility = func(context.Context, protocol.AgentTask, protocol.AgentChild, string) error { return nil }
			_, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision)
			if kind == "applied" && err != nil {
				t.Fatal(err)
			}
			if kind != "applied" && err == nil {
				t.Fatal("missing conflict")
			}
			workspaceCorrect(t, svc, s, task, child, ws)
			if kind == "dirty" {
				workspaceWrite(t, task.ParentRoot, "tracked", "user edits\n")
			}
			if kind == "partial" {
				workspaceWrite(t, task.ParentRoot, "tracked", "first result\n")
				workspaceGit(t, task.ParentRoot, "add", "tracked")
			}
			if kind == "persistence failure" {
				if _, err := s.DB.Exec(`CREATE TRIGGER reject_supersede BEFORE UPDATE ON agent_integrations BEGIN SELECT RAISE(ABORT,'supersede storage failed'); END;`); err != nil {
					t.Fatal(err)
				}
			}
			before := workspaceGit(t, task.ParentRoot, "status", "--porcelain")
			if _, err = svc.Integrate(t.Context(), task.ID, child.ID, task.BaseRevision); err == nil {
				t.Fatal("unsafe parent allowed intent supersede")
			}
			saved, err := s.AgentIntegration(t.Context(), task.ID, child.ID)
			if err != nil || saved.ResultRevision != first.ResultRevision || saved.Applied != (kind == "applied") {
				t.Fatalf("old intent replaced %+v %v", saved, err)
			}
			if after := workspaceGit(t, task.ParentRoot, "status", "--porcelain"); after != before {
				t.Fatal("blocked supersede modified parent")
			}
		})
	}
}
