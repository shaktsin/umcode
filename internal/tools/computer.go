package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shaktsin/umcode/internal/computeruse"
)

type computerList struct{ manager *computeruse.Manager }

func (*computerList) Name() string { return "computer.list" }
func (*computerList) Description() string {
	return "List visible desktop applications available to the opt-in Computer Use plugin. Use this before selecting an application by name or bundle ID."
}
func (*computerList) Schema() json.RawMessage { return schema(`{"type":"object","properties":{}}`) }
func (*computerList) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "List visible desktop applications"
}
func (t *computerList) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	if err := requireComputerUse(ctx); err != nil {
		return "", err
	}
	r, err := t.manager.List(ctx)
	return encodeComputerReport(r), err
}

type computerStart struct{ manager *computeruse.Manager }

func (*computerStart) Name() string { return "computer.start" }
func (*computerStart) Description() string {
	return "Open or select a macOS application for this task, optionally open a URL in that application, and return a full-resolution screenshot. The session persists across later computer.inspect and computer.act calls. The project may restrict this to an allowed-apps list; an unlisted app is refused."
}
func (*computerStart) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"app_name":{"type":"string"},"bundle_id":{"type":"string"},"app_path":{"type":"string"},"url":{"type":"string"}},"additionalProperties":false}`)
}
func (*computerStart) Assess(args json.RawMessage) (Risk, string) {
	var a struct {
		AppName  string `json:"app_name"`
		BundleID string `json:"bundle_id"`
		AppPath  string `json:"app_path"`
		URL      string `json:"url"`
	}
	_ = json.Unmarshal(args, &a)
	target := firstNonBlank(a.AppName, a.BundleID, a.AppPath, a.URL, "desktop application")
	return RiskYellow, "Open Computer Use session for " + target
}
func (t *computerStart) Call(ctx context.Context, args json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	var a struct {
		AppName  string `json:"app_name"`
		BundleID string `json:"bundle_id"`
		AppPath  string `json:"app_path"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if !computerUseAppAllowed(s, a.AppName, a.BundleID, a.AppPath) {
		return "", fmt.Errorf("%q is not on this project's Computer Use allowed-apps list; add it in project settings", firstNonBlank(a.AppName, a.BundleID, a.AppPath))
	}
	r, err := t.manager.Start(ctx, s.ThreadID, s.Root, computeruse.Target{Name: a.AppName, BundleID: a.BundleID, Path: a.AppPath, URL: a.URL})
	return encodeComputerReport(r), err
}

// computerUseAppAllowed checks a requested target against the project's
// Computer Use allowlist. An empty list means every app is allowed, matching
// the behavior before this list existed.
func computerUseAppAllowed(s *Scope, appName, bundleID, appPath string) bool {
	if len(s.ComputerUseApps) == 0 {
		return true
	}
	candidates := []string{appName, bundleID, appPath}
	if appPath != "" {
		candidates = append(candidates, filepath.Base(appPath))
	}
	for _, allowed := range s.ComputerUseApps {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		for _, c := range candidates {
			if c != "" && strings.EqualFold(strings.TrimSpace(c), allowed) {
				return true
			}
		}
	}
	return false
}

type computerInspect struct{ manager *computeruse.Manager }

func (*computerInspect) Name() string { return "computer.inspect" }
func (*computerInspect) Description() string {
	return "Capture the selected application's current window at original resolution and return its window metadata and a bounded accessibility summary. Inspect before acting and after short groups of actions."
}
func (*computerInspect) Schema() json.RawMessage { return schema(`{"type":"object","properties":{}}`) }
func (*computerInspect) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Inspect selected desktop application"
}
func (t *computerInspect) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	r, err := t.manager.Inspect(ctx, s.ThreadID)
	return encodeComputerReport(r), err
}

type computerAct struct{ manager *computeruse.Manager }

func (*computerAct) Name() string { return "computer.act" }
func (*computerAct) Description() string {
	return "Perform one user-like action in the selected desktop app, then return a fresh screenshot. Coordinates are relative to the latest screenshot. Supported actions: click, double_click, fill, type, key, scroll. Use fill for form fields."
}
func (*computerAct) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"action":{"type":"string","enum":["click","double_click","fill","type","key","scroll"]},"x":{"type":"number","minimum":0},"y":{"type":"number","minimum":0},"text":{"type":"string"},"key":{"type":"string"},"delta":{"type":"integer","minimum":-4000,"maximum":4000}},"required":["action"],"additionalProperties":false}`)
}
func (*computerAct) Assess(args json.RawMessage) (Risk, string) {
	var a struct {
		Action string `json:"action"`
		Text   string `json:"text"`
		Key    string `json:"key"`
	}
	_ = json.Unmarshal(args, &a)
	detail := a.Action
	if a.Action == "key" {
		detail += " " + a.Key
	}
	// Host UI mutations always require a review in normal mode. A click or
	// keystroke may submit data even when its visual target looked harmless.
	return RiskRed, "Computer Use " + strings.TrimSpace(detail)
}
func (t *computerAct) Call(ctx context.Context, args json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	var a computeruse.Action
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	r, err := t.manager.Act(ctx, s.ThreadID, a)
	return encodeComputerReport(r), err
}

type computerStop struct{ manager *computeruse.Manager }

func (*computerStop) Name() string { return "computer.stop" }
func (*computerStop) Description() string {
	return "Release this task's Computer Use session. It does not quit the user's application and keeps screenshot artifacts reviewable."
}
func (*computerStop) Schema() json.RawMessage { return schema(`{"type":"object","properties":{}}`) }
func (*computerStop) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Stop Computer Use session"
}
func (t *computerStop) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	t.manager.Stop(s.ThreadID)
	return `{"status":"stopped","framework":"umcode-computer-use","artifacts":[]}`, nil
}

func requireComputerUse(ctx context.Context) error {
	s := ScopeFrom(ctx)
	if s == nil || !s.AllowComputerUse {
		return errors.New("Computer Use is disabled for this project")
	}
	return nil
}

func requireComputerScope(ctx context.Context) (*Scope, error) {
	s := ScopeFrom(ctx)
	if s == nil || s.Root == "" || s.ThreadID == "" {
		return nil, ErrNoProject
	}
	if !s.AllowComputerUse {
		return nil, errors.New("Computer Use is disabled for this project")
	}
	return s, nil
}

func encodeComputerReport(r computeruse.Report) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
