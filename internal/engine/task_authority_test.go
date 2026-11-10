package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// Catches the original bug: inferred designed depth denying a permitted edit.
func TestTaskAuthority(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	e.Cfg.Models.DesignedWorkflow = true
	w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, WorkflowDepth: "designed", Goal: "Update any requested file"})
	if err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: store.NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Text: "Update notes.txt", Status: protocol.ItemCompleted, CreatedAt: time.Now()}
	if err := st.SaveItem(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	_ = w
	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, e.Cfg, nil, nil)
	write, _ := reg.Get("file.write")
	e.Tools.Add(write)
	root := t.TempDir()
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: root, ThreadID: th.ID})
	args := json.RawMessage(`{"path":"notes.txt","content":"verified edit"}`)
	out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "first", Name: "file.write", Args: args}, nil)
	b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if out.IsError || err != nil || string(b) != "verified edit" {
		t.Fatalf("inferred depth blocked ordinary action: %+v %q %v", out, b, err)
	}
	snap, err := st.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := e.validateTaskRequirement(ctx, th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"no_actions"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_ = req
	args = json.RawMessage(`{"path":"notes.txt","content":"unauthorized"}`)
	out = e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "second", Name: "file.write", Args: args}, nil)
	b, err = os.ReadFile(filepath.Join(root, "notes.txt"))
	if !out.IsError || err != nil || string(b) != "verified edit" {
		t.Fatalf("explicit restriction bypassed: %+v %q %v", out, b, err)
	}
	snap, err = st.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := e.checkTaskAuthority(ctx, th, snap, protocol.TaskOperation{TaskID: snap.Task.ID, Tool: "shell.run", Args: json.RawMessage(`{"command":"echo bypass > notes.txt"}`)})
	if err != nil || decision.Outcome == "allow" {
		t.Fatalf("shell workaround allowed: %+v %v", decision, err)
	}
}
func TestTaskAuthorityReadOnly(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Edit notes.txt")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: t.TempDir(), ReadOnly: true, ThreadID: th.ID})
	decision, err := e.checkTaskAuthority(ctx, th, snap, protocol.TaskOperation{TaskID: snap.Task.ID, Tool: "file.write", Args: json.RawMessage(`{"path":"notes.txt"}`)})
	if err != nil || decision.Outcome != "deny" {
		t.Fatalf("readonly=%+v %v", decision, err)
	}
}

