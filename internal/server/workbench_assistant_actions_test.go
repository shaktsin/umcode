package server_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func TestWorkbenchAssistantSavesRequestedBuildEdit(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		found := false
		for _, tool := range req.Tools {
			found = found || tool.Name == "workbench__save_build"
		}
		if !found {
			t.Error("save_build tool not offered")
		}
		return toolReply("workbench__save_build", `{"operations":[{"type":"update","nodeId":"dense","config":{"outFeatures":8}}]}`)(req, key)
	}, textReply("I saved the Build graph with eight outputs."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Change dense to eight outputs and save it", Workbench: &c}, &started)
	turn, _, items := h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn=%+v", turn)
	}
	opened, err := workbench.Open(h.ws, c.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	var graph struct {
		Nodes []struct {
			ID     string         `json:"id"`
			Config map[string]any `json:"config"`
		} `json:"nodes"`
	}
	if err = json.Unmarshal(opened.Graph, &graph); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range graph.Nodes {
		if node.ID == "dense" {
			found = true
			if node.Config["outFeatures"] != float64(8) {
				t.Fatalf("dense=%v", node.Config)
			}
		}
	}
	if !found {
		t.Fatal("dense not saved")
	}
	completed := false
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == "workbench.save_build" && item.Status == protocol.ItemCompleted && strings.Contains(item.Tool.Output, `"saved":true`) {
			completed = true
		}
	}
	if !completed {
		t.Fatal("no completed save_build tool result")
	}
}

