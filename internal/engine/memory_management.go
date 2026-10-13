package engine

import (
	"context"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func (e *Engine) ListProjectMemory(ctx context.Context, p protocol.MemoryListParams) (protocol.MemoryListResult, error) {
	if _, err := e.project(ctx, p.ProjectID); err != nil {
		return protocol.MemoryListResult{}, err
	}
	if e.Memory == nil {
		return protocol.MemoryListResult{}, protocol.Errorf(protocol.CodeInternal, "memory service unavailable")
	}
	return e.Memory.List(ctx, p)
}
func (e *Engine) CorrectProjectMemory(ctx context.Context, p protocol.MemoryChangeParams) (protocol.MemoryChangeResult, error) {
	if _, err := e.project(ctx, p.ProjectID); err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	if e.Memory == nil {
		return protocol.MemoryChangeResult{}, protocol.Errorf(protocol.CodeInternal, "memory service unavailable")
	}
	r, err := e.Memory.Correct(ctx, p)
	if err == store.ErrMemoryStale {
		return r, protocol.Errorf(protocol.CodeConflict, "memory changed; refresh before editing")
	}
	return r, err
}
func (e *Engine) RetireProjectMemory(ctx context.Context, p protocol.MemoryChangeParams) (protocol.MemoryChangeResult, error) {
	if _, err := e.project(ctx, p.ProjectID); err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	if e.Memory == nil {
		return protocol.MemoryChangeResult{}, protocol.Errorf(protocol.CodeInternal, "memory service unavailable")
	}
	r, err := e.Memory.Retire(ctx, p)
	if err == store.ErrMemoryStale {
		return r, protocol.Errorf(protocol.CodeConflict, "memory changed; refresh before retiring")
	}
	return r, err
}
