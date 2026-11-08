package server_test

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func TestWorkbenchDataRPCPersistsAndIsProjectScoped(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Data test", Task: "classification"}, &model)
	var before workbench.DataState
	h.call(protocol.MethodWorkbenchDataGet, protocol.WorkbenchDataGetParams{ProjectID: h.proj.ID, ModelID: model.ID}, &before)
	if before.Datasets == nil || len(before.Datasets) != 0 || before.Dataset != nil || before.Selection != nil {
		t.Fatalf("new model data = %+v", before)
	}
	var imported workbench.Dataset
	h.call(protocol.MethodWorkbenchDataImport, protocol.WorkbenchDataImportParams{ProjectID: h.proj.ID, ModelID: model.ID, Source: workbench.DataSource{Kind: "local", Name: "Customers"}, Files: []workbench.DataFile{{Name: "customers.csv", ContentBase64: base64.StdEncoding.EncodeToString([]byte("age,visits,label\n20,1,no\n30,3,yes\n40,5,no\n50,7,yes\n60,9,no\n70,11,yes\n80,13,no\n90,15,yes\n100,17,no\n110,19,yes\n"))}}}, &imported)
	if imported.RowCount != 10 || len(imported.Preview) != 10 || imported.ID == "" {
		t.Fatalf("import = %+v", imported)
	}
	config := workbench.DataConfig{DatasetID: imported.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"age", "visits"}, TargetColumn: "label", Scale: "standard", TrainPercent: 80, ValidationPercent: 10, TestPercent: 10, Seed: 42, Stratify: true}
	var selection workbench.DataSelection
	h.call(protocol.MethodWorkbenchDataPrepare, protocol.WorkbenchDataPrepareParams{ProjectID: h.proj.ID, ModelID: model.ID, Config: config}, &selection)
	if selection.Version == "" || selection.DatasetID != imported.ID || selection.Counts.Train+selection.Counts.Validation+selection.Counts.Test != 10 || len(selection.Statistics) != 2 {
		t.Fatalf("prepared = %+v", selection)
	}
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "data-reopen", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var reopened workbench.DataState
	if err := c.Call(h.ctx, protocol.MethodWorkbenchDataGet, protocol.WorkbenchDataGetParams{ProjectID: h.proj.ID, ModelID: model.ID}, &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.Dataset == nil || reopened.Dataset.ID != imported.ID || reopened.Selection == nil || reopened.Selection.Version != selection.Version {
		t.Fatalf("reopened = %+v", reopened)
	}
	var other protocol.Project
	h.call(protocol.MethodProjectCreate, protocol.ProjectCreateParams{Root: t.TempDir(), Name: "Other data project"}, &other)
	h.callErr(protocol.MethodWorkbenchDataGet, protocol.WorkbenchDataGetParams{ProjectID: other.ID, ModelID: model.ID, DatasetID: imported.ID})
	h.callErr(protocol.MethodWorkbenchDataGet, protocol.WorkbenchDataGetParams{ProjectID: h.proj.ID, ModelID: "../escape"})
	config.InputColumns = []string{"label"}
	h.callErr(protocol.MethodWorkbenchDataPrepare, protocol.WorkbenchDataPrepareParams{ProjectID: h.proj.ID, ModelID: model.ID, Config: config})
	h.call(protocol.MethodWorkbenchDataGet, protocol.WorkbenchDataGetParams{ProjectID: h.proj.ID, ModelID: model.ID}, &reopened)
	if reopened.Selection == nil || reopened.Selection.Version != selection.Version {
		t.Fatal("invalid preparation destroyed saved selection")
	}
	if _, err := os.Stat(filepath.Join(h.ws, "models", model.ID, "data")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkbenchDataRPCRequiresExplicitAppClient(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Data admin", Task: "classification"}, &model)
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "untrusted-data", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, method := range []string{protocol.MethodWorkbenchDataGet, protocol.MethodWorkbenchDataImport, protocol.MethodWorkbenchDataPrepare, protocol.MethodWorkbenchDataHuggingFace} {
		err := c.Call(h.ctx, method, map[string]any{"projectId": h.proj.ID, "modelId": model.ID, "action": "status"}, nil)
		var rpc *protocol.Error
		if !errors.As(err, &rpc) || rpc.Code != protocol.CodeInvalidRequest {
			t.Fatalf("non-app %s error = %v; want explicit app restriction", method, err)
		}
	}
}
