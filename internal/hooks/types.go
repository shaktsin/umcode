// Package hooks defines UMCode's normalized plugin hook contract.
package hooks

import "time"

// Event is a runtime lifecycle event understood by UMCode.
type Event string

const (
	TurnStart        Event = "TurnStart"
	BeforeToolUse    Event = "BeforeToolUse"
	AfterToolUse     Event = "AfterToolUse"
	ToolUseFailed    Event = "ToolUseFailed"
	TurnComplete     Event = "TurnComplete"
	BeforeCompaction Event = "BeforeCompaction"
	AfterCompaction  Event = "AfterCompaction"
)

const (
	EnvelopeVersion = 1
	MaxEventField   = 64 << 10
	MaxBlockReason  = 4 << 10
	MaxWarning      = 8 << 10
	MaxContext      = 32 << 10
	MaxStdout       = 256 << 10
	MaxStderr       = 64 << 10
	DefaultTimeout  = 10 * time.Second
	MaximumTimeout  = 60 * time.Second
)

// Declaration is one normalized command hook contributed by a plugin.
type Declaration struct {
	PluginID      string            `json:"pluginId"`
	PluginVersion string            `json:"pluginVersion,omitempty"`
	Root          string            `json:"root"`
	Source        string            `json:"source"`
	Event         Event             `json:"event"`
	Matcher       string            `json:"matcher,omitempty"`
	Command       string            `json:"command"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	// SecretValues are available to the process through explicitly declared
	// Env entries but are redacted from outcomes and audit records.
	SecretValues []string      `json:"-"`
	Required     bool          `json:"required"`
	Timeout      time.Duration `json:"timeout"`
	Order        int           `json:"order"`
}

// CanBlock reports whether the event is allowed to prevent pending work.
func (e Event) CanBlock() bool { return e == TurnStart || e == BeforeToolUse }
