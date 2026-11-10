// Package compute provides isolated command execution for project coding work.
// The desktop app routes this contract to its private, bundled microVM bridge.
package compute

import (
	"context"
	"io"
)

type Request struct {
	Root    string
	Dir     string
	Command string
	Network bool
	// RootReadOnly shares the project root with the guest read-only; the
	// guest's own /tmp scratch stays writable.
	RootReadOnly   bool
	Timeout        int
	VCPUs          int
	MemoryMiB      int
	DiskLimitBytes int64
	Stdout         io.Writer
	Stderr         io.Writer
}

// PreviewProcess is a long-running command inside a microVM with one guest
// TCP port mapped to loopback on the host. The caller owns its lifetime.
type PreviewProcess interface {
	HostPort() int
	Wait() error
	Stop()
}

// PreviewRunner extends command compute with managed long-running previews.
type PreviewRunner interface {
	Runner
	StartPreview(context.Context, Request, int) (PreviewProcess, error)
}

type Runner interface {
	Run(context.Context, Request) (int, error)
}
