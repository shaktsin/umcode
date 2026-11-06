package engine

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/workbench"
)

const workbenchHFSecretKey = "workbench/huggingface/read-token"

func (e *Engine) workbenchDataRoot(ctx context.Context, projectID, modelID string) (string, error) {
	project, err := e.project(ctx, projectID)
	if err != nil {
		return "", err
	}
	opened, err := workbench.Open(project.Root, modelID)
	if err != nil {
		return "", protocol.Errorf(protocol.CodeInvalidParams, "model is unavailable: %v", err)
	}
	if workbench.IsImportedGraph(opened.Graph) {
		state, err := e.importedRunner().Status(workbench.BundleTarget{ProjectRoot: project.Root, ModelID: modelID, CreatedAt: opened.Model.CreatedAt.Format(time.RFC3339Nano)})
		if err != nil {
			return "", err
		}
		if !state.Ready {
			return "", errors.New("Data is gated: " + state.Reason)
		}
	}
	return project.Root, nil
}

func (e *Engine) GetWorkbenchData(ctx context.Context, p protocol.WorkbenchDataGetParams) (any, error) {
	root, err := e.workbenchDataRoot(ctx, p.ProjectID, p.ModelID)
	if err != nil {
		return nil, err
	}
	state, err := workbench.ReadData(root, p.ModelID, p.DatasetID)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "read dataset: %v", err)
	}
	return state, nil
}

func (e *Engine) workbenchHFToken() (string, error) {
	if e.Secrets == nil {
		return "", nil
	}
	token, err := e.Secrets.Get(workbenchHFSecretKey)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("could not read the Hugging Face connection from secure storage")
	}
	return token, nil
}

func (e *Engine) ImportWorkbenchData(ctx context.Context, p protocol.WorkbenchDataImportParams) (any, error) {
	root, err := e.workbenchDataRoot(ctx, p.ProjectID, p.ModelID)
	if err != nil {
		return nil, err
	}
	e.workbenchMu.Lock()
	initial, err := matchingWorkbenchModel(root, p.ModelID, p.ModelCreatedAt)
	e.workbenchMu.Unlock()
	if err != nil {
		return nil, err
	}
	if p.Source.Kind != "local" {
		if len(p.Files) != 0 {
			return nil, protocol.Errorf(protocol.CodeInvalidParams, "Hugging Face imports select repository files instead of uploading bytes")
		}
		token, err := e.workbenchHFToken()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		p.Source, p.Files, err = workbench.NewHuggingFace().Download(ctx, p.Source, token)
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidParams, "download dataset: %v", err)
		}
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	if _, err := matchingWorkbenchModel(root, p.ModelID, initial.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	dataset, err := workbench.ImportData(root, p.ModelID, p.Source, p.Files)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "import dataset: %v", err)
	}
	_ = e.Store.Audit(ctx, "workbench.data.import", map[string]any{"projectId": p.ProjectID, "modelId": p.ModelID, "datasetId": dataset.ID, "source": dataset.Source.Kind, "rows": dataset.RowCount, "bytes": dataset.ByteCount})
	return dataset, nil
}

func (e *Engine) PrepareWorkbenchData(ctx context.Context, p protocol.WorkbenchDataPrepareParams) (any, error) {
	root, err := e.workbenchDataRoot(ctx, p.ProjectID, p.ModelID)
	if err != nil {
		return nil, err
	}
	e.workbenchMu.Lock()
	defer e.workbenchMu.Unlock()
	if _, err := matchingWorkbenchModel(root, p.ModelID, p.ModelCreatedAt); err != nil {
		return nil, err
	}
	opened, err := workbench.Open(root, p.ModelID)
	if err != nil {
		return nil, err
	}
	var selection workbench.DataSelection
	if workbench.IsImportedGraph(opened.Graph) {
		selection, err = workbench.PrepareImportedData(root, p.ModelID, p.Config)
	} else {
		selection, err = workbench.PrepareData(root, p.ModelID, p.Config)
	}
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "prepare dataset: %v", err)
	}
	_ = e.Store.Audit(ctx, "workbench.data.prepare", map[string]any{"projectId": p.ProjectID, "modelId": p.ModelID, "datasetId": selection.DatasetID, "version": selection.Version})
	return selection, nil
}

func (e *Engine) WorkbenchHuggingFace(ctx context.Context, p protocol.WorkbenchDataHuggingFaceParams) (any, error) {
	if _, err := e.workbenchDataRoot(ctx, p.ProjectID, p.ModelID); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	hub := workbench.NewHuggingFace()
	type status struct {
		Connected bool   `json:"connected"`
		Username  string `json:"username"`
	}
	switch p.Action {
	case "connect":
		if e.Secrets == nil {
			return nil, errors.New("secure credential storage is unavailable")
		}
		token := strings.TrimSpace(p.Secret)
		username, err := hub.Whoami(ctx, token)
		if err != nil {
			return nil, err
		}
		if err := e.Secrets.Set(workbenchHFSecretKey, token); err != nil {
			return nil, errors.New("could not save the Hugging Face token in secure storage")
		}
		return status{true, username}, nil
	case "disconnect":
		if e.Secrets != nil {
			if err := e.Secrets.Delete(workbenchHFSecretKey); err != nil && !errors.Is(err, secrets.ErrNotFound) {
				return nil, errors.New("could not disconnect Hugging Face from secure storage")
			}
		}
		return status{false, ""}, nil
	case "status", "search", "inspect":
		token, err := e.workbenchHFToken()
		if err != nil {
			return nil, err
		}
		if p.Action == "status" {
			if token == "" {
				return status{false, ""}, nil
			}
			name, err := hub.Whoami(ctx, token)
			if err != nil {
				return nil, err
			}
			return status{true, name}, nil
		}
		if p.Action == "inspect" {
			return hub.Inspect(ctx, p.Repo, p.Revision, token)
		}
		items, err := hub.Search(ctx, p.Query, token)
		if err != nil {
			return nil, err
		}
		return struct {
			Datasets []workbench.HFDataset `json:"datasets"`
		}{items}, nil
	default:
		return nil, protocol.Errorf(protocol.CodeInvalidParams, "unknown Hugging Face action")
	}
}
