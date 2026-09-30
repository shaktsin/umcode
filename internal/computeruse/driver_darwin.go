//go:build darwin

package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/config"
)

type desktopDriver struct {
	socket string
	client *http.Client
}

func newDriver() driver {
	home, err := config.HomeDir()
	if err != nil {
		return &desktopDriver{}
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(home, "computer-use.sock"))
	}}
	return &desktopDriver{socket: filepath.Join(home, "computer-use.sock"), client: &http.Client{Transport: transport, Timeout: 60 * time.Second}}
}

func (d *desktopDriver) invoke(ctx context.Context, command string, input any, output any) error {
	if d.socket == "" {
		return errors.New("Computer Use is available only while the UMCode desktop app is running")
	}
	b, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://umcode/v1/"+command, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("Computer Use requires the UMCode desktop app to be open: %w", err)
	}
	defer resp.Body.Close()
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("UMCode Computer Use returned invalid output: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		if failure.Error == "" {
			return fmt.Errorf("UMCode Computer Use failed with HTTP %d", resp.StatusCode)
		}
		return errors.New(failure.Error)
	}
	if output != nil && json.Unmarshal(body, output) != nil {
		return fmt.Errorf("UMCode Computer Use returned invalid output: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func (d *desktopDriver) List(ctx context.Context) ([]App, error) {
	var out struct {
		Apps []App `json:"apps"`
	}
	err := d.invoke(ctx, "list", map[string]any{}, &out)
	return out.Apps, err
}

func (d *desktopDriver) Open(ctx context.Context, target Target) (Target, error) {
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

func (d *desktopDriver) Inspect(ctx context.Context, target Target, screenshot string) (State, error) {
	var state State
	err := d.invoke(ctx, "inspect", map[string]any{"target": target, "screenshot": screenshot}, &state)
	return state, err
}

func (d *desktopDriver) Act(ctx context.Context, target Target, action Action) error {
	return d.invoke(ctx, "act", map[string]any{"target": target, "action": action}, nil)
}
