package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/workbench"
)

const assistantRPCGraph = `{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"input","kind":"input","position":{"x":0,"y":0},"config":{"mode":"features","features":16}},{"id":"dense","kind":"linear","position":{"x":0,"y":130},"config":{"outFeatures":3,"bias":true,"tieEmbedding":false}},{"id":"output","kind":"output","position":{"x":0,"y":260},"config":{}}],"edges":[{"id":"a","source":"input","target":"dense"},{"id":"b","source":"dense","target":"output"}]}`

func TestImportedAssistantRPCAdmitsDraftAndRejectsForgedSource(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	config := `{"model_type":"gpt2","n_embd":16,"n_layer":2,"n_head":4,"vocab_size":32,"n_positions":16}`
	inspection, err := workbench.InspectImport([]workbench.ImportFile{{Name: "config.json", Size: int64(len(config)), Kind: "config", Content: config}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(inspection.Graph)
	created, err := workbench.CreateImported(h.ws, "Imported assistant", raw)
	if err != nil {
		t.Fatal(err)
	}
	c.ModelID = created.Model.ID
	inspection.Graph.Nodes = inspection.Graph.Nodes[1:]
	inspection.Graph.Edges = []workbench.ImportedEdge{}
	raw, _ = json.Marshal(inspection.Graph)
	c.GraphJSON = string(raw)
	c.Revision = fmt.Sprintf("%x", sha256.Sum256(raw))
	c.SelectedNodeID = inspection.Graph.Nodes[0].ID
	for name, mutate := range map[string]func(*protocol.WorkbenchTurnContext){
		"revision":  func(c *protocol.WorkbenchTurnContext) { c.Revision = strings.Repeat("0", 64) },
		"selection": func(c *protocol.WorkbenchTurnContext) { c.SelectedNodeID = "missing" },
		"format swap": func(c *protocol.WorkbenchTurnContext) {
			c.GraphJSON = assistantRPCGraph
			c.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(c.GraphJSON)))
			c.SelectedNodeID = "dense"
		},
		"forged source": func(c *protocol.WorkbenchTurnContext) {
			var graph map[string]any
			if err := json.Unmarshal([]byte(c.GraphJSON), &graph); err != nil {
				t.Fatal(err)
			}
			graph["source"].(map[string]any)["warnings"] = []string{"forged"}
			raw, _ := json.Marshal(graph)
			c.GraphJSON = string(raw)
			c.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(c.GraphJSON)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			mutate(&bad)
			if h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain", Workbench: &bad}) == nil {
				t.Fatal("invalid context admitted")
			}
		})
	}
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		return toolReply("workbench__propose_edit", `{"explanation":"Restore original layer","operations":[{"type":"restore","nodeId":"import-1"}]}`)(req, key)
	}, textReply("Preview and apply, then save."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Restore the missing layer", Workbench: &c}, &started)
	turn, _, _ := h.waitTurn(started.Turn.ID, func(protocol.Approval) bool { t.Error("unexpected approval"); return false })
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn: %+v", turn)
	}
	items, err := h.eng.Store.ListItems(h.ctx, th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if strings.Contains(string(item.Data), `"type":"restore"`) && item.Status == protocol.ItemCompleted {
			found = true
		}
	}
	if !found {
		t.Fatal("restore proposal not recorded")
	}
	reopened, err := workbench.Open(h.ws, created.Model.ID)
	if err != nil || string(reopened.Graph) != string(created.Graph) {
		t.Fatalf("proposal changed saved graph: %v", err)
	}
}

func assistantRPCFixture(t *testing.T) (*harness, protocol.Thread, protocol.WorkbenchTurnContext) {
	t.Helper()
	h := newHarness(t, nil)
	h.addKey("claude", "test", "fake")
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Assistant test", Task: "classification"}, &model)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "Architecture guidance", Settings: protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-5"}}, &th)
	return h, th, protocol.WorkbenchTurnContext{ProjectID: h.proj.ID, ModelID: model.ID, ModelName: "untrusted name", GraphJSON: assistantRPCGraph, Revision: fmt.Sprintf("%x", sha256.Sum256([]byte(assistantRPCGraph))), SelectedNodeID: "dense", Summary: `{"parameters":51}`}
}

