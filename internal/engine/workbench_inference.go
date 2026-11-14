package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/workbench"
)

const localPromotionsSetting = "workbench.local_models"

type inferenceReceipt struct {
	Key     string
	Expires time.Time
}

func inferencePrefix(p protocol.WorkbenchInferenceParams) string {
	return p.ProjectID + "/" + p.ModelID + "/" + p.ModelCreatedAt + "/"
}
func (e *Engine) inferenceRunner() *workbench.InferenceManager {
	e.inferenceMu.Lock()
	defer e.inferenceMu.Unlock()
	if e.inferenceManager == nil {
		e.inferenceManager = workbench.NewInferenceManager(e.Cfg.Home)
		e.inferenceManager.SetPython(e.runtimeSetup().Selected(context.Background()))
	}
	return e.inferenceManager
}
func (e *Engine) promotions(ctx context.Context) ([]workbench.Promotion, error) {
	var ps []workbench.Promotion
	err := e.Store.GetSetting(ctx, localPromotionsSetting, &ps)
	if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	return ps, err
}
func (e *Engine) inferenceState(ctx context.Context, p protocol.WorkbenchInferenceParams, root string) (workbench.InferenceState, error) {
	cps, err := workbench.ListCheckpoints(root, p.ModelID, p.ModelCreatedAt)
	if err != nil {
		return workbench.InferenceState{}, err
	}
	state := workbench.InferenceState{Checkpoints: cps, Issues: []string{}, Runtimes: []workbench.RuntimeInfo{{ID: "pytorch", Name: "PyTorch", Available: e.inferenceRunner().Available()}}}
	if !state.Runtimes[0].Available {
		state.Runtimes[0].Reason = "Set up Python and PyTorch in the Train tab, then refresh Inference."
	}
	for _, cp := range cps {
		if e.inferenceRunner().Loaded(inferencePrefix(p) + cp.ID) {
			state.LoadedCheckpointID = cp.ID
			break
		}
	}
	e.inferenceMu.Lock()
	ps, err := e.promotions(ctx)
	e.inferenceMu.Unlock()
	if err != nil {
		return state, err
	}
	for _, promotion := range ps {
		if promotion.ProjectID == p.ProjectID && promotion.ModelID == p.ModelID && promotion.ModelCreatedAt == p.ModelCreatedAt {
			copy := promotion
			state.Promotion = &copy
		}
	}
	return state, nil
}
func (e *Engine) WorkbenchInference(ctx context.Context, op string, p protocol.WorkbenchInferenceParams) (any, error) {
	result, err := e.workbenchInference(ctx, op, p)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	return result, nil
}
func (e *Engine) workbenchInference(ctx context.Context, op string, p protocol.WorkbenchInferenceParams) (any, error) {
	if p.ModelCreatedAt == "" {
		return nil, errors.New("model version is required; refresh the model")
	}
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	model, err := matchingWorkbenchModel(project.Root, p.ModelID, p.ModelCreatedAt)
	if err != nil {
		return nil, err
	}
	opened, err := workbench.Open(project.Root, p.ModelID)
	if err != nil {
		return nil, err
	}
	if workbench.IsImportedGraph(opened.Graph) {
		if op == "get" {
			return workbench.InferenceState{Checkpoints: []workbench.Checkpoint{}, Issues: []string{workbench.ImportedRuntimeMessage}, Runtimes: []workbench.RuntimeInfo{}}, nil
		}
		return nil, errors.New(workbench.ImportedRuntimeMessage)
	}
	runner := e.inferenceRunner()
	switch op {
	case "get":
		return e.inferenceState(ctx, p, project.Root)
	case "test-architecture":
		opened, err := workbench.Open(project.Root, p.ModelID)
		if err != nil {
			return nil, err
		}
		return runner.Shape(ctx, opened.Graph)
	case "import":
		return workbench.ImportCheckpoint(project.Root, p.ModelID, p.ModelCreatedAt, p.Name, p.Filename, p.ContentBase64, func(g json.RawMessage, s workbench.DataSelection, b []byte, f string) (workbench.CheckpointValidation, error) {
			return runner.Validate(ctx, g, s, b, f)
		})
	case "unload":
		runner.Unload(inferencePrefix(p))
		return e.inferenceState(ctx, p, project.Root)
	case "unpromote":
		e.inferenceMu.Lock()
		defer e.inferenceMu.Unlock()
		ps, err := e.promotions(ctx)
		if err != nil {
			return nil, err
		}
		kept := []workbench.Promotion{}
		for _, x := range ps {
			if x.ProjectID != p.ProjectID || x.ModelID != p.ModelID || x.ModelCreatedAt != p.ModelCreatedAt {
				kept = append(kept, x)
			}
		}
		return map[string]bool{"ok": true}, e.Store.SetSetting(ctx, localPromotionsSetting, kept)
	}
	if p.Runtime != "pytorch" {
		return nil, errors.New("this custom architecture requires PyTorch; llama.cpp, vLLM and SGLang require compatible pretrained model formats")
	}
	snap, err := workbench.ReadCheckpoint(project.Root, p.ModelID, p.ModelCreatedAt, p.CheckpointID)
	if err != nil {
		return nil, fmt.Errorf("checkpoint unavailable: %w", err)
	}
	key := inferencePrefix(p) + p.CheckpointID
	switch op {
	case "load":
		runner.Unload(inferencePrefix(p))
		if err = runner.Load(ctx, key, snap); err != nil {
			return nil, err
		}
		return e.inferenceState(ctx, p, project.Root)
	case "predict":
		if len(p.Prompt) > 8192 {
			return nil, errors.New("prompt exceeds 8,192 UTF-8 bytes")
		}
		if p.MaxNewTokens < 1 || p.MaxNewTokens > 256 || p.Temperature < 0 || p.Temperature > 2 {
			return nil, errors.New("use 1–256 output tokens and temperature 0–2")
		}
		if len(p.ImageBase64) > ((8<<20)+2)/3*4 {
			return nil, errors.New("image exceeds 8 MiB")
		}
		prompt := p.Prompt
		if snap.Selection.Config.Purpose == "text-generation" {
			prompt, _, err = workbenchLocalPromptFormat(nil, "", prompt, snap.Checkpoint.Input.SequenceLength, p.MaxNewTokens, snap.Checkpoint.PromptFormat)
			if err != nil {
				return nil, err
			}
		}
		prediction, err := runner.Predict(ctx, key, snap, map[string]any{"prompt": prompt, "features": p.Features, "imageBase64": p.ImageBase64, "temperature": p.Temperature, "maxNewTokens": p.MaxNewTokens})
		if err != nil {
			return nil, err
		}
		prediction.TestID = store.NewID("test")
		if snap.Checkpoint.ChatCompatible && strings.TrimSpace(prediction.Text) == "" {
			return nil, errors.New("checkpoint returned no text; try a different prompt or checkpoint")
		}
		e.inferenceMu.Lock()
		if e.inferenceReceipts == nil || len(e.inferenceReceipts) >= 128 {
			e.inferenceReceipts = map[string]inferenceReceipt{}
		}
		e.inferenceReceipts[prediction.TestID] = inferenceReceipt{key, time.Now().Add(10 * time.Minute)}
		e.inferenceMu.Unlock()
		return prediction, nil
	case "promote":
		if !snap.Checkpoint.ChatCompatible {
			return nil, errors.New("only a causal text-generation checkpoint can be promoted to Chat")
		}
		e.inferenceMu.Lock()
		defer e.inferenceMu.Unlock()
		receipt, ok := e.inferenceReceipts[p.TestID]
		if !ok || receipt.Key != key || time.Now().After(receipt.Expires) {
			return nil, errors.New("test this checkpoint successfully before promoting it to Chat")
		}
		ps, err := e.promotions(ctx)
		if err != nil {
			return nil, err
		}
		kept := []workbench.Promotion{}
		promotion := workbench.Promotion{PromptFormat: snap.Checkpoint.PromptFormat, ID: store.NewID("wbl"), ProjectID: p.ProjectID, ModelID: p.ModelID, ModelCreatedAt: p.ModelCreatedAt, CheckpointID: p.CheckpointID, Name: model.Name + " · " + snap.Checkpoint.Name, Runtime: p.Runtime, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		for _, x := range ps {
			if x.ProjectID == p.ProjectID && x.ModelID == p.ModelID {
				if x.ModelCreatedAt == p.ModelCreatedAt && x.CheckpointID == p.CheckpointID {
					promotion.ID = x.ID
				}
			} else {
				kept = append(kept, x)
			}
		}
		kept = append(kept, promotion)
		if err = e.Store.SetSetting(ctx, localPromotionsSetting, kept); err != nil {
			return nil, err
		}
		return promotion, nil
	}
	return nil, errors.New("unknown inference operation")
}
func (e *Engine) WorkbenchLocalPromotion(ctx context.Context, id string) (workbench.Promotion, error) {
	e.inferenceMu.Lock()
	ps, err := e.promotions(ctx)
	e.inferenceMu.Unlock()
	if err != nil {
		return workbench.Promotion{}, err
	}
	for _, p := range ps {
		if p.ID == id {
			project, err := e.project(ctx, p.ProjectID)
			if err != nil {
				return p, fmt.Errorf("promotion project is unavailable: %w", err)
			}
			snap, err := workbench.ReadCheckpoint(project.Root, p.ModelID, p.ModelCreatedAt, p.CheckpointID)
			if err != nil {
				return p, fmt.Errorf("promotion checkpoint is unavailable: %w", err)
			}
			if !snap.Checkpoint.ChatCompatible || p.Runtime != "pytorch" {
				return p, errors.New("promotion is not a compatible local chat checkpoint")
			}
			p.PromptFormat = snap.Checkpoint.PromptFormat
			return p, nil
		}
	}
	return workbench.Promotion{}, errors.New("local model promotion is unavailable; test and promote a checkpoint in the Inference tab")
}
func (e *Engine) LocalWorkbenchModels(ctx context.Context, provider string) ([]protocol.Model, error) {
	result := []protocol.Model{}
	if provider != "" && provider != "workbench_local" {
		return result, nil
	}
	e.inferenceMu.Lock()
	ps, err := e.promotions(ctx)
	e.inferenceMu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if _, err := e.WorkbenchLocalPromotion(ctx, p.ID); err != nil {
			continue
		}
		project, err := e.project(ctx, p.ProjectID)
		if err != nil {
			continue
		}
		snap, err := workbench.ReadCheckpoint(project.Root, p.ModelID, p.ModelCreatedAt, p.CheckpointID)
		if err != nil {
			continue
		}
		result = append(result, protocol.Model{Provider: "workbench_local", ID: p.ID, DisplayName: p.Name, ContextWindow: snap.Checkpoint.Input.SequenceLength, Source: "workbench"})
	}
	return result, nil
}
func (e *Engine) GenerateWorkbenchLocal(ctx context.Context, id, prompt string, maxTokens int, temperature float64) (workbench.Prediction, error) {
	p, err := e.WorkbenchLocalPromotion(ctx, id)
	if err != nil {
		return workbench.Prediction{}, err
	}
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return workbench.Prediction{}, err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	snap, err := workbench.ReadCheckpoint(project.Root, p.ModelID, p.ModelCreatedAt, p.CheckpointID)
	if err != nil {
		return workbench.Prediction{}, err
	}
	return e.inferenceRunner().Predict(ctx, inferencePrefix(protocol.WorkbenchInferenceParams{ProjectID: p.ProjectID, ModelID: p.ModelID, ModelCreatedAt: p.ModelCreatedAt})+p.CheckpointID, snap, map[string]any{"prompt": prompt, "maxNewTokens": maxTokens, "temperature": temperature})
}

func (e *Engine) removeWorkbenchPromotion(ctx context.Context, projectID, modelID, createdAt string) error {
	e.inferenceMu.Lock()
	defer e.inferenceMu.Unlock()
	ps, err := e.promotions(ctx)
	if err != nil {
		return err
	}
	kept := []workbench.Promotion{}
	for _, p := range ps {
		if p.ProjectID != projectID || p.ModelID != modelID || p.ModelCreatedAt != createdAt {
			kept = append(kept, p)
		}
	}
	return e.Store.SetSetting(ctx, localPromotionsSetting, kept)
}
