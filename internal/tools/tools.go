// Package tools holds the tool registry and the built-in tools (files, shell),
// with workspace access control. Skills and MCP servers add tools through
// dynamic Sources.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Risk tiers.
type Risk string

const (
	RiskGreen  Risk = "green"
	RiskYellow Risk = "yellow"
	RiskRed    Risk = "red"
)

// ParseRisk converts a config value to a Risk (ok=false if unknown or empty).
func ParseRisk(s string) (Risk, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "green":
		return RiskGreen, true
	case "yellow":
		return RiskYellow, true
	case "red":
		return RiskRed, true
	}
	return "", false
}

// Tool is something the agent can call.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	// Assess returns the risk of a specific call and a one-line human summary.
	Assess(args json.RawMessage) (Risk, string)
	Call(ctx context.Context, args json.RawMessage) (string, error)
}

// WorkUpdater applies a semantic batch without coupling tools to its service.
type WorkUpdater interface {
	Update(context.Context, string, protocol.WorkUpdateRequest) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error)
}

// WorkflowUpdateTool returns approval metadata only to the engine. Call's JSON
// result contains the revision and counts, never gates or graph contents.
type WorkflowUpdateTool interface {
	Tool
	Apply(context.Context, json.RawMessage) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error)
}

// ContextAssessor lets tools whose metadata is scoped to a turn (for example
// plugin skills) assess risk from that turn's immutable capability snapshot.
type ContextAssessor interface {
	AssessContext(context.Context, json.RawMessage) (Risk, string)
}

// Guard is implemented by tools that can refuse a call outright, before any
// approval is requested (for example a destructive shell command).
type Guard interface {
	Forbidden(args json.RawMessage) (reason string, forbidden bool)
}

// Source contributes tools that can change at runtime (skills, MCP servers).
type Source interface {
	Tools() []Tool
}

// Registry maps tool names to tools.
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	sources []Source
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Add registers a static tool.
func (r *Registry) Add(t Tool) {
	r.mu.Lock()
	r.tools[t.Name()] = t
	r.mu.Unlock()
}

// AddSource registers a dynamic tool source.
func (r *Registry) AddSource(s Source) {
	r.mu.Lock()
	r.sources = append(r.sources, s)
	r.mu.Unlock()
}

// All returns every tool (static first on name clash), sorted by name.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	byName := make(map[string]Tool, len(r.tools))
	for n, t := range r.tools {
		byName[n] = t
	}
	sources := append([]Source(nil), r.sources...)
	r.mu.RUnlock()
	for _, s := range sources {
		for _, t := range s.Tools() {
			if _, exists := byName[t.Name()]; !exists {
				byName[t.Name()] = t
			}
		}
	}
	out := make([]Tool, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Get finds a tool by its canonical name (e.g. "shell.run") or wire name ("shell__run").
func (r *Registry) Get(name string) (Tool, bool) {
	for _, t := range r.All() {
		if t.Name() == name || ToWire(t.Name()) == name {
			return t, true
		}
	}
	return nil, false
}

// ToWire converts a tool name into one every provider accepts
// (^[a-zA-Z0-9_-]{1,64}$): dots become "__", other characters "_", and long
// names are shortened with a hash suffix.
func ToWire(name string) string {
	var b strings.Builder
	for _, r := range strings.ReplaceAll(name, ".", "__") {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	w := b.String()
	if len(w) > 64 {
		sum := sha256.Sum256([]byte(name))
		w = w[:55] + "_" + hex.EncodeToString(sum[:4])
	}
	return w
}

// FromWire reverses ToWire for simple names ("shell__run" → "shell.run").
// Registry.Get should be preferred: it matches any wire name exactly.
func FromWire(name string) string { return strings.ReplaceAll(name, "__", ".") }

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return v, fmt.Errorf("invalid arguments: %w", err)
	}
	return v, nil
}

// SelectionMetadata is trusted registration provenance, not name parsing.
// It is used only to group advertisements; it grants no execution permission.
type SelectionMetadata interface {
	SelectionMetadata() (family, origin string)
}
