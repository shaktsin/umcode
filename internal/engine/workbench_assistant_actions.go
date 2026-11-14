package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/workbench"
)

func workbenchActionSpecs() []llm.ToolSpec {
	return []llm.ToolSpec{
		{Name: tools.ToWire("workbench.save_build"), Description: "Apply supported changes to the current Build draft and save it in this model's project. An empty operations list saves the current draft. Use only when the user asks to make or save a change; use propose_edit for preview-only requests.", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operations"],"properties":{"operations":{"type":"array","maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["type"],"properties":{"type":{"type":"string","enum":["update","remove","connect","disconnect"]},"nodeId":{"type":"string"},"config":{"type":"object"},"source":{"type":"string"},"target":{"type":"string"},"edgeId":{"type":"string"}}}}}}`)},
		{Name: tools.ToWire("workbench.save_data"), Description: "Save Data preparation for this model using the selected imported dataset. Supply the complete Data configuration from the saved status with requested changes. Recomputes train, validation and test splits.", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["config"],"properties":{"config":{"type":"object"}}}`)},
		{Name: tools.ToWire("workbench.train"), Description: "Start or stop a local training run for the selected Workbench model. Starting returns a running run ID, not a completed result. Supply training config when starting and runId when stopping.", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation"],"properties":{"operation":{"type":"string","enum":["start","stop"]},"config":{"type":"object"},"runId":{"type":"string"}}}`)},
		{Name: tools.ToWire("workbench.test_checkpoint"), Description: "Run local PyTorch inference for an existing checkpoint of this model and return the actual prediction. Supply prompt for text or numeric features for a feature model.", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["checkpointId"],"properties":{"checkpointId":{"type":"string"},"prompt":{"type":"string"},"features":{"type":"object","additionalProperties":{"type":"number"}},"temperature":{"type":"number"},"maxNewTokens":{"type":"integer"}}}`)},
	}
}

type buildActionOperation struct {
	Type   string         `json:"type"`
	NodeID string         `json:"nodeId"`
	Config map[string]any `json:"config"`
	Source string         `json:"source"`
	Target string         `json:"target"`
	EdgeID string         `json:"edgeId"`
}

func applyBuildOperations(graphText string, operations []json.RawMessage) ([]byte, error) {
	if len(operations) > 32 {
		return nil, errors.New("Build action exceeds 32 operations")
	}
	var graph map[string]any
	if err := json.Unmarshal([]byte(graphText), &graph); err != nil {
		return nil, err
	}
	nodes, ok := graph["nodes"].([]any)
	if !ok {
		return nil, errors.New("Build graph has no layers")
	}
	edges, ok := graph["edges"].([]any)
	if !ok {
		return nil, errors.New("Build graph has no connections")
	}
	findNode := func(id string) (map[string]any, int) {
		for i, raw := range nodes {
			if node, ok := raw.(map[string]any); ok && node["id"] == id {
				return node, i
			}
		}
		return nil, -1
	}
	for _, raw := range operations {
		var op buildActionOperation
		if err := strictWorkbenchJSON(raw, &op); err != nil {
			return nil, err
		}
		switch op.Type {
		case "update":
			node, _ := findNode(op.NodeID)
			if node == nil || len(op.Config) == 0 {
				return nil, errors.New("update needs an existing layer and settings")
			}
			current, ok := node["config"].(map[string]any)
			if !ok {
				return nil, errors.New("layer settings unavailable")
			}
			for key, value := range op.Config {
				old, exists := current[key]
				if !exists {
					return nil, fmt.Errorf("unsupported setting %s", key)
				}
				if !sameScalarKind(old, value) {
					return nil, fmt.Errorf("invalid value for %s", key)
				}
				current[key] = value
			}
		case "remove":
			node, i := findNode(op.NodeID)
			if i < 0 {
				return nil, errors.New("layer to remove is unavailable")
			}
			if node["kind"] == "input" || node["kind"] == "output" {
				return nil, errors.New("the input and output layers cannot be removed by an assistant action")
			}
			nodes = append(nodes[:i], nodes[i+1:]...)
			kept := make([]any, 0, len(edges))
			for _, rawEdge := range edges {
				edge, ok := rawEdge.(map[string]any)
				if ok && edge["source"] != op.NodeID && edge["target"] != op.NodeID {
					kept = append(kept, rawEdge)
				}
			}
			edges = kept
		case "connect":
			if op.Source == op.Target || op.Source == "" || op.Target == "" {
				return nil, errors.New("connection needs different source and target layers")
			}
			source, _ := findNode(op.Source)
			if source == nil {
				return nil, errors.New("connection source is unavailable")
			}
			target, _ := findNode(op.Target)
			if target == nil {
				return nil, errors.New("connection target is unavailable")
			}
			if source["kind"] == "output" || target["kind"] == "input" {
				return nil, errors.New("connection has an invalid input or output port")
			}
			for _, rawEdge := range edges {
				edge, _ := rawEdge.(map[string]any)
				if edge["source"] == op.Source || edge["target"] == op.Target {
					return nil, errors.New("a layer port is already connected")
				}
			}
			visited := map[string]bool{}
			cursor := op.Target
			for cursor != "" && !visited[cursor] {
				if cursor == op.Source {
					return nil, errors.New("connection would create a loop")
				}
				visited[cursor] = true
				next := ""
				for _, rawEdge := range edges {
					edge, _ := rawEdge.(map[string]any)
					if edge["source"] == cursor {
						next, _ = edge["target"].(string)
						break
					}
				}
				cursor = next
			}
			edges = append(edges, map[string]any{"id": store.NewID("edge"), "source": op.Source, "target": op.Target})
		case "disconnect":
			i := -1
			for j, rawEdge := range edges {
				edge, _ := rawEdge.(map[string]any)
				if edge["id"] == op.EdgeID {
					i = j
					break
				}
			}
			if i < 0 {
				return nil, errors.New("connection to remove is unavailable")
			}
			edges = append(edges[:i], edges[i+1:]...)
		default:
			return nil, errors.New("unsupported Build action; use a preview for additions or imported layers")
		}
	}
	graph["nodes"], graph["edges"] = nodes, edges
	return json.Marshal(graph)
}
func sameScalarKind(a, b any) bool {
	switch a.(type) {
	case string:
		_, ok := b.(string)
		return ok
	case bool:
		_, ok := b.(bool)
		return ok
	case float64:
		_, ok := b.(float64)
		return ok
	default:
		return false
	}
}

func (e *Engine) saveWorkbenchBuild(ctx context.Context, args json.RawMessage) (string, error) {
	c := workbenchContextFrom(ctx)
	if c == nil {
		return "", errors.New("Build action requires a Workbench turn")
	}
	if c.BuildSaved {
		return "", errors.New("Build was already saved in this turn; ask again with the updated graph for another edit")
	}
	if len(args) > 65536 {
		return "", errors.New("Build action exceeds 64 KiB")
	}
	var request struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := strictWorkbenchJSON(args, &request); err != nil {
		return "", err
	}
	if request.Operations == nil {
		return "", errors.New("operations are required; use an empty list to save the current draft")
	}
	if workbench.IsImportedGraph([]byte(c.GraphJSON)) {
		return "", errors.New("automatic Build saving supports native architectures only; preview imported changes")
	}
	updated, err := applyBuildOperations(c.GraphJSON, request.Operations)
	if err != nil {
		return "", err
	}
	project, err := e.project(ctx, c.ProjectID)
	if err != nil {
		return "", err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	if _, err = matchingWorkbenchModel(project.Root, c.ModelID, c.ModelCreatedAt); err != nil {
		return "", err
	}
	opened, err := workbench.Open(project.Root, c.ModelID)
	if err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", sha256.Sum256(opened.Graph)) != c.SavedGraphHash {
		return "", errors.New("saved Build graph changed during this chat turn; ask again with the latest model")
	}
	if err = workbench.SaveGraph(project.Root, c.ModelID, updated); err != nil {
		return "", err
	}
	saved, err := workbench.Open(project.Root, c.ModelID)
	if err != nil {
		return "", err
	}
	c.SavedGraphHash = fmt.Sprintf("%x", sha256.Sum256(saved.Graph))
	c.BuildSaved = true
	result, _ := json.Marshal(map[string]any{"action": "save_build", "saved": true, "projectId": c.ProjectID, "modelId": c.ModelID, "modelCreatedAt": c.ModelCreatedAt, "baseDraftRevision": c.Revision})
	return string(result), nil
}

func (e *Engine) saveWorkbenchData(ctx context.Context, args json.RawMessage) (string, error) {
	c := workbenchContextFrom(ctx)
	if c == nil {
		return "", errors.New("Data action requires a Workbench turn")
	}
	if len(args) > 32768 {
		return "", errors.New("Data action exceeds 32 KiB")
	}
	var request struct {
		Config workbench.DataConfig `json:"config"`
	}
	if err := strictWorkbenchJSON(args, &request); err != nil {
		return "", err
	}
	project, err := e.project(ctx, c.ProjectID)
	if err != nil {
		return "", err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	if _, err = matchingWorkbenchModel(project.Root, c.ModelID, c.ModelCreatedAt); err != nil {
		return "", err
	}
	data, err := workbench.ReadData(project.Root, c.ModelID, "")
	if err != nil {
		return "", err
	}
	version := ""
	if data.Selection != nil {
		version = data.Selection.Version
	}
	if version != c.DataVersion {
		return "", errors.New("saved Data preparation changed during this chat turn; ask again with the latest model")
	}
	opened, err := workbench.Open(project.Root, c.ModelID)
	if err != nil {
		return "", err
	}
	if workbench.IsImportedGraph(opened.Graph) {
		return "", errors.New("automatic Data preparation supports native models only")
	}
	selection, err := workbench.PrepareData(project.Root, c.ModelID, request.Config)
	if err != nil {
		return "", err
	}
	c.DataVersion = selection.Version
	_ = e.Store.Audit(ctx, "workbench.data.prepare", map[string]any{"projectId": c.ProjectID, "modelId": c.ModelID, "datasetId": selection.DatasetID, "version": selection.Version})
	result, _ := json.Marshal(map[string]any{"action": "save_data", "saved": true, "projectId": c.ProjectID, "modelId": c.ModelID, "modelCreatedAt": c.ModelCreatedAt, "datasetId": selection.DatasetID, "version": selection.Version, "counts": selection.Counts})
	return string(result), nil
}

func (e *Engine) workbenchTrainAction(ctx context.Context, args json.RawMessage) (string, error) {
	c := workbenchContextFrom(ctx)
	if c == nil {
		return "", errors.New("Train action requires a Workbench turn")
	}
	if len(args) > 8192 {
		return "", errors.New("Train action exceeds 8 KiB")
	}
	var request struct {
		Operation string                   `json:"operation"`
		Config    workbench.TrainingConfig `json:"config"`
		RunID     string                   `json:"runId"`
	}
	if err := strictWorkbenchJSON(args, &request); err != nil {
		return "", err
	}
	p := protocol.WorkbenchTrainingParams{ProjectID: c.ProjectID, ModelID: c.ModelID, ModelCreatedAt: c.ModelCreatedAt, RunID: request.RunID}
	if request.Operation == "start" {
		if err := request.Config.Validate(); err != nil {
			return "", err
		}
		p.CheckSnapshot = true
		p.ExpectedGraphHash = c.SavedGraphHash
		p.ExpectedDataVersion = c.DataVersion
		p.Config.Epochs = request.Config.Epochs
		p.Config.BatchSize = request.Config.BatchSize
		p.Config.LearningRate = request.Config.LearningRate
		p.Config.Seed = request.Config.Seed
	} else if request.Operation == "stop" {
		if request.RunID == "" {
			return "", errors.New("stop requires the active runId")
		}
	} else {
		return "", errors.New("operation must be start or stop")
	}
	value, err := e.workbenchTraining(ctx, request.Operation, p)
	if err != nil {
		return "", err
	}
	run, ok := value.(workbench.TrainingRun)
	if !ok {
		return "", errors.New("training action returned no run")
	}
	result, _ := json.Marshal(map[string]any{"action": "train", "operation": request.Operation, "projectId": c.ProjectID, "modelId": c.ModelID, "modelCreatedAt": c.ModelCreatedAt, "runId": run.ID, "status": run.Status, "error": run.Error})
	return string(result), nil
}

func (e *Engine) workbenchCheckpointTest(ctx context.Context, args json.RawMessage) (string, error) {
	c := workbenchContextFrom(ctx)
	if c == nil {
		return "", errors.New("checkpoint test requires a Workbench turn")
	}
	if len(args) > 16<<10 {
		return "", errors.New("checkpoint test exceeds 16 KiB")
	}
	var request struct {
		CheckpointID string             `json:"checkpointId"`
		Prompt       string             `json:"prompt"`
		Features     map[string]float64 `json:"features"`
		Temperature  float64            `json:"temperature"`
		MaxNewTokens int                `json:"maxNewTokens"`
	}
	if err := strictWorkbenchJSON(args, &request); err != nil {
		return "", err
	}
	if request.CheckpointID == "" {
		return "", errors.New("choose a saved checkpoint")
	}
	if request.MaxNewTokens == 0 {
		request.MaxNewTokens = 32
	}
	p := protocol.WorkbenchInferenceParams{ProjectID: c.ProjectID, ModelID: c.ModelID, ModelCreatedAt: c.ModelCreatedAt, CheckpointID: request.CheckpointID, Runtime: "pytorch", Prompt: request.Prompt, Features: request.Features, Temperature: request.Temperature, MaxNewTokens: request.MaxNewTokens}
	value, err := e.workbenchInference(ctx, "predict", p)
	if err != nil {
		return "", err
	}
	prediction, ok := value.(workbench.Prediction)
	if !ok {
		return "", errors.New("inference returned no prediction")
	}
	truncated := len(prediction.Scores) > 20
	if truncated {
		prediction.Scores = prediction.Scores[:20]
	}
	result, _ := json.Marshal(map[string]any{"action": "test_checkpoint", "projectId": c.ProjectID, "modelId": c.ModelID, "modelCreatedAt": c.ModelCreatedAt, "checkpointId": request.CheckpointID, "prediction": prediction, "scoresTruncated": truncated})
	return string(result), nil
}

func (e *Engine) runWorkbenchAction(ctx, sctx context.Context, turn protocol.Turn, call llm.ToolCall) toolRunResult {
	name := tools.FromWire(call.Name)
	it, err := e.newItem(sctx, turn, protocol.ItemToolCall)
	if err != nil {
		return toolRunResult{Output: err.Error(), ModelOutput: err.Error(), IsError: true}
	}
	it.Tool = &protocol.ToolCallData{CallID: call.ID, Name: name, Args: call.Args, Risk: string(tools.RiskYellow)}
	if err = e.saveAndPublish(sctx, it, protocol.NotifyItemStarted); err != nil {
		return toolRunResult{Output: err.Error(), ModelOutput: err.Error(), IsError: true}
	}
	var output string
	switch name {
	case "workbench.save_build":
		output, err = e.saveWorkbenchBuild(ctx, call.Args)
	case "workbench.save_data":
		output, err = e.saveWorkbenchData(ctx, call.Args)
	case "workbench.train":
		output, err = e.workbenchTrainAction(ctx, call.Args)
	case "workbench.test_checkpoint":
		output, err = e.workbenchCheckpointTest(ctx, call.Args)
	default:
		err = errors.New("unsupported Workbench action")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		it.Status = protocol.ItemFailed
		it.Tool.Error = err.Error()
		_ = e.saveAndPublish(sctx, it, protocol.NotifyItemCompleted)
		return toolRunResult{Output: err.Error(), ModelOutput: err.Error(), IsError: true}
	}
	it.Status, it.Tool.Output = protocol.ItemCompleted, output
	if err = e.saveAndPublish(sctx, it, protocol.NotifyItemCompleted); err != nil {
		return toolRunResult{Output: err.Error(), ModelOutput: err.Error(), IsError: true}
	}
	if name == "workbench.train" {
		var result struct {
			Operation string `json:"operation"`
			RunID     string `json:"runId"`
		}
		if json.Unmarshal([]byte(output), &result) == nil && result.Operation == "start" {
			if c := workbenchContextFrom(ctx); c != nil {
				c.StartedRunIDs = append(c.StartedRunIDs, result.RunID)
			}
		}
	}
	return toolRunResult{Output: output, ModelOutput: output}
}
