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

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/workbench"
)

const assistantRPCGraph = `{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"input","kind":"input","position":{"x":0,"y":0},"config":{"mode":"features","features":16}},{"id":"dense","kind":"linear","position":{"x":0,"y":130},"config":{"outFeatures":3,"bias":true,"tieEmbedding":false}},{"id":"output","kind":"output","position":{"x":0,"y":260},"config":{}}],"edges":[{"id":"a","source":"input","target":"dense"},{"id":"b","source":"dense","target":"output"}]}`

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

func TestWorkbenchAssistantRPCContextAndProposal(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	disk := filepath.Join(h.ws, "models", c.ModelID, "graph.json")
	before, _ := os.ReadFile(disk)
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		if len(req.Tools) != 1 || req.Tools[0].Name != "workbench__propose_edit" {
			t.Errorf("unexpected tools: %+v", req.Tools)
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
		if it.Tool != nil && it.Tool.Name == "shell__run" && it.Status == protocol.ItemDenied && strings.Contains(it.Tool.Error, "Only architecture proposals") {
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
			if tool.Name == "workbench__propose_edit" {
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
