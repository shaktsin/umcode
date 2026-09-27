// Package computeruse owns persistent, opt-in desktop automation sessions.
// The engine never synthesizes host input itself; a separately bundled helper
// owns macOS Screen Recording and Accessibility permissions.
package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type App struct {
	Name     string `json:"name"`
	BundleID string `json:"bundle_id,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Path     string `json:"path,omitempty"`
}

type Window struct {
	ID          int     `json:"id"`
	Title       string  `json:"title,omitempty"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	PixelWidth  int     `json:"pixel_width,omitempty"`
	PixelHeight int     `json:"pixel_height,omitempty"`
}

type Target struct {
	Name     string `json:"name,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	PID      int    `json:"pid,omitempty"`
}

type State struct {
	App        App      `json:"app"`
	Window     Window   `json:"window"`
	Permission string   `json:"permission,omitempty"`
	Controls   []string `json:"controls,omitempty"`
}

type Artifact struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type"`
	Bytes    int64  `json:"bytes"`
}

type Report struct {
	Status     string     `json:"status"`
	Framework  string     `json:"framework"`
	SessionID  string     `json:"session_id,omitempty"`
	State      *State     `json:"state,omitempty"`
	Apps       []App      `json:"apps,omitempty"`
	Artifacts  []Artifact `json:"artifacts"`
	Reason     string     `json:"reason,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
}

type Action struct {
	Type  string  `json:"type"`
	X     float64 `json:"x,omitempty"`
	Y     float64 `json:"y,omitempty"`
	Text  string  `json:"text,omitempty"`
	Key   string  `json:"key,omitempty"`
	Delta int     `json:"delta,omitempty"`
}

type driver interface {
	List(context.Context) ([]App, error)
	Open(context.Context, Target) (Target, error)
	Inspect(context.Context, Target, string) (State, error)
	Act(context.Context, Target, Action) error
}

type Manager struct {
	ctx      context.Context
	driver   driver
	mu       sync.Mutex
	sessions map[string]*Session
}

type Session struct {
	ID, ThreadID, Root, ArtifactDir string
	Target                          Target
	Last                            State
	// LastScreenshotRel is the project-relative path of the most recent
	// screenshot taken for this session (set on every inspect/act). Approvals
	// for computer.act attach it so the person can see what they're approving.
	LastScreenshotRel string
}

func NewManager(ctx context.Context) *Manager {
	return &Manager{ctx: ctx, driver: newDriver(), sessions: map[string]*Session{}}
}

func NewManagerWithDriver(ctx context.Context, d driver) *Manager {
	return &Manager{ctx: ctx, driver: d, sessions: map[string]*Session{}}
}

func (m *Manager) List(ctx context.Context) (Report, error) {
	apps, err := m.driver.List(ctx)
	if err != nil {
		return Report{}, err
	}
	return Report{Status: "passed", Framework: "umcode-computer-use", Apps: apps, Artifacts: []Artifact{}}, nil
}

func (m *Manager) Start(ctx context.Context, threadID, root string, target Target) (Report, error) {
	started := time.Now()
	if threadID == "" || root == "" {
		return Report{}, errors.New("computer use requires a project task")
	}
	// If this task already has a session and the caller asked for the same
	// app again (e.g. a retry, or the model re-selecting it in a later
	// turn), reuse it instead of relaunching: relaunching re-activates the
	// app and steals focus from whatever the person switched to since.
	if existing := m.ForThread(threadID); existing != nil && target.URL == "" && sameApp(existing.Target, target) {
		report, err := m.inspect(ctx, existing, fmt.Sprintf("start-%d.png", time.Now().UnixMilli()))
		if err != nil {
			return Report{}, err
		}
		report.DurationMS = time.Since(started).Milliseconds()
		return report, nil
	}
	opened, err := m.driver.Open(ctx, target)
	if err != nil {
		return Report{}, err
	}
	id := "computer-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	s := &Session{ID: id, ThreadID: threadID, Root: root, Target: opened,
		ArtifactDir: filepath.Join(root, ".umcode", "artifacts", "computer-use", id)}
	m.mu.Lock()
	m.sessions[threadID] = s
	m.mu.Unlock()
	report, err := m.inspect(ctx, s, "initial.png")
	if err != nil {
		m.Stop(threadID)
		return Report{}, err
	}
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

func (m *Manager) Inspect(ctx context.Context, threadID string) (Report, error) {
	s := m.ForThread(threadID)
	if s == nil {
		return Report{}, errors.New("no Computer Use session is running for this task")
	}
	return m.inspect(ctx, s, fmt.Sprintf("inspect-%d.png", time.Now().UnixMilli()))
}

func (m *Manager) Act(ctx context.Context, threadID string, action Action) (Report, error) {
	s := m.ForThread(threadID)
	if s == nil {
		return Report{}, errors.New("no Computer Use session is running for this task")
	}
	switch action.Type {
	case "click", "double_click":
		if action.X < 0 || action.Y < 0 {
			return Report{}, errors.New("click coordinates must be non-negative")
		}
	case "fill":
		if action.X < 0 || action.Y < 0 || action.Text == "" {
			return Report{}, errors.New("fill requires coordinates and text")
		}
	case "type":
		if action.Text == "" {
			return Report{}, errors.New("type requires text")
		}
	case "key":
		if strings.TrimSpace(action.Key) == "" {
			return Report{}, errors.New("key requires a key name")
		}
	case "scroll":
		if action.Delta == 0 {
			return Report{}, errors.New("scroll requires a non-zero delta")
		}
	default:
		return Report{}, fmt.Errorf("unsupported computer action %q", action.Type)
	}
	// Screenshots are commonly Retina pixel dimensions while CGEvent uses
	// display points. Map model-selected screenshot coordinates back to the
	// current window's coordinate space before sending input.
	if (action.Type == "click" || action.Type == "double_click" || action.Type == "fill") && s.Last.Window.PixelWidth > 0 && s.Last.Window.PixelHeight > 0 {
		action.X *= s.Last.Window.Width / float64(s.Last.Window.PixelWidth)
		action.Y *= s.Last.Window.Height / float64(s.Last.Window.PixelHeight)
	}
	if err := m.driver.Act(ctx, s.Target, action); err != nil {
		return Report{}, err
	}
	return m.inspect(ctx, s, fmt.Sprintf("action-%d.png", time.Now().UnixMilli()))
}

func (m *Manager) inspect(ctx context.Context, s *Session, name string) (Report, error) {
	if err := os.MkdirAll(s.ArtifactDir, 0o700); err != nil {
		return Report{}, err
	}
	abs := filepath.Join(s.ArtifactDir, name)
	state, err := m.driver.Inspect(ctx, s.Target, abs)
	if err != nil {
		return Report{}, err
	}
	s.Last = state
	st, err := os.Stat(abs)
	if err != nil {
		return Report{}, fmt.Errorf("computer use screenshot: %w", err)
	}
	rel, err := filepath.Rel(s.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Report{}, errors.New("computer use artifact escaped the project workspace")
	}
	relSlash := filepath.ToSlash(rel)
	s.LastScreenshotRel = relSlash
	return Report{Status: "passed", Framework: "umcode-computer-use", SessionID: s.ID, State: &state,
		Artifacts: []Artifact{{Path: relSlash, Kind: "screenshot", MimeType: "image/png", Bytes: st.Size()}}}, nil
}

// LastScreenshot returns the most recent screenshot's project-relative path
// for this task's Computer Use session, if one is running and has taken at
// least one screenshot.
func (m *Manager) LastScreenshot(threadID string) (string, bool) {
	s := m.ForThread(threadID)
	if s == nil || s.LastScreenshotRel == "" {
		return "", false
	}
	return s.LastScreenshotRel, true
}

// sameApp reports whether two targets identify the same running app, so a
// repeat computer.start can reuse the existing session instead of
// relaunching (and re-activating, stealing focus) it.
func sameApp(a, b Target) bool {
	switch {
	case a.BundleID != "" && b.BundleID != "":
		return strings.EqualFold(a.BundleID, b.BundleID)
	case a.PID != 0 && b.PID != 0 && a.Name != "" && b.Name != "":
		return a.PID == b.PID
	case a.Path != "" && b.Path != "":
		return strings.EqualFold(a.Path, b.Path)
	case a.Name != "" && b.Name != "":
		return strings.EqualFold(a.Name, b.Name)
	default:
		return false
	}
}

func (m *Manager) ForThread(threadID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[threadID]
}

func (m *Manager) Stop(threadID string) {
	m.mu.Lock()
	delete(m.sessions, threadID)
	m.mu.Unlock()
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
}

func marshalReport(r Report) string {
	b, _ := json.Marshal(r)
	return string(b)
}
