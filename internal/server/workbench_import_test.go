package server_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
	"testing"
	"time"
)

func TestWorkbenchImportWeightMetadataAndEditedReopen(t *testing.T) {
	h := newHarness(t, nil)
	config := `{"model_type":"resnet","architectures":["ResNetForImageClassification"],"hidden_sizes":[8,16],"depths":[1,1],"embedding_size":8,"layer_type":"basic","num_labels":2}`
	header := `{"resnet.embedder.embedder.convolution.weight":{"dtype":"F32","shape":[8,3,7,7],"data_offsets":[0,4704]}}`
	params := protocol.WorkbenchImportParams{ProjectID: h.proj.ID, Files: []workbench.ImportFile{
		{Name: "config.json", Size: int64(len(config)), Kind: "config", Content: config},
		{Name: "model.safetensors", Size: int64(8 + len(header) + 4704), Kind: "weights", Header: header},
	}}
	var inspection workbench.ImportInspection
	h.call("workbench/import/inspect", params, &inspection)
	if inspection.Family != "resnet" || !inspection.Supported || !inspection.WeightsVerified || inspection.TensorCount != 1 {
		t.Fatalf("unexpected header inspection: %+v", inspection)
	}
	graph, _ := json.Marshal(inspection.Graph)
	var imported struct {
		workbench.OpenResult
		GraphJSON string `json:"graphJson"`
	}
	h.call("workbench/import/create", protocol.WorkbenchImportParams{ProjectID: h.proj.ID, Name: "CNN metadata", GraphJSON: string(graph)}, &imported)
	var edited map[string]any
	if err := json.Unmarshal(imported.Graph, &edited); err != nil {
		t.Fatal(err)
	}
	for _, item := range edited["nodes"].([]any) {
		node := item.(map[string]any)
		if node["kind"] == "conv2d" {
			node["config"].(map[string]any)["outChannels"] = 12
		}
	}
	raw, _ := json.Marshal(edited)
	// The app sends this exact JSON shape when saving an edited imported graph.
	h.call(protocol.MethodWorkbenchGraphSave, map[string]any{"projectId": h.proj.ID, "modelId": imported.Model.ID, "graphJson": string(raw), "modelCreatedAt": imported.Model.CreatedAt.Format(time.RFC3339Nano)}, nil)
	h.call(protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: h.proj.ID, ModelID: imported.Model.ID}, &imported)
	var reopened workbench.ImportedGraph
	if err := json.Unmarshal(imported.Graph, &reopened); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range reopened.Nodes {
		if node.Kind == "conv2d" {
			found = fmt.Sprint(node.Config["outChannels"]) == "12"
		}
	}
	if !found || len(reopened.Source.Tensors) != 1 || reopened.Source.Tensors[0].Shape[0] != 8 {
		t.Fatal("edited architecture or original tensor shape was lost after reopening")
	}
}

func TestWorkbenchImportRoundTripScopeAndRuntimeGuard(t *testing.T) {
	h := newHarness(t, nil)
	config := `{"model_type":"gpt2","n_embd":16,"n_layer":2,"n_head":4,"vocab_size":32,"n_positions":16}`
	params := protocol.WorkbenchImportParams{ProjectID: h.proj.ID, Files: []workbench.ImportFile{{Name: "config.json", Size: int64(len(config)), Kind: "config", Content: config}}}
	h.callErr("workbench/import/inspect", params)
	header := `{"transformer.wte.weight":{"dtype":"F32","shape":[32,16],"data_offsets":[0,2048]}}`
	params.Files = append(params.Files, workbench.ImportFile{Name: "model.safetensors", Kind: "weights", Size: int64(8 + len(header) + 2048), Header: header})
	h.callErr("workbench/import/inspect", protocol.WorkbenchImportParams{ProjectID: h.proj.ID, Files: params.Files[1:]})
	var inspection workbench.ImportInspection
	h.call("workbench/import/inspect", params, &inspection)
	if !inspection.Supported || !inspection.WeightsVerified || inspection.Family != "gpt2" {
		t.Fatalf("inspect=%+v", inspection)
	}
	graph, err := json.Marshal(inspection.Graph)
	if err != nil {
		t.Fatal(err)
	}
	var imported struct {
		workbench.OpenResult
		GraphJSON string `json:"graphJson"`
	}
	create := protocol.WorkbenchImportParams{ProjectID: h.proj.ID, Name: "GPT layers", GraphJSON: string(graph)}
	h.call("workbench/import/create", create, &imported)
	if !workbench.IsImportedGraph(imported.Graph) || imported.GraphJSON == "" {
		t.Fatal("import didn't preserve native-family architecture")
	}
	h.callErr("workbench/import/create", create)
	version := imported.Model.CreatedAt.Format(time.RFC3339Nano)
	var runtime workbench.ImportRuntimeState
	h.call("workbench/import/runtime", protocol.ImportRuntimeParams{ProjectID: h.proj.ID, ModelID: imported.Model.ID, ModelCreatedAt: version, Action: "status"}, &runtime)
	if runtime.Ready || runtime.Reason == "" {
		t.Fatalf("partial import not gated: %+v", runtime)
	}
	h.callErr("workbench/data/get", protocol.WorkbenchDataGetParams{ProjectID: h.proj.ID, ModelID: imported.Model.ID})
	var training workbench.TrainingState
	h.call("workbench/training/get", protocol.WorkbenchTrainingParams{ProjectID: h.proj.ID, ModelID: imported.Model.ID, ModelCreatedAt: version}, &training)
	if len(training.Issues) == 0 || training.RuntimeAvailable {
		t.Fatalf("import claimed training support %+v", training)
	}
	var inference workbench.InferenceState
	h.call("workbench/inference/get", protocol.WorkbenchInferenceParams{ProjectID: h.proj.ID, ModelID: imported.Model.ID, ModelCreatedAt: version}, &inference)
	if len(inference.Issues) == 0 || len(inference.Checkpoints) != 0 {
		t.Fatalf("import claimed inference support %+v", inference)
	}
	h.callErr("workbench/inference/test-architecture", protocol.WorkbenchInferenceParams{ProjectID: h.proj.ID, ModelID: imported.Model.ID, ModelCreatedAt: version})
	c, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "untrusted-import", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, op := range []string{"inspect", "create"} {
		err = c.Call(h.ctx, "workbench/import/"+op, create, nil)
		var rpc *protocol.Error
		if !errors.As(err, &rpc) || rpc.Code != protocol.CodeInvalidRequest {
			t.Fatalf("non-app %s=%v", op, err)
		}
	}
	params.ProjectID = "missing"
	h.callErr("workbench/import/inspect", params)
	h.call(protocol.MethodWorkbenchModelOpen, protocol.WorkbenchModelOpenParams{ProjectID: h.proj.ID, ModelID: imported.Model.ID}, &imported)
}
