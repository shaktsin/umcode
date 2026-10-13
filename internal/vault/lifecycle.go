package vault

import "sync"

var lifecycle sync.Mutex

// LockLifecycle serializes vault object deletion with operations that create or
// remove durable references. Callers acquire any per-memory target lock first,
// then this lock, and both before opening a database transaction.
func LockLifecycle() func() { lifecycle.Lock(); return lifecycle.Unlock }
