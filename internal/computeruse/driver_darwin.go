//go:build darwin

package computeruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type helperDriver struct{ path string }

func newDriver() driver { return &helperDriver{path: computerHelperPath()} }

func computerHelperPath() string {
	if p := os.Getenv("UMCODE_COMPUTER_HELPER_PATH"); p != "" {
		return p
	}
	exe, _ := os.Executable()
	resources := filepath.Dir(exe)
	if filepath.Base(resources) == "MacOS" {
		resources = filepath.Join(filepath.Dir(resources), "Resources")
	}
	return filepath.Join(resources, "computer", "UMCode Computer Use.app", "Contents", "MacOS", "UMCode Computer Use")
}

func (d *helperDriver) invoke(ctx context.Context, command string, input any, output any) error {
	if st, err := os.Stat(d.path); err != nil || st.IsDir() {
		return errors.New("the Computer Use plugin is enabled, but its signed macOS helper is not installed")
	}
	b, _ := json.Marshal(input)
	cmd := exec.CommandContext(ctx, d.path, command)
	cmd.Stdin = bytes.NewReader(b)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("Computer Use helper: %s", msg)
	}
	if output != nil && json.Unmarshal(stdout.Bytes(), output) != nil {
		return fmt.Errorf("Computer Use helper returned invalid output: %s", strings.TrimSpace(stdout.String()))
	}
	return nil
}

func (d *helperDriver) List(ctx context.Context) ([]App, error) {
	var out struct {
		Apps []App `json:"apps"`
	}
	err := d.invoke(ctx, "list", map[string]any{}, &out)
	return out.Apps, err
}

func (d *helperDriver) Open(ctx context.Context, target Target) (Target, error) {
	args := []string{}
	if target.BundleID != "" {
		args = append(args, "-b", target.BundleID)
	} else if target.Path != "" {
		args = append(args, "-a", target.Path)
	} else if target.Name != "" {
		args = append(args, "-a", target.Name)
	} else if target.URL == "" {
		return Target{}, errors.New("computer.start requires app_name, bundle_id, app_path, or url")
	}
	if target.URL != "" && target.BundleID == "" && target.Name == "" && target.Path == "" {
		return Target{}, errors.New("opening a URL requires app_name, bundle_id, or app_path so Computer Use cannot attach to the wrong browser")
	}
	if target.URL != "" {
		args = append(args, target.URL)
	}
	if err := exec.CommandContext(ctx, "/usr/bin/open", args...).Run(); err != nil {
		return Target{}, fmt.Errorf("open application: %w", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		apps, err := d.List(ctx)
		if err == nil {
			for _, app := range apps {
				if (target.BundleID != "" && app.BundleID == target.BundleID) ||
					(target.Name != "" && strings.EqualFold(app.Name, target.Name)) ||
					(target.Path != "" && app.Path == target.Path) {
					target.PID, target.Name, target.BundleID, target.Path = app.PID, app.Name, app.BundleID, app.Path
					return target, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return Target{}, errors.New("application did not become available for Computer Use")
}

func (d *helperDriver) Inspect(ctx context.Context, target Target, screenshot string) (State, error) {
	var state State
	err := d.invoke(ctx, "inspect", map[string]any{"target": target, "screenshot": screenshot}, &state)
	return state, err
}

func (d *helperDriver) Act(ctx context.Context, target Target, action Action) error {
	return d.invoke(ctx, "act", map[string]any{"target": target, "action": action}, nil)
}

// Hide and Show use System Events (a separate Automation permission UMCode
// itself must be granted, distinct from the helper's own Accessibility and
// Screen Recording permissions) to tuck the automated app away between
// actions, or bring it back so a click lands on the right window. This is
// best-effort: without the Automation permission it simply fails and the
// app behaves as it always did (visible the whole time Computer Use runs).
func (d *helperDriver) Hide(ctx context.Context, target Target) error {
	return setVisible(ctx, target, false)
}

func (d *helperDriver) Show(ctx context.Context, target Target) error {
	return setVisible(ctx, target, true)
}

func setVisible(ctx context.Context, target Target, visible bool) error {
	if target.PID == 0 {
		return errors.New("computer use: no pid to target for visibility")
	}
	script := fmt.Sprintf(`tell application "System Events"
	set visible of (first process whose unix id is %d) to %t`, target.PID, visible)
	if visible {
		script += fmt.Sprintf(`
	set frontmost of (first process whose unix id is %d) to true`, target.PID)
	}
	script += `
end tell`
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("computer use visibility: %s", msg)
	}
	return nil
}
