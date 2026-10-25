package store

import (
	"errors"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestErrNotFoundIsProtocolErrNotFound(t *testing.T) {
	if !errors.Is(ErrNotFound, protocol.ErrNotFound) || ErrNotFound.Error() != "not found" {
		t.Fatalf("store.ErrNotFound %v is not protocol.ErrNotFound", ErrNotFound)
	}
}
