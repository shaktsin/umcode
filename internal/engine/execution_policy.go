package engine

import (
	"context"
	"errors"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// The policy versions execution bookkeeping, not the meaning of user requests.
const modelDrivenExecutionPolicy = protocol.ModelDrivenExecutionPolicy

func (e *Engine) adoptExecutionPolicy(ctx context.Context, threadID string) error {
	snap, err := e.Store.ActiveExecutionTask(ctx, threadID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = e.Store.AdoptModelDrivenTask(ctx, snap.Task.ID)
	return err
}
