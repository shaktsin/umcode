package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/shaktsin/umcode/internal/retrieval"
	"github.com/shaktsin/umcode/internal/vault"
	"strings"
	"unicode/utf8"
)

var errRetrievalUnsupported = errors.New("retrieval file validation unsupported")

func validateRetrievalFiles(ctx context.Context, root string, cs []retrieval.Candidate) ([]retrieval.Candidate, error) {
	var out []retrieval.Candidate
	read := map[string][]byte{}
	attempts := 0
	total := 0
	for _, c := range cs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c.Kind != "excerpt" {
			out = append(out, c)
			continue
		}
		if c.WorkspaceRootHash != retrieval.WorkspaceHash(root) {
			continue
		}
		if !retrieval.AllowedExcerptPath(c.Path) || c.StartLine < 1 || c.EndLine < c.StartLine || c.EndLine-c.StartLine+1 > retrieval.MaxLines {
			return nil, errors.New("invalid retrieval containment metadata")
		}
		b, seen := read[c.Path]
		if !seen {
			if attempts >= retrieval.MaxFiles {
				continue
			}
			attempts++
			var err error
			b, err = readRetrievalFile(ctx, root, c.Path)
			if err != nil {
				if errors.Is(err, errRetrievalUnsupported) {
					continue
				}
				return nil, errors.New("retrieval file validation failed")
			}
			total += len(b)
			if total > retrieval.MaxReadBytes {
				continue
			}
			read[c.Path] = b
		}
		if b == nil || bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
			continue
		}
		hash := sha256.Sum256(b)
		if hex.EncodeToString(hash[:]) != c.ContentHash {
			continue
		}
		lines := strings.Split(string(b), "\n")
		if c.EndLine > len(lines) {
			continue
		}
		body := strings.Join(lines[c.StartLine-1:c.EndLine], "\n") + "\n"
		if len(body) > retrieval.MaxBodyBytes {
			continue
		}
		redacted, _ := vault.Redact([]byte(body))
		c.Body = string(redacted)
		out = append(out, c)
	}
	return out, nil
}
