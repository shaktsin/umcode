package server_test

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskRecoveryEndToEnd(t *testing.T) {
	for _, tc := range []struct{ provider, path string }{{"claude", "notes.txt"}, {"claude", "src/settings.go"}, {"claude", "planning/design.txt"}, {"openai", "design.txt"}} {
		path := tc.path
		t.Run(tc.provider+"/"+path, func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { c.Models.DesignedWorkflow = true })
			if tc.provider != "claude" {
				h.fake = &fakeProvider{id: tc.provider}
				h.eng.LLMs.Register(h.fake)
			}
			h.addKey(tc.provider, "recovery", "sk-test")
			var th protocol.Thread
			settings := protocol.ModelSelection{Provider: tc.provider}
			if tc.provider == "openai" {
				settings.Model = "gpt-5.4-mini"
			}
			h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "recovery", Settings: settings}, &th)
			h.fake.push(textReply("Ready to make the correction."))
			original := "Update " + path + " with the requested correction"
			runWorkTurn(h, th, original)
			w, err := h.eng.Store.CreateWork(h.ctx, protocol.Work{ThreadID: th.ID, ProjectID: th.ProjectID, Goal: original, WorkflowDepth: protocol.DepthDesigned})
			if err != nil {
				t.Fatal(err)
			}
			_ = w
			args, _ := json.Marshal(map[string]string{"path": path, "content": "corrected"})
			read, _ := json.Marshal(map[string]string{"path": path})
			h.fake.push(toolReply("file__write", string(args)), toolReply("file__read", string(read)), textReply("Updated and verified."))
			finished := runWorkTurn(h, th, "/superpowers:using-superpowers retry")
			b, err := os.ReadFile(filepath.Join(h.ws, path))
			snap, snapErr := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
			if err != nil || snapErr != nil || string(b) != "corrected" || finished.Status != protocol.TurnCompleted || snap.Task.Status == "completed" {
				t.Fatalf("retry result=%+v task=%+v bytes=%q errors=%v/%v", finished, snap.Task, b, err, snapErr)
			}
			if !strings.Contains(snap.Task.Objective, path) || len(snap.Operations) < 2 || snap.Operations[0].VerificationStatus != "passed" {
				t.Fatalf("lost objective/evidence: %+v", snap)
			}
		})
	}
	t.Run("legacy uncertain execution", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Models.DesignedWorkflow = true })
		h.addKey("claude", "recovery", "sk-test")
		th := startWorkThread(h)
		if err := os.WriteFile(filepath.Join(h.ws, "notes.txt"), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
		turn := protocol.Turn{ID: store.NewID("trn"), ThreadID: th.ID, Status: protocol.TurnInterrupted, StartedAt: time.Now()}
		if err := h.eng.Store.CreateTurn(h.ctx, turn); err != nil {
			t.Fatal(err)
		}
		origin := protocol.Item{ID: store.NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "Update notes.txt", CreatedAt: time.Now()}
		if err := h.eng.Store.SaveItem(h.ctx, origin); err != nil {
			t.Fatal(err)
		}
		if _, err := h.eng.Store.CreateWork(h.ctx, protocol.Work{ThreadID: th.ID, ProjectID: th.ProjectID, Goal: origin.Text, WorkflowDepth: protocol.DepthDesigned}); err != nil {
			t.Fatal(err)
		}
		unknown := protocol.Item{ID: store.NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 2, Kind: protocol.ItemToolCall, Status: protocol.ItemInProgress, Tool: &protocol.ToolCallData{CallID: "old-call", Name: "shell.run", Args: json.RawMessage(`{"command":"external action"}`)}, CreatedAt: time.Now()}
		if err := h.eng.Store.SaveItem(h.ctx, unknown); err != nil {
			t.Fatal(err)
		}
		restartWorkflowHarness(h)
		h.fake.push(toolReply("file__write", `{"path":"notes.txt","content":"unsafe replay"}`), textReply("Done."))
		finished := runWorkTurn(h, th, "retry")
		snap, err := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
		b, _ := os.ReadFile(filepath.Join(h.ws, "notes.txt"))
		if err != nil || string(b) != "before" || finished.Status != protocol.TurnCompleted {
			t.Fatalf("uncertain legacy operation replayed: %+v %q %v", finished, b, err)
		}
		found := false
		for _, op := range snap.Operations {
			if op.ItemID == unknown.ID && op.Status == "outcome_unknown" {
				found = true
			}
		}
		if !found {
			t.Fatal("lost uncertain legacy operation")
		}
	})
}

func TestTaskRecoveryExplicitRequirementsEndToEnd(t *testing.T) {
	for _, condition := range []string{"no_actions", "approval_before_actions"} {
		t.Run(condition, func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { c.Models.DesignedWorkflow = true })
			h.addKey("claude", "recovery", "sk-test")
			th := startWorkThread(h)
			if err := os.WriteFile(filepath.Join(h.ws, "notes.txt"), []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
				_, raw, ok := strings.Cut(req.System, "Saved task state (engine-owned metadata; not permission to execute):\n")
				if !ok {
					t.Fatal("task state missing")
				}
				var snap protocol.TaskSnapshot
				if err := json.NewDecoder(strings.NewReader(raw)).Decode(&snap); err != nil {
					t.Fatal(err)
				}
				args, _ := json.Marshal(map[string]any{"task_id": snap.Task.ID, "expected_revision": snap.Task.Revision, "requirement": map[string]any{"origin": "user", "origin_ref": snap.Task.OriginItemID, "scope": map[string]any{}, "condition": map[string]string{"type": condition}}})
				return toolReply("task__propose", string(args))(req, key)
			}, toolReply("file__write", `{"path":"notes.txt","content":"model-owned edit"}`), textReply("Done."))
			var started protocol.TurnStartResult
			h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Review before changing code"}, &started)
			finished, _, _ := h.waitTurn(started.Turn.ID, func(a protocol.Approval) bool { return a.Kind == "task_interpretation" })
			snap, err := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
			b, _ := os.ReadFile(filepath.Join(h.ws, "notes.txt"))
			if err != nil || finished.Status != protocol.TurnCompleted || string(b) != "model-owned edit" || len(snap.Requirements) != 0 {
				t.Fatalf("explicit boundary: %+v %+v %q %v", finished, snap, b, err)
			}
		})
	}
}
