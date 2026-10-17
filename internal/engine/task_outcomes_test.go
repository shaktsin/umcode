package engine

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestTaskOutcomes(t *testing.T) {
	for _, mode := range []string{"write_verified", "edit_conflict", "alternate_write", "unrelated", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			e, th, root, _ := taskRuntimeFixture(t, mode, false)
			finished := runTaskRuntimeTurn(t, e, th, "retry")
			snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unrelated" {
				if finished.Status != protocol.TurnFailed || string(b) != "before\n// unrelated comment" || snap.Task.Status != "blocked" {
					t.Fatalf("unrelated success concealed pending edit: %+v %+v file=%q", finished, snap.Task, b)
				}
				return
			}
			if string(b) != "after" {
				t.Fatalf("effect absent: %q", b)
			}
			if mode == "unverified" {
				if snap.Task.Status == "completed" {
					t.Fatal("unverified effect completed the task")
				}
				items, err := e.Store.ListItems(t.Context(), th.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				last := ""
				for _, item := range items {
					if item.Kind == protocol.ItemAgentMessage {
						last = item.Text
					}
				}
				if strings.Contains(last, "successfully updated and verified") || !strings.Contains(strings.ToLower(last), "verification") {
					t.Fatalf("unverified success not corrected: %q", last)
				}
				return
			}
			if finished.Status != protocol.TurnCompleted || snap.Task.Status != "completed" {
				t.Fatalf("verified outcome remained blocked: %+v %+v", finished, snap.Task)
			}
			verified := false
			for _, op := range snap.Operations {
				if op.Status == "succeeded" && (op.Tool == "file.write" || op.Tool == "file.edit") && op.VerificationStatus == "passed" && len(op.EvidenceIDs) > 0 {
					verified = true
				}
			}
			if !verified {
				t.Fatal("completion lacks recorded read-back evidence")
			}
		})
	}
}

func TestDelegatedTaskRecovery(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Work within the approved task scope")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	parentRoot := t.TempDir()
	childRoot := t.TempDir()
	x := &childExecution{task: protocol.AgentTask{ID: "delegated", ThreadID: th.ID, RootTurnID: user.TurnID, ParentRoot: parentRoot}, child: protocol.AgentChild{Role: protocol.AgentRoleReviewer}, workspace: ChildWorkspace{Root: childRoot}}
	ctx := withChildExecution(t.Context(), x)
	ctx = tools.WithScope(ctx, &tools.Scope{Root: childRoot, ReadOnly: true, RestrictedChild: true})
	op := protocol.TaskOperation{Tool: "file.write", Args: json.RawMessage(`{"path":"notes.txt","content":"after"}`)}
	decision, err := e.checkChildTaskAuthority(ctx, op)
	if err != nil || decision.Outcome != "deny" {
		t.Fatalf("readonly child=%+v %v", decision, err)
	}
	if childToolAllowed(ctx, "task.propose") {
		t.Fatal("child can change parent task requirements")
	}
	if _, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"no_actions"}`)}); err != nil {
		t.Fatal(err)
	}
	x.child.Role = protocol.AgentRoleImplementer
	ctx = tools.WithScope(withChildExecution(t.Context(), x), &tools.Scope{Root: childRoot, RestrictedChild: true})
	decision, err = e.checkChildTaskAuthority(ctx, op)
	if err != nil || decision.Outcome != "deny" {
		t.Fatalf("parent restriction not inherited=%+v %v", decision, err)
	}
	// Rebuilding context simulates provider/compaction changes: the store remains authoritative.
	fresh := tools.WithScope(withChildExecution(t.Context(), x), &tools.Scope{Root: childRoot, RestrictedChild: true})
	decision, err = e.checkChildTaskAuthority(fresh, op)
	if err != nil || decision.Outcome != "deny" {
		t.Fatalf("fresh context lost parent restriction=%+v %v", decision, err)
	}
}

func TestTaskLargeWriteRuntime(t *testing.T) {
	e, th, root, _ := taskRuntimeFixture(t, "large", false)
	finished := runTaskRuntimeTurn(t, e, th, "retry")
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil || string(b) != strings.Repeat("new content\n", 16000) || snap.Task.Status != "completed" || finished.Status != protocol.TurnCompleted {
		t.Fatalf("large write: %v %v %+v", err, finished, snap.Task)
	}
	if snap.Operations[0].ArgsRef == "" {
		t.Fatal("large args not archived")
	}
}
func TestTaskInvalidCallCannotComplete(t *testing.T) {
	e, th, root, _ := taskRuntimeFixture(t, "invalid", false)
	runTaskRuntimeTurn(t, e, th, "retry")
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	b, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
	if snap.Task.Status == "completed" || string(b) != "before" {
		t.Fatalf("invalid call completed: %+v %q", snap.Task, b)
	}
	items, _ := e.Store.ListItems(t.Context(), th.ID, 0)
	last := ""
	for _, it := range items {
		if it.Kind == protocol.ItemAgentMessage {
			last = it.Text
		}
	}
	if strings.Contains(last, "successfully") {
		t.Fatalf("false completion: %s", last)
	}
}

func TestTaskOutcomesBlockedReadback(t *testing.T) {
	for _, mode := range []string{"read_denied", "read_failed"} {
		t.Run(mode, func(t *testing.T) {
			e, th, root, p := taskRuntimeFixture(t, mode, false)
			if mode == "read_failed" {
				p.beforeRead = func() {
					if err := os.Remove(filepath.Join(root, "notes.txt")); err != nil {
						t.Fatal(err)
					}
				}
			}
			runTaskRuntimeTurn(t, e, th, "retry")
			snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
			if snap.Task.Status == "completed" {
				t.Fatal("blocked readback completed task")
			}
			if mode == "read_failed" {
				found := false
				for _, op := range snap.Operations {
					if op.Tool == "file.read" && op.Failure != nil && op.Failure.Category == "verification_failed" {
						found = true
					}
				}
				if !found {
					t.Fatal("failed readback did not record verification_failed")
				}
			}
			write := snap.Operations[0]
			if write.Status != "succeeded" || write.VerificationStatus == "passed" {
				t.Fatalf("lost partial effect: %+v", write)
			}
			items, _ := e.Store.ListItems(t.Context(), th.ID, 0)
			last := ""
			for _, it := range items {
				if it.Kind == protocol.ItemAgentMessage {
					last = it.Text
				}
			}
			if strings.Contains(last, "successfully") || !strings.Contains(last, "verification") {
				t.Fatalf("unsupported partial summary: %s", last)
			}
			if mode == "read_denied" {
				b, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
				if string(b) != "after" {
					t.Fatalf("mutation lost: %q", b)
				}
			}
		})
	}
}
