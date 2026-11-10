package protocol

import "errors"

// ErrNotFound is the sentinel for a missing record. Persistence layers
// return it (store.ErrNotFound is the same value) so consumers can test for
// absence with errors.Is without importing the store.
var ErrNotFound = errors.New("not found")
