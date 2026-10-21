package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"time"
)

type compactionProgress struct {
	engine *Engine
	state  protocol.CompactionState
}
type compactionProgressKey struct{}

func (e *Engine) beginCompaction(ctx context.Context, turn protocol.Turn) (*compactionProgress, error) {
	now := time.Now().UTC()
	p := &compactionProgress{engine: e, state: protocol.CompactionState{ID: store.NewID("cmp"), ThreadID: turn.ThreadID, TurnID: turn.ID, Sequence: 1, Status: "preparing", StartedAt: now, UpdatedAt: now}}
	if err := p.save(ctx); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *compactionProgress) save(ctx context.Context) error {
	if err := p.engine.Store.SaveCompactionState(ctx, p.state); err != nil {
		return err
	}
	p.engine.Bus.Publish(p.state.ThreadID, protocol.NotifyCompactionUpdated, p.state)
	return nil
}
func (p *compactionProgress) phase(ctx context.Context, status string) error {
	if p == nil {
		return nil
	}
	p.state.Sequence++
	p.state.Status = status
	p.state.UpdatedAt = time.Now().UTC()
	return p.save(ctx)
}
func (p *compactionProgress) finish(ctx context.Context, cause error) error {
	if p == nil {
		return nil
	}
	status := "completed"
	if cause != nil {
		status = "failed"
		p.state.Error = "Automatic compaction failed; the original sources remain saved."
	}
	if errors.Is(cause, context.Canceled) {
		status = "cancelled"
		p.state.Error = "Automatic compaction was cancelled."
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		status = "interrupted"
		p.state.Error = "Automatic compaction timed out."
	}
	return p.phase(ctx, status)
}
func compactionPhase(ctx context.Context, status string) error {
	p, _ := ctx.Value(compactionProgressKey{}).(*compactionProgress)
	if p == nil {
		return nil
	}
	return p.phase(ctx, status)
}
