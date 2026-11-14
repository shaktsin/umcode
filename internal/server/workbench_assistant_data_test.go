package server_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func assistantDataToolItem(items []protocol.Item, tool string) *protocol.Item {
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == tool {
			return &item
		}
	}
	return nil
}

func TestWorkbenchAssistantImportsLocalFile(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	if err := os.WriteFile(filepath.Join(h.ws, "reviews.csv"), []byte("text,label\ngood,pos\nbad,neg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"sources": []map[string]any{{"type": "local", "path": "reviews.csv"}}})
	h.fake.push(toolReply("workbench__import_data", string(args)), textReply("Imported."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Import reviews.csv", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, nil)
	item := assistantDataToolItem(items, "workbench.import_data")
	if item == nil || item.Status != protocol.ItemCompleted {
		t.Fatalf("import item = %+v", item)
	}
	if !strings.Contains(item.Tool.Output, `"ok":true`) {
		t.Fatalf("output = %s", item.Tool.Output)
	}
	state, err := workbench.ReadData(h.ws, c.ModelID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Datasets) != 1 || state.Datasets[0].RowCount != 2 {
		t.Fatalf("datasets = %+v", state.Datasets)
	}
}

func TestWorkbenchAssistantImportRejectsEscapeAndPartialFailure(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	if err := os.WriteFile(filepath.Join(h.ws, "good.csv"), []byte("x\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"sources": []map[string]any{
		{"type": "local", "path": "good.csv"},
		{"type": "local", "path": "../escape.csv"},
		{"type": "local", "path": "missing.csv"},
	}})
	h.fake.push(toolReply("workbench__import_data", string(args)), textReply("Imported what I could."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "Import files", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, nil)
	item := assistantDataToolItem(items, "workbench.import_data")
	if item == nil || item.Status != protocol.ItemCompleted {
		t.Fatalf("import item = %+v", item)
	}
	var result struct {
		Sources []struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(item.Tool.Output), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sources) != 3 || !result.Sources[0].OK || result.Sources[1].OK || result.Sources[2].OK {
		t.Fatalf("sources = %+v", result.Sources)
	}
	if !strings.Contains(result.Sources[1].Error, "inside the project") {
		t.Fatalf("escape error = %q", result.Sources[1].Error)
	}
}

func TestWorkbenchAssistantInspectsData(t *testing.T) {
	h, th, c := assistantRPCFixture(t)
	for _, name := range []string{"a", "b"} {
		_, err := workbench.ImportData(h.ws, c.ModelID, workbench.DataSource{Kind: "local", Name: name}, []workbench.DataFile{{Name: name + ".csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte("text,label\nhello,pos\n"))}})
		if err != nil {
			t.Fatal(err)
		}
	}
	h.fake.push(toolReply("workbench__inspect_data", `{"datasetIds":[],"sample":5}`), textReply("Here is the data."))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "How does the data look?", Workbench: &c}, &started)
	_, _, items := h.waitTurn(started.Turn.ID, nil)
	item := assistantDataToolItem(items, "workbench.inspect_data")
	if item == nil || item.Status != protocol.ItemCompleted {
		t.Fatalf("inspect item = %+v", item)
	}
	if !strings.Contains(item.Tool.Output, `"sharedColumns":["label","text"]`) {
		t.Fatalf("output = %s", item.Tool.Output)
	}
}
