package server_test

import (
	"errors"
	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
	"testing"
	"time"
)

func TestWorkbenchInferenceRequiresVersionAndAppClient(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Inference", Task: "custom"}, &model)
	p := protocol.WorkbenchInferenceParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: model.CreatedAt.Format(time.RFC3339Nano)}
	var state workbench.InferenceState
	h.call("workbench/inference/get", p, &state)
	if len(state.Checkpoints) != 0 || state.Checkpoints == nil {
		t.Fatalf("empty inference = %+v", state)
	}
	old := p
	p.ModelCreatedAt = ""
	h.callErr("workbench/inference/get", p)
	p = old
	p.ModelCreatedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	h.callErr("workbench/inference/get", p)
	p = old
	p.CheckpointID = "../escape"
	p.Runtime = "pytorch"
	h.callErr("workbench/inference/load", p)
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "untrusted-inference", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, op := range []string{"get", "import", "load", "unload", "predict", "promote", "unpromote", "test-architecture"} {
		err = c.Call(h.ctx, "workbench/inference/"+op, p, nil)
		var rpc *protocol.Error
		if !errors.As(err, &rpc) || rpc.Code != protocol.CodeInvalidRequest {
			t.Fatalf("non-app %s = %v", op, err)
		}
	}
}

func TestWorkbenchDeleteRemovesOnlyMatchingChatPromotion(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Promoted delete", Task: "text-generation"}, &model)
	created := model.CreatedAt.Format(time.RFC3339Nano)
	records := []workbench.Promotion{{ID: "delete", ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: created}, {ID: "keep", ProjectID: "another-project", ModelID: model.ID, ModelCreatedAt: created}}
	if err := h.eng.Store.SetSetting(h.ctx, "workbench.local_models", records); err != nil {
		t.Fatal(err)
	}
	h.call(protocol.MethodWorkbenchModelDelete, protocol.WorkbenchModelDeleteParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: created}, nil)
	var saved []workbench.Promotion
	if err := h.eng.Store.GetSetting(h.ctx, "workbench.local_models", &saved); err != nil || len(saved) != 1 || saved[0].ID != "keep" {
		t.Fatalf("remaining promotions=%+v err=%v", saved, err)
	}
}
