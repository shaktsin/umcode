package server_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func TestWorkbenchRuntimeSelectionUpdatesTrainingAndInference(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Runtime", Task: "custom"}, &model)
	path := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '{\"version\":\"3.13\",\"missing\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := protocol.WorkbenchRuntimeParams{ProjectID: h.proj.ID, Path: path}
	var selected workbench.RuntimeState
	h.call("workbench/runtime/select", p, &selected)
	if !selected.Ready || selected.Selected != path {
		t.Fatalf("selected=%+v", selected)
	}
	var training workbench.TrainingState
	h.call("workbench/training/get", protocol.WorkbenchTrainingParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: model.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")}, &training)
	if !training.RuntimeAvailable {
		t.Fatal("training did not use selected runtime")
	}
	var inference workbench.InferenceState
	h.call("workbench/inference/get", protocol.WorkbenchInferenceParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: model.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")}, &inference)
	if len(inference.Runtimes) != 1 || !inference.Runtimes[0].Available {
		t.Fatalf("inference=%+v", inference.Runtimes)
	}
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "untrusted-runtime", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, op := range []string{"get", "select", "install"} {
		err = c.Call(h.ctx, "workbench/runtime/"+op, p, nil)
		var rpc *protocol.Error
		if !errors.As(err, &rpc) || rpc.Code != protocol.CodeInvalidRequest {
			t.Fatalf("non-app %s=%v", op, err)
		}
	}
}
