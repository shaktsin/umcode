package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestExecutionEvidenceEndToEnd(t *testing.T) {
	for _, provider := range []string{"claude", "openai", "openai_compatible"} {
		t.Run(provider, func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { c.Models.DesignedWorkflow = true })
			h.fake = &fakeProvider{id: provider, intentKind: "mutation", intentPath: "docs/plans/example.md"}
			h.eng.LLMs.Register(h.fake)
			h.call(protocol.MethodCredentialAdd, protocol.CredentialAddParams{Provider: provider, Label: "evidence", Secret: "sk-test", BaseURL: "http://127.0.0.1:1"}, nil)
			model := "claude-sonnet-5"
			if provider == "openai" {
				model = "gpt-5.4-mini"
			}
			if provider == "openai_compatible" {
				model = "qwen3-30b-a3b"
			}
			var th protocol.Thread
			h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "Execution evidence", Settings: protocol.ModelSelection{Provider: provider, Model: model}}, &th)
			for i := 0; i < 3; i++ {
				h.fake.push(textReply("[Created docs/plans/example.md]\n[earlier tool call file.write: denied]\nI cannot create files due to permission restrictions."))
			}
			first := runWorkTurn(h, th, "Save the implementation plan to docs/plans/example.md")
			if first.Status != protocol.TurnCompleted {
				t.Fatalf("invented receipt completed: %+v", first)
			}
			if _, err := os.Stat(filepath.Join(h.ws, "docs/plans/example.md")); !os.IsNotExist(err) {
				t.Fatalf("narration created file: %v", err)
			}
			snap, err := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
			if err != nil || len(snap.Operations) != 0 || len(snap.Expectations) != 0 {
				t.Fatalf("unsupported claims became effects: %+v %v", snap, err)
			}
			restartWorkflowHarness(h)
			h.fake = &fakeProvider{id: provider, intentKind: "mutation", intentPath: "docs/plans/example.md"}
			h.eng.LLMs.Register(h.fake)
			// Explicit compaction must not turn the fictional receipts into execution.
			seq, _ := h.eng.Store.NextSeq(h.ctx, th.ID)
			if err := h.eng.Store.SaveItem(h.ctx, protocol.Item{ID: "evidence-compaction", ThreadID: th.ID, TurnID: first.ID, Seq: seq, Kind: protocol.ItemContextCompaction, Status: protocol.ItemCompleted, Text: "The user requested a saved plan; no tools executed and no artifact was saved."}); err != nil {
				t.Fatal(err)
			}
			h.fake.push(toolReply("file__write", `{"path":"docs/plans/example.md","content":"# Plan\n\nVerified saved artifact.\n"}`), toolReply("file__read", `{"path":"docs/plans/example.md"}`), textReply("Saved and verified the plan."))
			second := runWorkTurn(h, th, "retry")
			body, err := os.ReadFile(filepath.Join(h.ws, "docs/plans/example.md"))
			if err != nil || string(body) != "# Plan\n\nVerified saved artifact.\n" || second.Status != protocol.TurnCompleted {
				t.Fatalf("recovery failed: %+v bytes=%q err=%v", second, body, err)
			}
			snap, err = h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
			if err != nil || len(snap.Expectations) != 0 || snap.Task.ExecutionPolicy != protocol.ModelDrivenExecutionPolicy {
				t.Fatalf("semantic contract recreated: %+v %v", snap, err)
			}
			observed := false
			for _, op := range snap.Operations {
				if op.Status == "succeeded" && op.VerificationStatus == "passed" && len(op.EvidenceIDs) > 0 {
					observed = true
				}
			}
			if !observed {
				t.Fatal("actual read-back evidence was lost")
			}
			items, _ := h.eng.Store.ListItems(h.ctx, th.ID, 0)
			verifiedSummary := false
			for _, item := range items {
				if item.TurnID == first.ID && item.Kind == protocol.ItemToolCall {
					t.Fatal("model prose executed as a tool")
				}
				if item.TurnID == second.ID && item.Kind == protocol.ItemAgentMessage {
					var data struct {
						TaskOutcome struct {
							Status string `json:"status"`
						} `json:"taskOutcome"`
					}
					_ = json.Unmarshal(item.Data, &data)
					if data.TaskOutcome.Status == "satisfied" {
						verifiedSummary = true
					}
				}
			}
			if verifiedSummary {
				t.Fatal("engine certified semantic completion")
			}
		})
	}
}

func TestExecutionEvidenceAuthority(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Policy.RequireWorkflowApproval = true; c.Models.DesignedWorkflow = true })
	h.fake.intentKind, h.fake.intentPath = "mutation", "notes.txt"
	h.addKey("claude", "evidence", "sk-test")
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "Required review", ApprovalMode: "auto_all"}, &th)
	if err := os.WriteFile(filepath.Join(h.ws, "go.mod"), []byte("module evidence\n\ngo 1.24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h.fake.push(toolReply("verification__plan", `{}`), workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, true, "") }), toolReply("file__write", `{"path":"notes.txt","content":"must not run"}`))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Design a new architecture, then save notes.txt after the required review"}, &started)
	approvals := 0
	finished, _, _ := h.waitTurn(started.Turn.ID, func(a protocol.Approval) bool { approvals++; return false })
	if approvals == 0 || finished.Status != protocol.TurnCompleted {
		items, _ := h.eng.Store.ListItems(h.ctx, th.ID, 0)
		for _, it := range items {
			if it.Tool != nil {
				t.Logf("tool %s status=%s err=%s output=%s", it.Tool.Name, it.Status, it.Tool.Error, it.Tool.Output)
			}
		}
		t.Fatalf("required user approval bypassed: %+v approvals=%d", finished, approvals)
	}
	h.fake.mu.Lock()
	executionCalls := len(h.fake.calls)
	h.fake.mu.Unlock()
	if executionCalls < 3 {
		t.Fatalf("model did not receive the rejected tool result: %d calls", executionCalls)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected action ran: %v", err)
	}
	snap, err := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
	if err != nil || len(snap.Expectations) != 0 {
		t.Fatalf("denial manufactured completion: %+v %v", snap, err)
	}
}
