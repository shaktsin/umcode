package tools

import (
	"context"
	"github.com/shaktsin/umcode/internal/protocol"
)

type rawSinkKey struct{}

// RawSink receives a tool's unclipped result so the engine can record it
// without depending on the clipped string the model sees.
type RawSink struct {
	Text            string
	CaptureExcerpts bool
	Excerpts        []protocol.ObservedExcerpt
}

// WithRawSink returns a context carrying a fresh sink.
func WithRawSink(ctx context.Context) (context.Context, *RawSink) {
	s := &RawSink{}
	return context.WithValue(ctx, rawSinkKey{}, s), s
}

// SetRaw stores text in the context's sink; it does nothing without one.
func SetRaw(ctx context.Context, text string) {
	if s, _ := ctx.Value(rawSinkKey{}).(*RawSink); s != nil {
		s.Text = text
	}
}
