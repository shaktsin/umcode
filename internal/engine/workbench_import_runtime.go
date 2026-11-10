package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func (e *Engine) importedRunner() *workbench.ImportedRuntime {
	e.inferenceMu.Lock()
	defer e.inferenceMu.Unlock()
	if e.importedRuntime == nil {
		e.importedRuntime = workbench.NewImportedRuntime(e.Cfg.Home)
	}
	return e.importedRuntime
}
func (e *Engine) WorkbenchImportRuntime(ctx context.Context, p protocol.ImportRuntimeParams) (any, error) {
	if p.ModelCreatedAt == "" {
		return nil, errors.New("Refresh the model before using its runtime.")
	}
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	if _, err = matchingWorkbenchModel(project.Root, p.ModelID, p.ModelCreatedAt); err != nil {
		return nil, err
	}
	opened, err := workbench.Open(project.Root, p.ModelID)
	if err != nil {
		return nil, err
	}
	if !workbench.IsImportedGraph(opened.Graph) {
		return nil, errors.New("Use the regular runtime for a model built from scratch.")
	}
	target := workbench.BundleTarget{ProjectRoot: project.Root, ModelID: p.ModelID, CreatedAt: p.ModelCreatedAt}
	runner := e.importedRunner()
	switch p.Action {
	case "begin":
		id, err := target.Begin(p.Files)
		return map[string]string{"uploadId": id}, err
	case "chunk":
		err = target.Chunk(p.UploadID, p.Name, p.Offset, p.ContentBase64)
	case "abort":
		err = target.Abort(p.UploadID)
	case "finish":
		err = target.Finish(p.UploadID)
	case "status":
		return runner.Status(target)
	case "validate":
		return runner.Validate(ctx, target)
	case "infer":
		return runner.Infer(ctx, target, p.Prompt, p.ImageBase64, p.Checkpoint, p.MaxTokens)
	case "train":
		return runner.Train(ctx, target, p.Epochs, p.LearningRate)
	case "job":
		return runner.Job(target)
	case "cancel":
		err = runner.Cancel(target)
	default:
		return nil, errors.New("Unsupported imported model action.")
	}
	return map[string]bool{"ok": err == nil}, err
}