func TestWorkbenchAssistantCannotClaimAnUnstartedRun(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	fakeID := "train-" + strings.Repeat("a", 32)
	h.fake.push(textReply("Started for real: " + fakeID + " is running."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Start training", Workbench: &c}, &started)
	_, streamed, _ := h.waitTurn(started.Turn.ID, nil)
	if strings.Contains(streamed, fakeID) {
		t.Fatalf("unverified run claim was streamed: %q", streamed)
	}
	items, err := h.eng.Store.ListItems(h.ctx, th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == protocol.ItemAgentMessage && strings.Contains(item.Text, fakeID) {
			t.Fatalf("unverified run claim was shown: %q", item.Text)
		}
	}
}

func TestWorkbenchAssistantSavesDataPreparation(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	dataset, err := workbench.ImportData(h.ws, c.ModelID, workbench.DataSource{Kind: "local", Name: "Numbers"}, []workbench.DataFile{{Name: "numbers.csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte("x,label\n1,a\n2,b\n3,a\n4,b\n5,a\n6,b\n7,a\n8,b\n9,a\n10,b\n"))}})
	if err != nil {
		t.Fatal(err)
	}
	config := workbench.DataConfig{DatasetID: dataset.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x"}, TargetColumn: "label", Scale: "none", TrainPercent: 80, ValidationPercent: 10, TestPercent: 10, Seed: 17, Stratify: true}
	args, _ := json.Marshal(map[string]any{"config": config})
	h.fake.push(toolReply("workbench__save_data", string(args)), textReply("Saved Data preparation."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Prepare this dataset and save it", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, nil)
	state, err := workbench.ReadData(h.ws, c.ModelID, "")
	if err != nil {
		t.Fatal(err)
	}
	if state.Selection == nil || state.Selection.Config.DatasetID != dataset.ID {
		t.Fatal("assistant did not save Data preparation")
	}
	completed := false
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == "workbench.save_data" && item.Status == protocol.ItemCompleted {
			completed = true
		}
	}
	if !completed {
		t.Fatal("no completed Data action")
	}
}

func TestWorkbenchAssistantStartsAndStopsTraining(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python")
	script := "#!/bin/sh\ncase \"$3\" in *importlib.util*) echo '{\"version\":\"3.13\",\"missing\":[]}' ;; *) exec sleep 60 ;; esac\n"
	if err := os.WriteFile(python, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UMCODE_WORKBENCH_PYTHON", python)
	h, th, c := assistantRPCFixture(t)
	graph := `{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"input","kind":"input","position":{"x":0,"y":0},"config":{"mode":"features","features":1}},{"id":"dense","kind":"linear","position":{"x":0,"y":100},"config":{"outFeatures":2,"bias":true}},{"id":"output","kind":"output","position":{"x":0,"y":200},"config":{}}],"edges":[{"id":"a","source":"input","target":"dense"},{"id":"b","source":"dense","target":"output"}]}`
	if err := workbench.SaveGraph(h.ws, c.ModelID, json.RawMessage(graph)); err != nil {
		t.Fatal(err)
	}
	c.GraphJSON = graph
	c.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(graph)))
	c.SelectedNodeID = "dense"
	dataset, err := workbench.ImportData(h.ws, c.ModelID, workbench.DataSource{Kind: "local", Name: "Numbers"}, []workbench.DataFile{{Name: "numbers.csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte("x,label\n1,a\n2,b\n3,a\n4,b\n5,a\n6,b\n7,a\n8,b\n9,a\n10,b\n"))}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = workbench.PrepareData(h.ws, c.ModelID, workbench.DataConfig{DatasetID: dataset.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x"}, TargetColumn: "label", Scale: "none", TrainPercent: 80, ValidationPercent: 10, TestPercent: 10, Seed: 17, Stratify: true})
	if err != nil {
		t.Fatal(err)
	}
	h.fake.push(toolReply("workbench__save_build", `{"operations":[]}`), toolReply("workbench__train", `{"operation":"start","config":{"epochs":20,"batchSize":2,"learningRate":0.003,"seed":17}}`), textReply("Saved Build and started training."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Start training", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, nil)
	runID := ""
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == "workbench.train" && item.Status == protocol.ItemCompleted {
			var result struct {
				RunID string `json:"runId"`
			}
			_ = json.Unmarshal([]byte(item.Tool.Output), &result)
			runID = result.RunID
		}
	}
	if runID == "" {
		t.Fatal("training action did not start")
	}
	h.fake.push(toolReply("workbench__train", fmt.Sprintf(`{"operation":"stop","runId":%q}`, runID)), textReply("Training stopped."))
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Stop training", Workbench: &c}, &started)
	_, _, items = h.waitTurn(started.Turn.ID, nil)
	stopped := false
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == "workbench.train" && item.Status == protocol.ItemCompleted && strings.Contains(item.Tool.Output, `"operation":"stop"`) {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("training action did not stop")
	}
}

func TestWorkbenchAssistantTestsCheckpoint(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python")
	script := "#!/bin/sh\ncase \"$3\" in *importlib.util*) echo '{\"version\":\"3.13\",\"missing\":[]}' ;; *) while IFS= read -r line; do echo '{\"ok\":true,\"value\":1.0,\"device\":\"cpu\",\"input\":{\"mode\":\"features\",\"features\":[\"x\"]}}'; done ;; esac\n"
	if err := os.WriteFile(python, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UMCODE_WORKBENCH_PYTHON", python)
	h, th, c := assistantRPCFixture(t)
	dataset, err := workbench.ImportData(h.ws, c.ModelID, workbench.DataSource{Kind: "local", Name: "Numbers"}, []workbench.DataFile{{Name: "numbers.csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte("x,label\n1,a\n2,b\n3,a\n4,b\n5,a\n6,b\n7,a\n8,b\n9,a\n10,b\n"))}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = workbench.PrepareData(h.ws, c.ModelID, workbench.DataConfig{DatasetID: dataset.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x"}, TargetColumn: "label", Scale: "none", TrainPercent: 80, ValidationPercent: 10, TestPercent: 10, Seed: 17, Stratify: true})
	if err != nil {
		t.Fatal(err)
	}
	version := mustModelVersion(t, h, c.ModelID)
	snapshot, err := workbench.SnapshotTraining(h.ws, c.ModelID, version)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := workbench.SaveTrainingCheckpoint(h.ws, c.ModelID, version, "Test", snapshot, []byte("weights"), workbench.CheckpointValidation{Input: workbench.CheckpointInput{Mode: "features", Features: []string{"x"}, Classes: []string{"a", "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.fake.push(toolReply("workbench__test_checkpoint", fmt.Sprintf(`{"checkpointId":%q,"features":{"x":1},"maxNewTokens":1,"temperature":0}`, checkpoint.ID)), textReply("The checkpoint predicted a value."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Test this checkpoint with x=1", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, nil)
	passed := false
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == "workbench.test_checkpoint" && item.Status == protocol.ItemCompleted && strings.Contains(item.Tool.Output, `"value":1`) {
			passed = true
		}
	}
	if !passed {
		t.Fatal("checkpoint test did not return the local prediction")
	}
}

func mustModelVersion(t *testing.T, h *harness, id string) string {
	t.Helper()
	opened, err := workbench.Open(h.ws, id)
	if err != nil {
		t.Fatal(err)
	}
	return opened.Model.CreatedAt.Format(time.RFC3339Nano)
}

func TestWorkbenchAssistantRejectsConcurrentBuildSave(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	h.fake.push(func(req llm.Request, key string) ([]llm.Event, error) {
		if err := workbench.SaveGraph(h.ws, c.ModelID, json.RawMessage(assistantRPCGraph)); err != nil {
			t.Fatal(err)
		}
		return toolReply("workbench__save_build", `{"operations":[{"type":"update","nodeId":"dense","config":{"outFeatures":8}}]}`)(req, key)
	}, textReply("The Build graph changed while I was working, so I did not save."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Change dense to eight outputs and save it", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, nil)
	failed := false
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == "workbench.save_build" && item.Status == protocol.ItemFailed && strings.Contains(item.Tool.Error, "changed") {
			failed = true
		}
	}
	if !failed {
		t.Fatal("concurrent save was not rejected")
	}
	opened, err := workbench.Open(h.ws, c.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(opened.Graph), `"outFeatures": 3`) {
		t.Fatalf("concurrent graph overwritten: %s", opened.Graph)
	}
}
