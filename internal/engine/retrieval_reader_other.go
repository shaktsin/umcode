//go:build !darwin && !linux

package engine

import (
	"context"
)

func readRetrievalFile(ctx context.Context, root, rel string) ([]byte, error) {
	return nil, errRetrievalUnsupported
}
