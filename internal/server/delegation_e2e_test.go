package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/plugins"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// The only replaced boundary is the paid provider. RPC admission, tools,
// coordinator, hidden turns, Git workspaces, review and lead checks are real.
// Removing the RPC's snapshot scoping, correction identity, actual receipts or
// shared accounting makes these independently derived assertions fail.
func TestDelegationRPCEndToEnd(t *testing.T) {
	for _, activation := range []string{"manual", "installed_skill", "dirty_parent"} {
		t.Run(activation, func(t *testing.T) {
			h := newHarness(t, nil)
			// Positive Go verification needs the parent's normal cache-write
			// permission; this is the same app RPC used by existing Go fixtures.
			h.call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: h.proj.ID, Tools: &protocol.ProjectTools{Network: boolPtr(true)}}, &h.proj)
			for name, content := range map[string]string{
				"go.mod":        "module fixture\ngo 1.24\n",
				"value.go":      "package fixture\nfunc Value() int { return 0 }\n",
				"value_test.go": "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=2 { t.Fatal(Value()) } }\n",
			} {
				writeFixtureFile(t, filepath.Join(h.ws, name), content)
			}
			commitDelegationFixture(t, h)
			h.addKey("claude", "test", "fake")
			var th, other protocol.Thread
			h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "RPC acceptance"}, &th)
			h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "foreign"}, &other)
			if err := h.eng.Store.UpdateThread(h.ctx, th.ID, map[string]any{"approval_mode": "auto_all"}); err != nil {
				t.Fatal(err)
			}
			params := protocol.TurnStartParams{ThreadID: th.ID, Text: "Set Value to 2 using implementation and independent review"}
			if activation == "installed_skill" {
				root := t.TempDir()
				dir := filepath.Join(root, "skills", "subagent-driven-development")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"superpowers","version":"1.0.0"}`)
				writeFixtureFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: subagent-driven-development\ndescription: Delegates work\n---\nUse subagents.")
				installServerPlugin(t, h, root, plugins.InstallManaged)
				params.Text = "/superpowers:subagent-driven-development " + params.Text
			} else {
				var enabled protocol.DelegationEnableResult
				h.call(protocol.MethodDelegationEnable, protocol.ThreadIDParams{ThreadID: th.ID}, &enabled)
				params.DelegationActivationToken = enabled.ActivationToken
			}
			var mu sync.Mutex
			counts := map[string]int{}
			var taskID string
			script := func(req llm.Request, _ string) ([]llm.Event, error) {
				mu.Lock()
				defer mu.Unlock()
				role := "implementer"
				for _, spec := range req.Tools {
					if spec.Name == tools.ToWire("agent.spawn") {
						role = "lead"
						break
					}
					if spec.Name == tools.ToWire("agent.review_report") {
						role = "reviewer"
					}
				}
				n := counts[role]
				counts[role]++
				done := func() ([]llm.Event, error) {
					return []llm.Event{{Type: llm.EventTextDelta, Text: "Retained acceptance result"}, {Type: llm.EventDone, Usage: llm.Usage{InputTokens: 11, OutputTokens: 3, Reported: true}}}, nil
				}
				call := func(name string, args any) ([]llm.Event, error) {
					b, _ := json.Marshal(args)
					return []llm.Event{{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: fmt.Sprintf("%s-%d", role, n), Name: tools.ToWire(name), Args: b}}, {Type: llm.EventDone, Usage: llm.Usage{InputTokens: 11, OutputTokens: 3, Reported: true}}}, nil
				}
				task, err := h.eng.Store.ActiveAgentTask(h.ctx, th.ID, h.proj.ID)
				if err != nil { // Completion releases the active task before the final answer.
					if role == "lead" && n == 13 {
						return done()
					}
					return nil, err
				}
				taskID = task.ID
				children, err := h.eng.Store.AgentChildren(h.ctx, task.ID)
				if err != nil {
					return nil, err
				}
				var impl, review protocol.AgentChild
				for _, c := range children {
					if c.Role == protocol.AgentRoleImplementer {
						impl = c
					} else if review.ID == "" || c.CreatedAt.After(review.CreatedAt) {
						review = c
					}
				}
				if role == "implementer" {
					if n == 0 || n == 2 {
						return call("file.write", map[string]any{"path": "value.go", "content": fmt.Sprintf("package fixture\nfunc Value() int { return %d }\n", 1+n/2)})
					}
					return done()
				}
				if role == "reviewer" {
					switch n % 3 {
					case 0:
						return call("file.read", map[string]any{"path": "value.go"})
					case 1:
						last := req.Messages[len(req.Messages)-1]
						if last.IsError || !strings.Contains(last.Result, "func Value") {
							t.Errorf("reviewer could not inspect committed change: %s", last.Result)
						}
						verdict := "approved"
						findings := []protocol.AgentFinding{}
						if n < 3 {
							verdict = "changes_requested"
							findings = append(findings, protocol.AgentFinding{Path: "value.go", Line: 2, Severity: "important", Message: "Value must return 2"})
						}
						return call("agent.review_report", map[string]any{"revision": impl.Revision, "verdict": verdict, "findings": findings})
					default:
						return done()
					}
				}
				// Failure boundaries must actually fail through the public tool path.
				if n == 13 {
					last := req.Messages[len(req.Messages)-1]
					if last.IsError {
						t.Logf("final completion response: %s", last.Result)
					}
				}
				if n == 5 || n == 11 {
					last := req.Messages[len(req.Messages)-1]
					if !last.IsError {
						t.Errorf("step %d accepted premature integration/completion: %s", n, last.Result)
					}
				}
				switch n {
				case 0:
					return call("agent.spawn", map[string]any{"role": "implementer", "assignment": map[string]any{"prompt": "Set Value to 2", "acceptanceChecks": []string{"Value returns 2; tests pass"}}})
				case 1, 6:
					return call("agent.wait", map[string]any{"ids": []string{impl.ID}})
				case 2, 7:
					return call("agent.spawn", map[string]any{"role": "reviewer", "assignment": map[string]any{"prompt": "Independently review Value"}})
				case 3, 8:
					return call("agent.wait", map[string]any{"ids": []string{review.ID}})
				case 4:
					return call("agent.integrate", map[string]any{})
				case 5:
					return call("agent.followup", map[string]any{"id": impl.ID, "message": "Fix the finding: Value must return 2"})
				case 9:
					// Positive file transport is RPC-scoped and reads the committed blob,
					// even after the retained checkout has different mutable bytes.
					p := protocol.DelegationFileParams{ThreadID: th.ID, TaskID: task.ID, ChildID: impl.ID, Revision: impl.Revision, Path: "value.go"}
					var file protocol.ProjectReadFileResult
					if err = h.c.Call(h.ctx, "delegation/file", p, &file); err != nil {
						return nil, err
					}
					if file.Content != "package fixture\nfunc Value() int { return 2 }\n" {
						t.Errorf("wrong committed snapshot: %+v", file)
					}
					workspace, workspaceErr := h.eng.Store.AgentWorkspaceForChild(h.ctx, impl.ID, "implementer")
					if workspaceErr != nil {
						return nil, workspaceErr
					}
					if err = os.WriteFile(filepath.Join(workspace.Root, "value.go"), []byte("mutable checkout bytes\n"), 0600); err != nil {
						return nil, err
					}
					var retained protocol.ProjectReadFileResult
					readErr := h.c.Call(h.ctx, "delegation/file", p, &retained)
					restoreErr := os.WriteFile(filepath.Join(workspace.Root, "value.go"), []byte(file.Content), 0600)
					if readErr != nil {
						return nil, readErr
					}
					if restoreErr != nil {
						return nil, restoreErr
					}
					if retained.Content != file.Content {
						t.Errorf("snapshot RPC followed mutable checkout: %+v", retained)
					}
					p.ThreadID = other.ID
					if err = h.c.Call(h.ctx, "delegation/file", p, nil); err == nil {
						t.Error("foreign file read accepted")
					}
					if activation == "dirty_parent" {
						if err = os.WriteFile(filepath.Join(h.ws, "value.go"), []byte("user edit retained\n"), 0600); err != nil {
							return nil, err
						}
					}
					return call("agent.integrate", map[string]any{})
				case 10:
					if activation == "dirty_parent" {
						last := req.Messages[len(req.Messages)-1]
						if !last.IsError {
							t.Errorf("dirty parent integrated: %s", last.Result)
						}
						return done()
					}
					return call("agent.complete", map[string]any{})
				case 11:
					workflow, err := h.eng.Store.AgentWorkflow(h.ctx, task.ID)
					if err != nil {
						return nil, err
					}
					checks := []map[string]any{}
					for _, c := range workflow.Checks {
						checks = append(checks, map[string]any{"label": c.Label, "command": c.Command, "required_command": c.RequiredCommand, "directory": c.Directory})
					}
					return call("verification.run", map[string]any{"checks": checks})
				case 12:
					return call("agent.complete", map[string]any{})
				default:
					return done()
				}
			}
			// Bounded scripts prevent an unexpected paid execution loop being hidden by
			// the harness's ordinary fallback answer.
			for i := 0; i < 30; i++ {
				h.fake.push(script)
			}
			var started protocol.TurnStartResult
			h.call(protocol.MethodTurnStart, params, &started)
			turn, _, _ := h.waitTurnWithin(started.Turn.ID, func(protocol.Approval) bool { return true }, 90*time.Second)
			wantStatus := protocol.TurnCompleted

			if turn.Status != wantStatus {
				t.Fatalf("lead status: %+v", turn)
			}
			var list protocol.DelegationListResult
			h.call("delegation/list", protocol.DelegationListParams{ThreadID: th.ID, TaskID: taskID}, &list)
			if len(list.Tasks) != 1 {
				t.Fatalf("missing RPC snapshot: %+v", list)
			}
			snapshot := list.Tasks[0]
			if len(snapshot.Children) != 3 {
				t.Fatalf("correction replaced implementer or review identity: %+v", snapshot.Children)
			}
			wantActivation := protocol.AgentActivationExplicitUser
			if activation == "installed_skill" {
				wantActivation = protocol.AgentActivationInstalledSkill
			}
			if snapshot.Task.Activation != wantActivation || snapshot.Task.BaseRoot != "" || snapshot.Task.ParentRoot != "" {
				t.Fatalf("wrong admission or exposed root: %+v", snapshot.Task)
			}
			for _, child := range snapshot.Children {
				if child.State != protocol.AgentStateCompleted || child.WorkspaceID != "" {
					t.Errorf("wrong/redaction child: %+v", child)
				}
				if child.Role == protocol.AgentRoleImplementer && child.CorrectionRounds != 1 {
					t.Errorf("same-child correction missing: %+v", child)
				}
				if child.Role == protocol.AgentRoleReviewer && (child.Result.Review == nil || child.Result.Review.Revision == "") {
					t.Errorf("structured exact review missing: %+v", child)
				}
			}
			wantCalls := 24
			wantLead := 14
			if activation == "dirty_parent" {
				wantCalls = 21
				wantLead = 11
				if snapshot.Task.State == protocol.AgentStateCompleted {
					t.Fatal("dirty parent completed")
				}
				b, err := os.ReadFile(filepath.Join(h.ws, "value.go"))
				if err != nil || string(b) != "user edit retained\n" {
					t.Fatalf("lost parent edits: %q %v", b, err)
				}
			} else {
				if snapshot.Task.State != protocol.AgentStateCompleted || snapshot.Workflow == nil || len(snapshot.Workflow.Checks) != 2 {
					evidence, _ := json.Marshal(snapshot)
					t.Fatalf("missing lead verification: %s", evidence)
				}
				for _, check := range snapshot.Workflow.Checks {
					if check.Status != "passed" {
						t.Errorf("incomplete required check: %+v", check)
					}
				}
			}
			mu.Lock()
			gotCounts := fmt.Sprint(counts)
			countsOK := counts["lead"] == wantLead && counts["implementer"] == 4 && counts["reviewer"] == 6
			mu.Unlock()
			h.fake.mu.Lock()
			calls := len(h.fake.calls)
			intentCalls := len(h.fake.intentCalls)
			h.fake.mu.Unlock()
			if !countsOK || calls != wantCalls {
				t.Errorf("unexpected provider attempts: %d %s, want %d", calls, gotCounts, wantCalls)
			}
			// Shared usage includes the lead's final answer even after durable completion.
			if !snapshot.BudgetAvailable || snapshot.Usage.Requests != int64(wantCalls+intentCalls) || snapshot.Usage.InputTokens != int64(wantCalls*11+intentCalls*5) || snapshot.Usage.OutputTokens != int64(wantCalls*3+intentCalls*5) {
				t.Errorf("shared actual usage: %+v", snapshot)
			}
			h.call("delegation/cancel", protocol.DelegationCancelParams{ThreadID: th.ID, TaskID: taskID}, nil)
			h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "no restart", DelegationTaskID: taskID})
			h.fake.mu.Lock()
			after := len(h.fake.calls)
			h.fake.mu.Unlock()
			if after != wantCalls {
				t.Errorf("cancel/resume reached provider: %d", after)
			}
			t.Logf("%s: calls=%d (%s), shared tokens=%d, children=3, exact checks verified=%t", activation, calls, gotCounts, snapshot.Usage.InputTokens+snapshot.Usage.OutputTokens, activation != "dirty_parent")
		})
	}
}

func TestDelegationRPCUnavailableRouting(t *testing.T) {
	h := newHarness(t, nil)
	commitDelegationFixture(t, h)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "no keys"}, &th)
	var enabled protocol.DelegationEnableResult
	h.call(protocol.MethodDelegationEnable, protocol.ThreadIDParams{ThreadID: th.ID}, &enabled)
	h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: enabled.ActivationToken})
	var snapshot protocol.DelegationListResult
	h.call("delegation/list", protocol.DelegationListParams{ThreadID: th.ID}, &snapshot)
	h.fake.mu.Lock()
	calls := len(h.fake.calls)
	h.fake.mu.Unlock()
	if calls != 0 || len(snapshot.Tasks) != 0 {
		t.Fatalf("unavailable routing executed/admitted: calls=%d tasks=%+v", calls, snapshot.Tasks)
	}
}

func TestDelegationRPCCancelReleasesApprovalAndWait(t *testing.T) {
	h := newHarness(t, nil)
	commitDelegationFixture(t, h)
	h.addKey("claude", "test", "fake")
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "cancel acceptance"}, &th)
	var enabled protocol.DelegationEnableResult
	h.call(protocol.MethodDelegationEnable, protocol.ThreadIDParams{ThreadID: th.ID}, &enabled)
	var mu sync.Mutex
	leadCalls := 0
	script := func(req llm.Request, _ string) ([]llm.Event, error) {
		mu.Lock()
		defer mu.Unlock()
		lead := false
		for _, spec := range req.Tools {
			if spec.Name == tools.ToWire("agent.spawn") {
				lead = true
			}
		}
		if !lead {
			return toolReply("shell__run", `{"command":"printf unapproved > never.txt"}`)(req, "")
		}
		leadCalls++
		if leadCalls == 1 {
			return toolReply("agent__spawn", `{"role":"implementer","assignment":{"prompt":"Wait for command permission"}}`)(req, "")
		}
		task, err := h.eng.Store.ActiveAgentTask(h.ctx, th.ID, h.proj.ID)
		if err != nil {
			return nil, err
		}
		children, err := h.eng.Store.AgentChildren(h.ctx, task.ID)
		if err != nil || len(children) != 1 {
			return nil, fmt.Errorf("spawn snapshot: %+v %v", children, err)
		}
		return toolReply("agent__wait", fmt.Sprintf(`{"ids":[%q]}`, children[0].ID))(req, "")
	}
	for i := 0; i < 6; i++ {
		h.fake.push(script)
	}
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Implement bounded work", DelegationActivationToken: enabled.ActivationToken}, &started)
	var approval protocol.Approval
	deadline := time.After(15 * time.Second)
	for approval.ID == "" {
		select {
		case n := <-h.c.Notifications():
			if n.Method == protocol.NotifyApprovalRequest {
				var event protocol.ApprovalEvent
				if err := json.Unmarshal(n.Params, &event); err != nil {
					t.Fatal(err)
				}
				approval = event.Approval
			}
		case <-deadline:
			t.Fatal("no attributed child approval")
		}
	}
	if approval.ThreadID != th.ID || approval.AgentTaskID == "" || approval.AgentChildID == "" {
		t.Fatalf("approval attribution: %+v", approval)
	}
	var cancelled protocol.DelegationCancelResult
	h.call("delegation/cancel", protocol.DelegationCancelParams{ThreadID: th.ID, TaskID: approval.AgentTaskID}, &cancelled)
	if cancelled.Task.State != protocol.AgentStateCancelled {
		t.Fatalf("cancellation acknowledgement: %+v", cancelled)
	}
	// The parent was waiting on the actual child; cancellation must release both.
	turn, _, _ := h.waitTurnWithin(started.Turn.ID, nil, 90*time.Second)
	if turn.Status == protocol.TurnRunning {
		t.Fatalf("parent wait remained live: %+v", turn)
	}
	h.callErr(protocol.MethodApprovalRespond, protocol.ApprovalRespondParams{ApprovalID: approval.ID, Approve: true})
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	children, err := h.eng.DelegationCoordinator.Wait(ctx, approval.AgentTaskID, []string{approval.AgentChildID})
	if err != nil || len(children) != 1 || children[0].State != protocol.AgentStateCancelled {
		t.Fatalf("cancelled child not released: %+v %v", children, err)
	}
	workspace, err := h.eng.Store.AgentWorkspaceForChild(h.ctx, approval.AgentChildID, "implementer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(workspace.Root, "never.txt")); !os.IsNotExist(err) {
		t.Fatalf("unapproved command executed: %v", err)
	}
	h.fake.mu.Lock()
	calls := len(h.fake.calls)
	h.fake.mu.Unlock()
	if calls != 3 {
		t.Fatalf("paid execution continued after approval cancellation: %d requests", calls)
	}
	var list protocol.DelegationListResult
	h.call("delegation/list", protocol.DelegationListParams{ThreadID: th.ID, TaskID: approval.AgentTaskID}, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].Usage.Requests != 3 || list.Tasks[0].Task.State != protocol.AgentStateCancelled {
		t.Fatalf("cancelled retained accounting: %+v", list)
	}
}
