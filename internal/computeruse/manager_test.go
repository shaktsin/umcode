package computeruse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeDriver struct {
	actions []Action
	opens   int
}

func (f *fakeDriver) List(context.Context) ([]App, error) {
	return []App{{Name: "Fixture", BundleID: "com.example.fixture", PID: 42}}, nil
}
func (f *fakeDriver) Open(_ context.Context, target Target) (Target, error) {
	f.opens++
	target.Name, target.BundleID, target.PID = "Fixture", "com.example.fixture", 42
	return target, nil
}
func (f *fakeDriver) Inspect(_ context.Context, _ Target, screenshot string) (State, error) {
	if err := os.WriteFile(screenshot, []byte("png"), 0o600); err != nil {
		return State{}, err
	}
	return State{App: App{Name: "Fixture", BundleID: "com.example.fixture", PID: 42},
		Window: Window{ID: 7, Width: 800, Height: 600}, Permission: "ready"}, nil
}
func (f *fakeDriver) Act(_ context.Context, _ Target, action Action) error {
	f.actions = append(f.actions, action)
	return nil
}

func TestPersistentComputerUseSession(t *testing.T) {
	driver := &fakeDriver{}
	root := t.TempDir()
	manager := NewManagerWithDriver(context.Background(), driver)
	report, err := manager.Start(context.Background(), "thread-1", root, Target{Name: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || report.State == nil || report.State.App.BundleID != "com.example.fixture" || len(report.Artifacts) != 1 {
		t.Fatalf("unexpected start report: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(report.Artifacts[0].Path))); err != nil {
		t.Fatalf("screenshot artifact: %v", err)
	}
	report, err = manager.Act(context.Background(), "thread-1", Action{Type: "fill", X: 10, Y: 20, Text: "hello"})
	if err != nil || len(driver.actions) != 1 || report.State == nil {
		t.Fatalf("act report=%+v actions=%+v err=%v", report, driver.actions, err)
	}
	manager.Stop("thread-1")
	if _, err := manager.Inspect(context.Background(), "thread-1"); err == nil {
		t.Fatal("stopped session remained available")
	}
}

func TestComputerUseRejectsInvalidActions(t *testing.T) {
	manager := NewManagerWithDriver(context.Background(), &fakeDriver{})
	if _, err := manager.Start(context.Background(), "thread-1", t.TempDir(), Target{Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{{Type: "fill", Text: ""}, {Type: "scroll"}, {Type: "unknown"}} {
		if _, err := manager.Act(context.Background(), "thread-1", action); err == nil {
			t.Fatalf("invalid action was accepted: %+v", action)
		}
	}
}

// A repeat computer.start for the same app must reuse the existing session
// instead of relaunching (and re-activating, stealing focus from) the app.
func TestComputerUseStartReusesSameApp(t *testing.T) {
	driver := &fakeDriver{}
	manager := NewManagerWithDriver(context.Background(), driver)
	root := t.TempDir()
	if _, err := manager.Start(context.Background(), "thread-1", root, Target{Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	if driver.opens != 1 {
		t.Fatalf("expected 1 open after first start, got %d", driver.opens)
	}
	report, err := manager.Start(context.Background(), "thread-1", root, Target{Name: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if driver.opens != 1 {
		t.Fatalf("expected the second start to reuse the session (still 1 open), got %d", driver.opens)
	}
	if report.State == nil || report.State.App.BundleID != "com.example.fixture" {
		t.Fatalf("reused start returned an unexpected report: %+v", report)
	}
	// A start naming a URL always goes through Open, since that's how a URL
	// gets navigated to; the app being the same must not short-circuit it.
	if _, err := manager.Start(context.Background(), "thread-1", root, Target{Name: "Fixture", URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	if driver.opens != 2 {
		t.Fatalf("expected a URL start to still call Open, got %d opens", driver.opens)
	}
}

// LastScreenshot exposes the most recent screenshot path so an approval for
// a computer.act call can show the person what they're approving.
func TestComputerUseLastScreenshot(t *testing.T) {
	manager := NewManagerWithDriver(context.Background(), &fakeDriver{})
	if _, ok := manager.LastScreenshot("thread-1"); ok {
		t.Fatal("expected no screenshot before a session exists")
	}
	if _, err := manager.Start(context.Background(), "thread-1", t.TempDir(), Target{Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	path, ok := manager.LastScreenshot("thread-1")
	if !ok || path == "" {
		t.Fatalf("expected a screenshot path after start, got %q ok=%v", path, ok)
	}
}
