package server_test

import (
	"errors"
	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
	"testing"
	"time"
)

func TestWorkbenchTrainingRequiresVersionPreparationAndAppClient(t *testing.T) {
	h := newHarness(t, nil)
	var model workbench.Model
	h.call(protocol.MethodWorkbenchModelCreate, protocol.WorkbenchModelCreateParams{ProjectID: h.proj.ID, Name: "Training", Task: "custom"}, &model)
	p := protocol.WorkbenchTrainingParams{ProjectID: h.proj.ID, ModelID: model.ID, ModelCreatedAt: model.CreatedAt.Format(time.RFC3339Nano)}
	var state workbench.TrainingState
	h.call("workbench/training/get", p, &state)
	if state.Runs == nil || len(state.Runs) != 0 || len(state.Issues) == 0 {
		t.Fatalf("state=%+v", state)
	}
	saved := p
	p.ModelCreatedAt = ""
	h.callErr("workbench/training/get", p)
	p = saved
	p.ModelCreatedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	h.callErr("workbench/training/start", p)
	p = saved
	p.Config.Epochs = 20
	p.Config.BatchSize = 16
	p.Config.LearningRate = .003
	p.Config.Seed = 17
	h.callErr("workbench/training/start", p)
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "untrusted-training", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, op := range []string{"get", "start", "stop"} {
		err = c.Call(h.ctx, "workbench/training/"+op, p, nil)
		var rpc *protocol.Error
		if !errors.As(err, &rpc) || rpc.Code != protocol.CodeInvalidRequest {
			t.Fatalf("non-app %s=%v", op, err)
		}
	}
}
