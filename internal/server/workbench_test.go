package server_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestWorkbenchDeleteIsRecoverableAndProjectScoped(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Delete this", Task: "custom"}, &model)
	otherRoot := t.TempDir()
	var other protocol.Project
	h.call(protocol.MethodProjectCreate, protocol.ProjectCreateParams{Root: otherRoot, Name: "Other"}, &other)
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: other.ID, Name: "Delete this", Task: "custom"}, nil)
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "untrusted-delete", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(h.ctx, protocol.MethodWorkbenchModelDelete, protocol.WorkbenchModelDeleteParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: model.CreatedAt.Format(time.RFC3339Nano)}, nil)
	var rpc *protocol.Error
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeInvalidRequest {
		t.Fatalf("non-app delete = %v", err)
	}
	if _, err := workbench.Open(h.ws, model.ID); err != nil {
		t.Fatalf("denied deletion changed model: %v", err)
	}
	h.callErr(protocol.MethodWorkbenchModelDelete, protocol.WorkbenchModelDeleteParams{ProjectID: h.proj.ID, ModelID: "../escape"})
	var deleted struct {
		OK        bool   `json:"ok"`
		TrashPath string `json:"trashPath"`
	}
	h.call(protocol.MethodWorkbenchModelDelete, protocol.WorkbenchModelDeleteParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: model.CreatedAt.Format(time.RFC3339Nano)}, &deleted)
	if !deleted.OK || !strings.HasPrefix(deleted.TrashPath, "models/.trash/") {
		t.Fatalf("delete = %+v", deleted)
	}
	if _, err := os.Stat(filepath.Join(h.ws, filepath.FromSlash(deleted.TrashPath), "graph.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := workbench.Open(otherRoot, model.ID); err != nil {
		t.Fatalf("other project changed: %v", err)
	}
	err = h.callErr(protocol.MethodWorkbenchModelDelete, protocol.WorkbenchModelDeleteParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: model.CreatedAt.Format(time.RFC3339Nano)})
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeNotFound {
		t.Fatalf("repeat delete = %v", err)
	}
	var list struct {
		Models []workbench.Model `json:"models"`
	}
	h.call(protocol.MethodWorkbenchModelList, protocol.WorkbenchModelListParams{ProjectID: h.proj.ID}, &list)
	if len(list.Models) != 0 {
		t.Fatalf("deleted model remains: %+v", list)
	}
}

func TestWorkbenchStaleDeleteCannotRemoveReplacement(t *testing.T) {
	h := newHarness(t, nil)
	var old, replacement workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Reused", Task: "custom"}, &old)
	p := map[string]any{"projectId": h.proj.ID, "modelId": old.ID, "modelCreatedAt": old.CreatedAt.Format(time.RFC3339Nano)}
	h.call(protocol.MethodWorkbenchModelDelete, p, nil)
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Reused", Task: "custom"}, &replacement)
	err := h.c.Call(h.ctx, protocol.MethodWorkbenchModelDelete, p, nil)
	var rpc *protocol.Error
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeConflict {
		t.Fatalf("stale delete should reject replacement, got %v", err)
	}
	if _, err := workbench.Open(h.ws, replacement.ID); err != nil {
		t.Fatalf("replacement deleted: %v", err)
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