func TestTaskRequirementApproval(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Review before implementation")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)})
	if err != nil {
		t.Fatal(err)
	}
	op := protocol.TaskOperation{TaskID: snap.Task.ID, Tool: "file.write", Args: json.RawMessage(`{"path":"notes.txt"}`)}
	snap, _ = e.Store.ActiveExecutionTask(t.Context(), th.ID)
	decision, err := e.checkTaskAuthority(t.Context(), th, snap, op)
	if err != nil || decision.Outcome != "ask" {
		t.Fatalf("approval not requested: %+v %v", decision, err)
	}
	approval := protocol.Approval{ID: store.NewID("apr"), Kind: "task_requirement", ThreadID: th.ID, NodeID: req.ID, NodeRevision: req.Revision, Status: "pending", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := e.Store.CreateApproval(t.Context(), approval); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.LinkTaskRequirementApproval(t.Context(), req.ID, req.Revision, approval.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.DecideApproval(t.Context(), approval.ID, "denied", "user"); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Store.ActiveExecutionTask(t.Context(), th.ID)
	decision, err = e.checkTaskAuthority(t.Context(), th, snap, op)
	if err != nil || decision.Outcome != "deny" {
		t.Fatalf("rejection bypassed: %+v %v", decision, err)
	}
	// A distinct trusted record is required to approve; retry itself never does.
}

func TestTaskAuthorityChangedWhileWaiting(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	user := protocol.Item{ID: store.NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Text: "Approve before editing", Status: protocol.ItemCompleted, CreatedAt: time.Now()}
	if err := st.SaveItem(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := st.ActiveExecutionTask(t.Context(), th.ID)
	if _, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)}); err != nil {
		t.Fatal(err)
	}
	tool := &engineTestTool{name: "file.write", risk: tools.RiskRed, output: "effect ran"}
	e.Tools.Add(tool)
	result := make(chan toolRunResult, 1)
	go func() {
		result <- e.runTool(t.Context(), t.Context(), th, turn, llm.ToolCall{ID: "wait", Name: "file.write", Args: json.RawMessage(`{"path":"notes.txt","content":"changed"}`)}, nil)
	}()
	deadline := time.After(5 * time.Second)
	var pending protocol.Approval
	for pending.ID == "" {
		select {
		case <-deadline:
			t.Fatal("task approval was not requested")
		default:
		}
		approvals, err := st.ListApprovals(t.Context(), "pending")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range approvals {
			e.mu.Lock()
			live := e.approvals[a.ID] != nil
			e.mu.Unlock()
			if a.Kind == "task_requirement" && live {
				pending = a
				break
			}
		}
		if pending.ID == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := st.DB.ExecContext(t.Context(), `UPDATE threads SET approval_mode='normal' WHERE id=?`, th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), pending.ID, true, false, "user"); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-result:
		if !out.IsError || tool.calls.Load() != 0 {
			t.Fatalf("narrowed permission ignored: %+v effects=%d", out, tool.calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval wait did not finish")
	}
}

func TestTaskAuthorityResourceAliases(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Do not change src yet")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if _, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{"paths":["src"]}`), Condition: json.RawMessage(`{"type":"no_actions"}`)}); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Store.ActiveExecutionTask(t.Context(), th.ID)
	root := t.TempDir()
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: root, ThreadID: th.ID})
	for _, path := range []string{"src/main.go", "./src/main.go", filepath.Join(root, "src/main.go")} {
		args, _ := json.Marshal(map[string]string{"path": path})
		decision, err := e.checkTaskAuthority(ctx, th, snap, protocol.TaskOperation{TaskID: snap.Task.ID, Tool: "file.write", Args: args})
		if err != nil || decision.Outcome != "deny" {
			t.Fatalf("resource alias bypassed requirement: %s %+v %v", path, decision, err)
		}
	}
}

func TestTaskApprovalTransport(t *testing.T) {
	for _, kind := range []string{"workflow", "interpretation"} {
		for _, approve := range []bool{false, true} {
			name := kind + map[bool]string{false: "_reject", true: "_approve"}[approve]
			t.Run(name, func(t *testing.T) {
				e, th, turn, st := pluginHookEngine(t)
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before"), 0600); err != nil {
					t.Fatal(err)
				}
				user := protocol.Item{ID: store.NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Text: "Edit notes.txt", Status: protocol.ItemCompleted, CreatedAt: time.Now()}
				if err := st.SaveItem(t.Context(), user); err != nil {
					t.Fatal(err)
				}
				if _, err := e.resolveTask(t.Context(), th, user); err != nil {
					t.Fatal(err)
				}
				snap, _ := st.ActiveExecutionTask(t.Context(), th.ID)
				var name string
				var args json.RawMessage
				if kind == "workflow" {
					if _, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)}); err != nil {
						t.Fatal(err)
					}
					reg := tools.NewRegistry()
					tools.RegisterBuiltins(reg, e.Cfg, nil, nil)
					tool, _ := reg.Get("file.write")
					e.Tools.Add(tool)
					name = "file.write"
					args = json.RawMessage(`{"path":"notes.txt","content":"after"}`)
				} else {
					e.Tools.Add(&taskProposalTool{engine: e})
					name = "task.propose"
					args, _ = json.Marshal(map[string]any{"task_id": snap.Task.ID, "expected_revision": snap.Task.Revision, "requirement": map[string]any{"origin": "user", "origin_ref": user.ID, "scope": map[string]any{}, "condition": map[string]string{"type": "no_actions"}}})
				}
				ctx := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{Root: root, ThreadID: th.ID})
				done := make(chan toolRunResult, 1)
				go func() {
					done <- e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "approval", Name: name, Args: args}, nil)
				}()
				deadline := time.After(5 * time.Second)
				var pending protocol.Approval
				for pending.ID == "" {
					select {
					case <-deadline:
						t.Fatal("concrete approval was not requested")
					default:
					}
					list, err := st.ListApprovals(t.Context(), "pending")
					if err != nil {
						t.Fatal(err)
					}
					for _, a := range list {
						e.mu.Lock()
						live := e.approvals[a.ID] != nil
						e.mu.Unlock()
						if live {
							pending = a
							break
						}
					}
					if pending.ID == "" {
						time.Sleep(time.Millisecond)
					}
				}
				if _, err := e.RespondApproval(t.Context(), pending.ID, approve, false, "user"); err != nil {
					t.Fatal(err)
				}
				select {
				case result := <-done:
					if result.IsError == approve {
						t.Fatalf("decision ignored: %+v", result)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("approval did not complete")
				}
				b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
				if err != nil {
					t.Fatal(err)
				}
				want := "before"
				if kind == "workflow" && approve {
					want = "after"
				}
				if string(b) != want {
					t.Fatalf("approval effect=%q want=%q", b, want)
				}
				snap, err = st.ActiveExecutionTask(t.Context(), th.ID)
				if err != nil {
					t.Fatal(err)
				}
				count := 1
				if kind == "interpretation" && !approve {
					count = 0
				}
				if len(snap.Requirements) != count {
					t.Fatalf("unconfirmed interpretation persisted: %+v", snap.Requirements)
				}
			})
		}
	}
}

