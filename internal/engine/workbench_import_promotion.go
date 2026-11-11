package engine

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/workbench"
)

func importedTestKey(p protocol.ImportRuntimeParams, fingerprint string) string {
	return inferencePrefix(protocol.WorkbenchInferenceParams{ProjectID: p.ProjectID, ModelID: p.ModelID, ModelCreatedAt: p.ModelCreatedAt}) + "imported/" + p.Checkpoint + "/" + fingerprint
}

func (e *Engine) testImportedChat(ctx context.Context, target workbench.BundleTarget, p protocol.ImportRuntimeParams) (any, error) {
	before, _, identityErr := workbench.ImportedChatIdentity(target, p.Checkpoint)
	result, err := e.importedRunner().Infer(ctx, target, p.Prompt, p.ImageBase64, p.Checkpoint, p.MaxTokens)
	if err != nil {
		return nil, err
	}
	text, _ := result["text"].(string)
	if result["chatCompatible"] != true || strings.TrimSpace(text) == "" {
		return result, nil
	}
	after, _, err := workbench.ImportedChatIdentity(target, p.Checkpoint)
	if identityErr != nil || err != nil || before != after {
		return nil, errors.New("Model changed during the prompt test. Test the selected weights again.")
	}
	testID := store.NewID("test")
	e.inferenceMu.Lock()
	if e.inferenceReceipts == nil || len(e.inferenceReceipts) >= 128 {
		e.inferenceReceipts = map[string]inferenceReceipt{}
	}
	e.inferenceReceipts[testID] = inferenceReceipt{Key: importedTestKey(p, after), Expires: time.Now().Add(10 * time.Minute)}
	e.inferenceMu.Unlock()
	result["testId"] = testID
	return result, nil
}

func (e *Engine) promoteImportedChat(ctx context.Context, target workbench.BundleTarget, p protocol.ImportRuntimeParams, name string) (workbench.Promotion, error) {
	state, err := e.importedRunner().Status(target)
	if err != nil {
		return workbench.Promotion{}, err
	}
	if !state.Ready {
		return workbench.Promotion{}, errors.New(state.Reason)
	}
	fingerprint, window, err := workbench.ImportedChatIdentity(target, p.Checkpoint)
	if err != nil {
		return workbench.Promotion{}, err
	}
	e.inferenceMu.Lock()
	defer e.inferenceMu.Unlock()
	receipt, ok := e.inferenceReceipts[p.TestID]
	if !ok || receipt.Key != importedTestKey(p, fingerprint) || time.Now().After(receipt.Expires) {
		return workbench.Promotion{}, errors.New("Run a successful prompt test with these weights and a compatible chat tokenizer before promoting to Chat.")
	}
	ps, err := e.promotions(ctx)
	if err != nil {
		return workbench.Promotion{}, err
	}
	weights := "Original weights"
	if p.Checkpoint != "" {
		weights = p.Checkpoint
	}
	promotion := workbench.Promotion{ID: store.NewID("wbl"), ProjectID: p.ProjectID, ModelID: p.ModelID, ModelCreatedAt: p.ModelCreatedAt, CheckpointID: p.Checkpoint, Name: name + " · " + weights, Runtime: "transformers", PromptFormat: "imported-chat-v1", SourceFingerprint: fingerprint, ContextWindow: window, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	kept := []workbench.Promotion{}
	for _, old := range ps {
		if old.ProjectID == p.ProjectID && old.ModelID == p.ModelID {
			if old.ModelCreatedAt == p.ModelCreatedAt && old.Runtime == promotion.Runtime && old.CheckpointID == p.Checkpoint && old.SourceFingerprint == fingerprint {
				promotion.ID = old.ID
			}
		} else {
			kept = append(kept, old)
		}
	}
	return promotion, e.Store.SetSetting(ctx, localPromotionsSetting, append(kept, promotion))
}

func (e *Engine) importedPromotion(ctx context.Context, p protocol.ImportRuntimeParams) (*workbench.Promotion, error) {
	e.inferenceMu.Lock()
	ps, err := e.promotions(ctx)
	e.inferenceMu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, promotion := range ps {
		if promotion.ProjectID == p.ProjectID && promotion.ModelID == p.ModelID && promotion.ModelCreatedAt == p.ModelCreatedAt && promotion.Runtime == "transformers" {
			validated, err := e.WorkbenchLocalPromotion(ctx, promotion.ID)
			if err == nil {
				return &validated, nil
			}
		}
	}
	return nil, nil
}

func (e *Engine) validateImportedPromotion(root string, p workbench.Promotion) error {
	target := workbench.BundleTarget{ProjectRoot: root, ModelID: p.ModelID, CreatedAt: p.ModelCreatedAt}
	state, err := e.importedRunner().Status(target)
	if err != nil {
		return err
	}
	if !state.Ready {
		return errors.New(state.Reason)
	}
	fingerprint, window, err := workbench.ImportedChatIdentity(target, p.CheckpointID)
	if err != nil {
		return err
	}
	if p.PromptFormat != "imported-chat-v1" || p.SourceFingerprint != fingerprint || p.ContextWindow != window {
		return errors.New("Imported model changed. Test and promote the selected weights again.")
	}
	return nil
}
