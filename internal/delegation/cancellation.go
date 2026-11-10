package delegation

import (
	"context"
	"errors"
	"sync"
)

// childCancellation owns the engine turns an agent execution starts: cancelling
// it cancels every registered turn (see RegisterChildCancellation).
type cancellationKey struct{}
type childCancellation struct {
	mu        sync.Mutex
	cancelled bool
	handles   []context.CancelFunc
}

func (h *childCancellation) cancel() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelled = true
	for _, f := range h.handles {
		f()
	}
}
func RegisterChildCancellation(ctx context.Context, cancel context.CancelFunc) error {
	h, _ := ctx.Value(cancellationKey{}).(*childCancellation)
	if h == nil {
		return errors.New("missing child cancellation owner")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		cancel()
		return context.Canceled
	}
	h.handles = append(h.handles, cancel)
	return nil
}

// WithChildCancellation gives ctx a cancellation owner for the engine turns an
// execution starts (see RegisterChildCancellation): each registered turn is
// cancelled when ctx is done. The agent runtime's runner gives every agent
// execution one. stop releases the watch on ctx.
func WithChildCancellation(ctx context.Context) (context.Context, func()) {
	h := &childCancellation{}
	ctx = context.WithValue(ctx, cancellationKey{}, h)
	stop := context.AfterFunc(ctx, h.cancel)
	return ctx, func() { stop() }
}

// InheritRunnerContext preserves the actual-turn cancellation owner when the
// engine reconstructs its execution context. It grants no parent delegation
// authority.
func InheritRunnerContext(ctx, from context.Context) context.Context {
	if value := from.Value(cancellationKey{}); value != nil {
		ctx = context.WithValue(ctx, cancellationKey{}, value)
	}
	return ctx
}
