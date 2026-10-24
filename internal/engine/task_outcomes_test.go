package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
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
			body, err := os.ReadFile(filepath.Join(root, "notes.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := "after"
			if mode == "unrelated" {
				want = "before\n// unrelated comment"
			}
			if string(body) != want {
				t.Fatalf("recorded effect: %q want %q", body, want)
			}
			if finished.Status != protocol.TurnCompleted || snap.Task.Status == "completed" {
				t.Fatalf("response lifecycle certified semantic task completion: turn=%s task=%s", finished.Status, snap.Task.Status)
			}
			verified := false
			for _, op := range snap.Operations {
				if op.Status == "succeeded" && (op.Tool == "file.write" || op.Tool == "file.edit") && op.VerificationStatus == "passed" && len(op.EvidenceIDs) > 0 {
					verified = true
				}
			}
			if mode != "unverified" && !verified {
				t.Fatal("actual read-back evidence was lost")
			}
			if mode == "unverified" && verified {
				t.Fatal("unobserved write acquired read-back evidence")
			}
			if mode == "unverified" && modelDrivenAnswer(t, e, th, finished) != "I successfully updated and verified the file." {
				t.Fatal("engine rewrote the model's conclusion")
			}
		})
	}
}

func TestFailedSearchItemKeepsAdvisoryStatus(t *testing.T) {
	e, th, turn, ctx, _ := expectationEngine(t, "mutation", "file_content")
	out := expectationTool(t, e, th, turn, ctx, "file.search", `{"path":"notes.txt","pattern":"before"}`)
	if !out.IsError {
		t.Fatal("file.search on a file should fail")
	}
	items, err := e.Store.ListItems(ctx, th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind != protocol.ItemToolCall || item.Tool == nil || item.Tool.Name != "file.search" {
			continue
		}
		f := taskItemMetadata(item).ActionFailure
		if f == nil || !f.Advisory || !f.Retryable {
			t.Fatalf("saved tool item lost advisory status: %+v", f)
		}
		return
	}
	t.Fatal("file.search item was not saved")
}

func TestVerifiedRetryPublishesResolvedFailureItem(t *testing.T) {
	e, th, turn, ctx, root := expectationEngine(t, "mutation", "file_content")
	first := expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"later.txt"}`)
	if !first.IsError {
		t.Fatal("missing file read should fail")
	}
	items, err := e.Store.ListItems(ctx, th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	failedID := ""
	for _, item := range items {
		if item.Kind == protocol.ItemToolCall && item.Tool != nil && item.Tool.Name == "file.read" {
			failedID = item.ID
		}
	}
	if failedID == "" {
		t.Fatal("failed read item missing")
	}
	if err := os.WriteFile(filepath.Join(root, "later.txt"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	published := false
	e.Bus.Add(&memorySubscriber{thread: th.ID, notify: func(method string, params any) {
		if method != protocol.NotifyItemCompleted {
			return
		}
		event, ok := params.(protocol.ItemEvent)
		if !ok || event.Item.ID != failedID {
			return
		}
		var data struct {
			ActionFailureResolvedByID string `json:"actionFailureResolvedById"`
		}
		_ = json.Unmarshal(event.Item.Data, &data)
		published = data.ActionFailureResolvedByID != ""
	}})
	second := expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"later.txt"}`)
	if second.IsError {
		t.Fatalf("retry failed: %s", second.Output)
	}
	if !published {
		t.Fatal("verified retry did not publish resolved historical item")
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
	if err != nil || string(b) != strings.Repeat("new content\n", 16000) || snap.Task.Status == "completed" || finished.Status != protocol.TurnCompleted {
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
	if last != "I successfully updated and verified the file." {
		t.Fatalf("model response rewritten: %s", last)
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
			if last != "I successfully updated and verified the file." {
				t.Fatalf("model response rewritten despite preserved failed observation: %s", last)
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
