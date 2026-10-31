package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/plugins"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestWorkbenchAssistantReviewPreservesWorkAndPendingGates(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed_proposal_observation", true: "pending_coding_approval"}[pending], func(t *testing.T) {
			h, th, c := assistantRPCFixture(t)
			w, err := h.eng.Store.CreateWork(h.ctx, protocol.Work{ThreadID: th.ID, ProjectID: h.proj.ID, Goal: "Existing coding work", WorkflowDepth: protocol.DepthDirect})
			if err != nil {
				t.Fatal(err)
			}
			if pending {
				h.eng.Cfg.Policy.RequireWorkflowApproval = true
				if err = h.eng.Store.SetWorkDepth(h.ctx, w.ID, protocol.DepthDesigned); err != nil {
					t.Fatal(err)
				}
				_, err = h.eng.Store.AddWorkNode(h.ctx, protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeDecision, Title: "Review the coding decision", Status: protocol.StatusProposed, Content: json.RawMessage(`{"required":true,"gate_kind":"security"}`)})
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := h.eng.Store.GetWorkDetail(h.ctx, w.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected, _ := json.Marshal(before)
			args := `{"explanation":"Malformed suggestion","operations":[]}`
			if pending {
				args = `{"explanation":"Widen projection","operations":[{"type":"update","nodeId":"dense","config":{"outFeatures":8}}]}`
			}
			h.fake.push(toolReply("workbench__propose_edit", args), textReply("This is architecture guidance only."))
			var started protocol.TurnStartResult
			h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Help with this layer", Workbench: &c}, &started)
			_, _, items := h.waitTurn(started.Turn.ID, func(a protocol.Approval) bool {
				t.Errorf("architecture suggestion reached an unrelated coding approval: %s", a.Kind)
				return false
			})
			after, err := h.eng.Store.GetWorkDetail(h.ctx, w.ID)
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := json.Marshal(after)
			if string(actual) != string(expected) {
				t.Fatalf("architecture proposal changed coding Work/evidence\nbefore=%s\nafter=%s", expected, actual)
			}
			if pending {
				found := false
				for _, item := range items {
					if item.Status == protocol.ItemCompleted && item.Tool != nil && item.Tool.Name == "workbench.propose_edit" {
						found = true
					}
				}
				if !found {
					t.Fatal("coding gate blocked an advisory proposal")
				}
			}
		})
	}
}

func TestWorkbenchAssistantReviewSuppressesExecutablePluginHooks(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"assistant-hook-fixture","version":"1.0.0","extensions":{"com.openai":{"hooks":"./hooks/hooks.json"}}}`)
	marker := filepath.Join(h.ws, "hook-marker.log")
	events := map[string]any{}
	for _, event := range []string{"TurnStart", "BeforeToolUse", "AfterToolUse", "ToolUseFailed", "TurnComplete"} {
		events[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "./helper", "env": map[string]string{"UMCODE_PLUGIN_TEST_HELPER": "hook", "UMCODE_PLUGIN_TEST_MARKER": marker}}}}}
	}
	raw, _ := json.Marshal(map[string]any{"hooks": events})
	writeFixtureFile(t, filepath.Join(root, "hooks", "hooks.json"), string(raw))
	copyFixtureExecutable(t, filepath.Join(root, "helper"))
	installed := installServerPlugin(t, h, root, plugins.InstallManaged)
	scripts := [][]func(llm.Request, string) ([]llm.Event, error){
		{textReply("This layer projects feature vectors.")},
		{toolReply("workbench__propose_edit", `{"explanation":"Widen","operations":[{"type":"update","nodeId":"dense","config":{"outFeatures":8}}]}`), textReply("Preview the suggestion.")},
		{toolReply("workbench__propose_edit", `{"explanation":"invalid","operations":[]}`), textReply("I could not propose that change.")},
		{func(llm.Request, string) ([]llm.Event, error) { return nil, context.Canceled }},
	}
	for _, script := range scripts {
		h.fake.push(script...)
		var started protocol.TurnStartResult
		h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain this model layer", Workbench: &c}, &started)
		h.waitTurn(started.Turn.ID, nil)
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("a workbench question executed an installed plugin command")
		}
		runs, err := h.eng.Store.ListPluginHookRuns(h.ctx, installed.ID, h.proj.ID, started.Turn.ID, 100)
		if err != nil || len(runs) != 0 {
			t.Fatalf("workbench hook audit: %+v %v", runs, err)
		}
	}
	h.fake.push(toolReply("file__list", `{}`), textReply("Ordinary chat retains its hooks."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "List this project"}, &started)
	h.waitTurn(started.Turn.ID, nil)
	runs, err := h.eng.Store.ListPluginHookRuns(h.ctx, installed.ID, h.proj.ID, started.Turn.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, run := range runs {
		seen[run.Event] = true
	}
	for _, event := range []string{"TurnStart", "BeforeToolUse", "AfterToolUse", "TurnComplete"} {
		if !seen[event] {
			t.Fatalf("ordinary-chat hook did not run: %s", event)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("ordinary chat positive control did not execute the marker hook", err)
	}
}

func TestWorkbenchAssistantReviewTypedSkillDoesNotActivateDelegation(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "subagent-driven-development")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"superpowers","version":"1.0.0"}`)
	writeFixtureFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: subagent-driven-development\ndescription: Explicit delegated development\n---\nUse subagents.\n")
	installServerPlugin(t, h, root, plugins.InstallManaged)
	commitDelegationFixture(t, h)
	message := "/superpowers:subagent-driven-development Explain this architecture"
	h.fake.push(textReply("I can help explain the architecture."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: message, Workbench: &c}, &started)
	h.waitTurn(started.Turn.ID, nil)
	tasks, err := h.eng.Store.AgentTasksForThread(h.ctx, th.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("workbench text activated a delegation task/model scope: %+v %v", tasks, err)
	}
	h.fake.push(textReply("Ordinary chat retains explicit delegation activation."))
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: message}, &started)
	h.waitTurn(started.Turn.ID, nil)
	tasks, err = h.eng.Store.AgentTasksForThread(h.ctx, th.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ordinary explicit skill activation changed: %+v %v", tasks, err)
	}
	if _, err = h.eng.Store.AgentModelScope(h.ctx, tasks[0].ID); err != nil {
		t.Fatal("ordinary activation has no model scope", err)
	}
}