func TestWorkbenchAssistantSeesFailedTrainingRun(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	opened, err := workbench.Open(h.ws, c.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	version := opened.Model.CreatedAt.Format(time.RFC3339Nano)
	run := workbench.TrainingRun{ID: "train-" + strings.Repeat("a", 32), ModelCreatedAt: version, Name: "Failed local run", Status: "failed", Error: "expected target class index below 18, got 24", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = workbench.WriteTrainingRun(h.ws, c.ModelID, version, run); err != nil {
		t.Fatal(err)
	}
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		var joined string
		for _, message := range req.Messages {
			joined += message.JoinedText()
		}
		if !strings.Contains(joined, "expected target class index below 18, got 24") || !strings.Contains(joined, `"status":"failed"`) {
			t.Errorf("failed run missing from assistant context: %s", joined)
		}
		for _, stage := range []string{`"data":`, `"build":`, `"train":`, `"inference":`} {
			if !strings.Contains(joined, stage) {
				t.Errorf("missing %s stage", stage)
			}
		}
		if !strings.Contains(req.System, "training run error") {
			t.Error("assistant was not instructed to use run errors")
		}
		return textReply("The saved training run failed because the target class index exceeded the output width.")(req, key)
	})
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Why did training fail?", Workbench: &c}, &started)
	turn, _, _ := h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn: %+v", turn)
	}
}

func TestWorkbenchAssistantRPCContextAndProposal(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	disk := filepath.Join(h.ws, "models", c.ModelID, "graph.json")
	before, _ := os.ReadFile(disk)
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		names := map[string]bool{}
		for _, tool := range req.Tools {
			names[tool.Name] = true
		}
		for _, name := range []string{"workbench__propose_edit", "workbench__save_build", "workbench__save_data", "workbench__train", "workbench__test_checkpoint"} {
			if !names[name] {
				t.Errorf("missing Workbench tool %s", name)
			}
		}
		if !strings.Contains(req.System, "workbench") {
			t.Error("missing assistant instructions")
		}
		joined := ""
		for _, m := range req.Messages {
			joined += m.JoinedText()
		}
		if !strings.Contains(joined, "outFeatures") || !strings.Contains(joined, "dense") || !strings.Contains(joined, c.Revision) {
			t.Error("unsaved architecture/selection missing from request")
		}
		return toolReply("workbench__propose_edit", `{"explanation":"Widen the projection","operations":[{"type":"update","nodeId":"dense","config":{"outFeatures":8}}]}`)(req, key)
	}, textReply("Preview this suggestion before applying it."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Why is this projection small?", Workbench: &c}, &started)
	turn, _, _ := h.waitTurn(started.Turn.ID, func(a protocol.Approval) bool { t.Errorf("proposal asked for tool approval: %s", a.Tool); return false })
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn: %+v", turn)
	}
	h.fake.mu.Lock()
	privateClassifications := len(h.fake.intentCalls)
	h.fake.mu.Unlock()
	if privateClassifications != 0 {
		t.Fatal("architecture guidance entered the software-task classifier instead of using its submitted model context")
	}
	if _, err := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("architecture guidance created or changed a software execution task")
	}
	items, err := h.eng.Store.ListItems(h.ctx, th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	contextSeen, proposalSeen := false, false
	for _, it := range items {
		var data map[string]json.RawMessage
		_ = json.Unmarshal(it.Data, &data)
		if raw := data["workbenchContext"]; len(raw) > 0 {
			var got protocol.WorkbenchTurnContext
			_ = json.Unmarshal(raw, &got)
			contextSeen = got.GraphJSON == c.GraphJSON && got.ModelName == "Assistant test" && got.SelectedNodeID == "dense"
		}
		if raw := data["workbenchProposal"]; len(raw) > 0 {
			var got protocol.WorkbenchProposal
			_ = json.Unmarshal(raw, &got)
			proposalSeen = it.Status == protocol.ItemCompleted && got.ProjectID == h.proj.ID && got.ModelID == c.ModelID && got.Revision == c.Revision
		}
	}
	if !contextSeen || !proposalSeen {
		t.Fatalf("missing durable context/proposal: context=%v proposal=%v", contextSeen, proposalSeen)
	}
	after, _ := os.ReadFile(disk)
	if string(before) != string(after) {
		t.Fatal("proposal wrote the bundle")
	}
	var reopened protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: th.ID}, &reopened)
	if len(reopened.Items) != len(items) {
		t.Fatal("history unavailable over RPC")
	}
}

