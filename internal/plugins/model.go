// Package plugins owns plugin package adaptation, validation, installation,
// and project capability composition.
package plugins

import (
	"fmt"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/hooks"
)

type Format string

const (
	FormatAgent  Format = "agent"
	FormatCodex  Format = "codex"
	FormatClaude Format = "claude"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Diagnostic struct {
	Code        string   `json:"code"`
	Phase       string   `json:"phase"`
	Severity    Severity `json:"severity"`
	Component   string   `json:"component,omitempty"`
	Message     string   `json:"message"`
	Remediation string   `json:"remediation,omitempty"`
}

type Error struct {
	Diagnostic Diagnostic
	Cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Diagnostic.Message != "" {
		return e.Diagnostic.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return fmt.Sprintf("plugin error %s", e.Diagnostic.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type SkillComponent struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Required bool   `json:"required"`
}

type MCPComponent struct {
	Name     string                 `json:"name"`
	Path     string                 `json:"path,omitempty"`
	Config   config.MCPServerConfig `json:"config"`
	Required bool                   `json:"required"`
}

type Component struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Required  bool   `json:"required"`
	Supported bool   `json:"supported"`
}

type Setting struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
}

type Package struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Version     string              `json:"version,omitempty"`
	Format      Format              `json:"format"`
	Root        string              `json:"root"`
	Skills      []SkillComponent    `json:"skills"`
	MCPServers  []MCPComponent      `json:"mcpServers"`
	Hooks       []hooks.Declaration `json:"hooks"`
	Components  []Component         `json:"components"`
	Settings    []Setting           `json:"settings"`
	Diagnostics []Diagnostic        `json:"diagnostics"`
}

type Adapter interface {
	Format() Format
	Detect(root string) (bool, error)
	Load(root string) (Package, error)
}
