package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/shaktsin/umcode/internal/protocol"
)

type rawSinkKey struct{}

// RawSink receives a tool's unclipped result so the engine can record it
// without depending on the clipped string the model sees.
type RawSink struct {
	OutcomeUnknown  bool
	FileRead        *FileReadEvidence
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

// FileReadEvidence binds a successful built-in read to its full bytes without
// placing the unclipped body in metadata or granting additional file access.
type FileReadEvidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Body   string `json:"-"`
}

func captureFileReadEvidence(ctx context.Context, path string, body []byte) {
	if sink, _ := ctx.Value(rawSinkKey{}).(*RawSink); sink != nil {
		hash := sha256.Sum256(body)
		sink.FileRead = &FileReadEvidence{Path: path, SHA256: hex.EncodeToString(hash[:]), Bytes: len(body), Body: string(body)}
	}
}

// MarkOutcomeUnknown records interruption of a first-party effectful invocation.
func MarkOutcomeUnknown(ctx context.Context) {
	if sink, _ := ctx.Value(rawSinkKey{}).(*RawSink); sink != nil {
		sink.OutcomeUnknown = true
	}
}
