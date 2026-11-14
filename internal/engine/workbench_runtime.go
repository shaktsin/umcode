package engine

import (
	"context"
	"errors"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

func (e *Engine) runtimeSetup() *workbench.RuntimeService {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	if e.runtimeService == nil {
		e.runtimeService = workbench.NewRuntimeService(e.Cfg.Home)
	}
	return e.runtimeService
}
func (e *Engine) applyRuntime(python string) error {
	e.trainingMu.Lock()
	defer e.trainingMu.Unlock()
	return e.applyRuntimeLocked(python)
}
func (e *Engine) applyRuntimeLocked(python string) error {
	if e.trainingManager != nil {
		if err := e.trainingManager.SetPython(python); err != nil {
			return err
		}
	}
	e.inferenceMu.Lock()
	defer e.inferenceMu.Unlock()
	if e.inferenceManager != nil {
		e.inferenceManager.SetPython(python)
	}
	return nil
}
func (e *Engine) WorkbenchRuntime(ctx context.Context, op string, p protocol.WorkbenchRuntimeParams) (any, error) {
	project, err := e.project(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	svc := e.runtimeSetup()
	switch op {
	case "get":
		state := svc.State(ctx, project.Root)
		python := ""
		if state.Ready {
			python = state.Selected
		}
		if err = e.applyRuntime(python); err != nil {
			return nil, err
		}
		return state, nil
	case "select":
		e.trainingMu.Lock()
		defer e.trainingMu.Unlock()
		busy := e.trainingManager != nil && e.trainingManager.Busy()
		if busy {
			return nil, errors.New("stop the active training run before changing Python")
		}
		state, err := svc.Select(ctx, p.Path)
		if err != nil {
			return nil, err
		}
		python := ""
		if state.Ready {
			python = state.Selected
		}
		if err = e.applyRuntimeLocked(python); err != nil {
			return nil, err
		}
		return svc.State(ctx, project.Root), nil
	case "install":
		e.trainingMu.Lock()
		busy := e.trainingManager != nil && e.trainingManager.Busy()
		e.trainingMu.Unlock()
		if busy {
			return nil, errors.New("stop the active training run before installing a new runtime")
		}
		if err = svc.Install(ctx, p.Path); err != nil {
			return nil, err
		}
		return svc.State(ctx, project.Root), nil
	default:
		return nil, errors.New("unknown runtime operation")
	}
}
