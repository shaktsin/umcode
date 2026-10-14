package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"sync"
	"time"
)

type turnRecorderKey struct{}
type turnRecorder struct {
	mu      sync.Mutex
	engine  *Engine
	turnID  string
	data    protocol.TurnDiagnostics
	pending protocol.RequestDiagnostics
	writers int
	failed  bool
}

func withTurnRecorder(ctx context.Context, r *turnRecorder) context.Context {
	return context.WithValue(ctx, turnRecorderKey{}, r)
}
func recorderFrom(ctx context.Context) *turnRecorder {
	r, _ := ctx.Value(turnRecorderKey{}).(*turnRecorder)
	return r
}
func newTurnRecorder(e *Engine, id string, p optimization.Policy, source string) *turnRecorder {
	r := &turnRecorder{engine: e, turnID: id, writers: 1, data: protocol.TurnDiagnostics{Version: 1, Policy: protocol.OptimizationSnapshot{Source: source, ContextCompiler: p.ContextCompiler, ContextRetrieval: p.ContextRetrieval, ProgressiveTools: p.ProgressiveTools, ToolResultReducers: p.ToolResultReducers, DesignedWorkflow: p.DesignedWorkflow, AutoPromote: p.AutoPromote, AutomaticWorkflow: p.AutomaticWorkflow}}}
	r.mu.Lock()
	r.saveLocked()
	r.mu.Unlock()
	return r
}
func (r *turnRecorder) saveLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.engine.Store.SaveTurnDiagnostics(ctx, r.turnID, r.data); err != nil {
		r.failed = true
		r.data.Complete = false
		r.engine.Log.Warn("turn diagnostics unavailable", "turn", r.turnID, "err", err)
	}
}
func (r *turnRecorder) prepare(d protocol.RequestDiagnostics) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.pending = d
	r.mu.Unlock()
}
func (r *turnRecorder) template() protocol.RequestDiagnostics {
	if r == nil {
		return protocol.RequestDiagnostics{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending
}
func (r *turnRecorder) recordRequest(d protocol.RequestDiagnostics) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d.Ordinal = len(r.data.Requests) + r.data.OmittedRequests + 1
	if len(r.data.Requests) < 128 {
		r.data.Requests = append(r.data.Requests, d)
	} else {
		r.data.OmittedRequests++
	}
	r.saveLocked()
}
func (r *turnRecorder) retain() {
	if r != nil {
		r.mu.Lock()
		r.writers++
		r.data.Complete = false
		r.mu.Unlock()
	}
}
func (r *turnRecorder) finish() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writers--
	r.data.Complete = r.writers == 0 && !r.failed && r.data.OmittedRequests == 0
	r.saveLocked()
}
func turnDetailsCost(requests []protocol.RequestDiagnostics) (*float64, string) {
	var sum float64
	known, unknown := 0, 0
	for _, r := range requests {
		if r.CostUSD != nil && r.CostState == "known" {
			known++
			sum += *r.CostUSD
		} else {
			unknown++
		}
	}
	if known == 0 {
		return nil, "unavailable"
	}
	if unknown > 0 {
		return &sum, "partial"
	}
	return &sum, "known"
}
func (e *Engine) TurnDetails(ctx context.Context, p protocol.TurnDetailsParams) (protocol.TurnDetailsResult, error) {
	if p.ThreadID == "" || p.TurnID == "" {
		return protocol.TurnDetailsResult{}, protocol.Errorf(protocol.CodeInvalidParams, "threadId and turnId are required")
	}
	out, err := e.Store.ReadTurnDetails(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return out, protocol.Errorf(protocol.CodeNotFound, "turn not found in this chat")
	}
	return out, err
}