func TestWorkbenchAssistantRPCRejectsForeignContextAndOtherTools(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	bad := c
	bad.ProjectID = "other"
	if h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain", Workbench: &bad}) == nil {
		t.Fatal("foreign project accepted")
	}
	bad = c
	bad.ModelID = "missing"
	if h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain", Workbench: &bad}) == nil {
		t.Fatal("missing model accepted")
	}
	h.fake.push(toolReply("shell__run", `{"command":"touch assistant-escaped"}`), textReply("I can only suggest architecture changes."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain my model", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, func(a protocol.Approval) bool {
		t.Errorf("unavailable tool reached approval: %s", a.Tool)
		return false
	})
	failed := false
	for _, it := range items {
		if it.Tool != nil && it.Tool.Name == "shell__run" && it.Status == protocol.ItemDenied && strings.Contains(it.Tool.Error, "Only scoped Workbench actions") {
			failed = true
		}
	}
	if !failed {
		t.Fatal("unavailable tool did not fail before execution")
	}
	if _, err := os.Stat(filepath.Join(h.ws, "assistant-escaped")); !os.IsNotExist(err) {
		t.Fatal("tool escaped proposal mode")
	}
	h.fake.push(func(req llm.Request, _ string) ([]llm.Event, error) {
		for _, tool := range req.Tools {
			if strings.HasPrefix(tool.Name, "workbench__") {
				t.Error("workbench tool leaked into ordinary chat")
			}
		}
		return []llm.Event{{Type: llm.EventTextDelta, Text: "Ordinary answer"}, {Type: llm.EventDone}}, nil
	})
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Hello"}, &started)
	h.waitTurn(started.Turn.ID, nil)
}

func TestWorkbenchAssistantPreservesExistingCodingTask(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	h.fake.push(textReply("The project is ready to discuss."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Explain this project"}, &started)
	h.waitTurn(started.Turn.ID, nil)
	before, err := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(before)
	for _, scripts := range [][]func(llm.Request, string) ([]llm.Event, error){
		{toolReply("workbench__propose_edit", `{"explanation":"Widen the projection","operations":[{"type":"update","nodeId":"dense","config":{"outFeatures":8}}]}`), textReply("You can preview this change.")},
		{toolReply("file__write", `{"path":"not-written.txt","content":"no"}`), textReply("Only architecture suggestions are available.")},
		{func(llm.Request, string) ([]llm.Event, error) { return nil, context.Canceled }},
	} {
		h.fake.push(scripts...)
		h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Help with this layer", Workbench: &c}, &started)
		h.waitTurn(started.Turn.ID, nil)
		after, err := h.eng.Store.ActiveExecutionTask(h.ctx, th.ID)
		actual, _ := json.Marshal(after)
		if err != nil || string(actual) != string(expected) {
			t.Fatalf("workbench changed the existing coding task: %v\nbefore=%s\nafter=%s", err, expected, actual)
		}
	}
}
