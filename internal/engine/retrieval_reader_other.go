//go:build !darwin && !linux

package engine

import (
	"context"
	"errors"
)

func readRetrievalFile(ctx context.Context, root, rel string) ([]byte, error) {
	return nil, errors.New("unsupported retrieval platform")
}
