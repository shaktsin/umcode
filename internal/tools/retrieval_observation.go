package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"github.com/shaktsin/umcode/internal/vault"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func IsFileObservationSource(t Tool) bool {
	switch t.(type) {
	case *fileRead, *fileSearch:
		return true
	}
	return false
}
func captureEnabled(ctx context.Context) bool {
	s, _ := ctx.Value(rawSinkKey{}).(*RawSink)
	return s != nil && s.CaptureExcerpts
}
func SetObservedExcerpts(ctx context.Context, xs []protocol.ObservedExcerpt) {
	s, _ := ctx.Value(rawSinkKey{}).(*RawSink)
	if s == nil || !s.CaptureExcerpts {
		return
	}
	for _, x := range xs {
		if len(s.Excerpts) >= retrieval.MaxFiles {
			return
		}
		s.Excerpts = append(s.Excerpts, x)
	}
}
func captureFileExcerpt(ctx context.Context, path string, body []byte, start, end int) {
	if !captureEnabled(ctx) || len(body) > retrieval.MaxFileBytes || bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		return
	}
	scope := ScopeFrom(ctx)
	if scope == nil || scope.Root == "" {
		return
	}
	rel, err := filepath.Rel(scope.Root, path)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if !retrieval.AllowedExcerptPath(rel) {
		return
	}
	lines := strings.Split(string(body), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if start < 1 || start > len(lines) {
		return
	}
	if end > len(lines) {
		end = len(lines)
	}
	if end > start+retrieval.MaxLines-1 {
		end = start + retrieval.MaxLines - 1
	}
	var text strings.Builder
	last := start - 1
	for i := start - 1; i < end; i++ {
		if text.Len()+len(lines[i])+1 > retrieval.MaxBodyBytes {
			break
		}
		text.WriteString(lines[i])
		text.WriteByte('\n')
		last = i + 1
	}
	if last < start {
		return
	}
	hash := sha256.Sum256(body)
	redacted, _ := vault.Redact([]byte(text.String()))
	SetObservedExcerpts(ctx, []protocol.ObservedExcerpt{{Path: rel, WorkspaceRootHash: retrieval.WorkspaceHash(scope.Root), ContentHash: hex.EncodeToString(hash[:]), StartLine: start, EndLine: last, Text: string(redacted)}})
}

type boundedCapture struct{ body []byte }

func (b *boundedCapture) Write(p []byte) (int, error) {
	n := len(p)
	left := retrieval.MaxFileBytes + 1 - len(b.body)
	if left > 0 {
		if left > n {
			left = n
		}
		b.body = append(b.body, p[:left]...)
	}
	return n, nil
}