func TestTaskCompletedCannotLendApproval(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Review before changes")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	req, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)})
	if err != nil {
		t.Fatal(err)
	}
	a := protocol.Approval{ID: store.NewID("apr"), Kind: "task_requirement", ThreadID: th.ID, NodeID: req.ID, NodeRevision: req.Revision, Status: "approved", Args: json.RawMessage(`{}`), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := e.Store.CreateApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.LinkTaskRequirementApproval(t.Context(), req.ID, req.Revision, a.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if _, err := e.Store.TransitionExecutionTask(t.Context(), snap.Task.ID, protocol.TaskTransition{ExpectedRevision: snap.Task.Revision, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	current := add("Change another file")
	resolution, err := e.resolveTask(t.Context(), th, current)
	if err != nil || resolution.TaskID != snap.Task.ID {
		t.Fatal("implicit task transition discarded scoped approval requirements", err)
	}
	latest, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	decision, err := e.checkTaskAuthority(t.Context(), th, latest, protocol.TaskOperation{TaskID: latest.Task.ID, Tool: "file.write", Args: json.RawMessage(`{"path":"other.txt"}`)})
	if err != nil || decision.Outcome == "allow" {
		t.Fatalf("completed scope approval lent to new work: %+v %v", decision, err)
	}
}

func TestTaskExplicitArtifactException(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Prepare planning/design.txt but do not change code")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "planning"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: root})
	_, err := e.validateTaskRequirement(ctx, th, protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{"except_paths":["planning/design.txt"]}`), Condition: json.RawMessage(`{"type":"no_actions"}`)})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Store.ActiveExecutionTask(ctx, th.ID)
	check := func(tool, path string, want string) {
		t.Helper()
		args, _ := json.Marshal(map[string]string{"path": path, "content": "draft"})
		d, err := e.checkTaskAuthority(ctx, th, snap, protocol.TaskOperation{TaskID: snap.Task.ID, Tool: tool, Args: args})
		if err != nil || d.Outcome != want {
			t.Fatalf("%s %s: %+v %v", tool, path, d, err)
		}
	}
	check("file.write", "planning/design.txt", "allow")
	check("file.edit", "source.go", "deny")
	check("shell.run", "planning/design.txt", "deny")
	if err := os.Symlink(filepath.Join(root, "source.go"), filepath.Join(root, "planning/design.txt")); err != nil {
		t.Fatal(err)
	}
	check("file.write", "planning/design.txt", "deny")
}
