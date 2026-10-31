package server_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func TestWorkbenchRoundTripAndProjectScope(t *testing.T) {
	h := newHarness(t, nil)
	var created workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Support", Task: "text-generation"}, &created)
	graph := json.RawMessage(`{"version":1,"nodes":[{"id":"tokens"}],"edges":[]}`)
	h.call(protocol.MethodWorkbenchGraphSave, protocol.WorkbenchGraphSaveParams{ProjectID: h.proj.ID, ModelID: created.ID, Graph: graph}, nil)
	data, err := os.ReadFile(filepath.Join(h.ws, "models", created.ID, "graph.json"))
	if err != nil || !strings.Contains(string(data), "tokens") {
		t.Fatalf("saved graph = %s, %v", data, err)
	}
	// A new RPC connection must reload persisted bundles, with no session cache.
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "workbench-reopen", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var opened workbench.OpenResult
	if err := c.Call(h.ctx, protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: h.proj.ID, ModelID: created.ID}, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Model.Task != "text-generation" || !strings.Contains(string(opened.Graph), "tokens") {
		t.Fatalf("reopened = %+v", opened)
	}
	var list struct {
		Models []workbench.Model `json:"models"`
	}
	h.call(protocol.MethodWorkbenchModelList, protocol.WorkbenchModelListParams{ProjectID: h.proj.ID}, &list)
	if len(list.Models) != 1 || list.Models[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}
	var other protocol.Project
	h.call(protocol.MethodProjectCreate, protocol.ProjectCreateParams{Root: t.TempDir(), Name: "Other"}, &other)
	h.call(protocol.MethodWorkbenchModelList, protocol.WorkbenchModelListParams{ProjectID: other.ID}, &list)
	if len(list.Models) != 0 {
		t.Fatal("models leaked between projects")
	}
	h.callErr(protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: other.ID, ModelID: created.ID})
	h.callErr(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Support", Task: "custom"})
	h.callErr(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{Name: "No project", Task: "custom"})
	h.callErr(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Bad", Task: "unknown"})
	h.callErr(protocol.MethodWorkbenchGraphSave, protocol.WorkbenchGraphSaveParams{ProjectID: h.proj.ID, ModelID: created.ID, Graph: json.RawMessage(`{"version":1,"nodes":null,"edges":[]}`)})
	h.callErr(protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: h.proj.ID, ModelID: "../escape"})
	h.call(protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: h.proj.ID, ModelID: created.ID}, &opened)
	if !strings.Contains(string(opened.Graph), "tokens") {
		t.Fatal("invalid save damaged graph")
	}
}

func TestWorkbenchMissingModelReturnsNotFound(t *testing.T) {
	h := newHarness(t, nil)
	err := h.callErr(protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: h.proj.ID, ModelID: "missing"})
	var rpc *protocol.Error
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeNotFound {
		t.Fatalf("missing model error = %v", err)
	}
}

func TestWorkbenchExactJSONTransport(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Numbers", Task: "custom"}, &model)
	const graph = `{"version":1,"nodes":[],"edges":[],"seed":9007199254740993,"weight":1.0000000000000000001}`
	h.call(protocol.MethodWorkbenchGraphSave, map[string]any{"projectId": h.proj.ID, "modelId": model.ID, "graphJson": graph}, nil)
	var opened struct {
		GraphJSON string `json:"graphJson"`
	}
	h.call(protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: h.proj.ID, ModelID: model.ID}, &opened)
	if !strings.Contains(opened.GraphJSON, "9007199254740993") || !strings.Contains(opened.GraphJSON, "1.0000000000000000001") {
		t.Fatalf("rounded JSON transport: %s", opened.GraphJSON)
	}
	h.callErr(protocol.MethodWorkbenchGraphSave, map[string]any{"projectId": h.proj.ID, "modelId": model.ID, "graphJson": graph, "graph": json.RawMessage(graph)})
}
