// Package computeruse owns persistent, opt-in desktop automation sessions.
// The engine never synthesizes host input itself; a separately bundled helper
// owns macOS Screen Recording and Accessibility permissions.
package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
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
	Status      string     `json:"status"`
	Framework   string     `json:"framework"`
	SessionID   string     `json:"session_id,omitempty"`
	State       *State     `json:"state,omitempty"`
	Apps        []App      `json:"apps,omitempty"`
	Artifacts   []Artifact `json:"artifacts"`
	Reason      string     `json:"reason,omitempty"`
	DurationMS  int64      `json:"duration_ms,omitempty"`
	ActionCount int        `json:"action_count,omitempty"`
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
	// Hide and Show are best-effort: a driver that cannot support them (no
	// permission granted, or the non-darwin stub) returns an error, which
	// callers treat as "leave visibility as it is" rather than a failure.
	Hide(context.Context, Target) error
	Show(context.Context, Target) error
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
	// LastAnnotatedScreenshotRel is LastScreenshotRel with a marker drawn at
	// the point of the most recent click/fill, for a person watching the
	// live view to see exactly what was clicked. Empty when the last
	// screenshot was not preceded by a point action, or annotation failed.
	LastAnnotatedScreenshotRel string
	// ActionCount is how many Act calls have run in this session. Surfaced on
	// every report as a lightweight, always-on audit trail of how much this
	// session has actually done, independent of the approval log.
	ActionCount int
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
	// current window's coordinate space before sending input. clickX/clickY
	// keep the original screenshot-pixel coordinates for the click marker
	// drawn below, independent of that rescaling.
	pointAction := action.Type == "click" || action.Type == "double_click" || action.Type == "fill"
	clickX, clickY := action.X, action.Y
	if pointAction && s.Last.Window.PixelWidth > 0 && s.Last.Window.PixelHeight > 0 {
		action.X *= s.Last.Window.Width / float64(s.Last.Window.PixelWidth)
		action.Y *= s.Last.Window.Height / float64(s.Last.Window.PixelHeight)
	}
	// The window may have been tucked away (see inspect below) since the
	// last action; bring it back so this click lands on the right target.
	// Best-effort: if UMCode hasn't been granted the Automation permission
	// this needs, the app just stays visible like it always did.
	_ = m.driver.Show(ctx, s.Target)
	if err := m.driver.Act(ctx, s.Target, action); err != nil {
		return Report{}, err
	}
	s.ActionCount++
	report, err := m.inspect(ctx, s, fmt.Sprintf("action-%d.png", time.Now().UnixMilli()))
	if err != nil {
		return Report{}, err
	}
	if pointAction {
		if rel, aerr := m.markClick(s, clickX, clickY); aerr == nil {
			report.Artifacts = append(report.Artifacts, Artifact{Path: rel, Kind: "screenshot_click", MimeType: "image/png"})
		}
	}
	return report, nil
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
	// Now that the screenshot for this step is captured, tuck the app back
	// out of the way instead of leaving it sitting in front of whatever the
	// person is actually working on. Best-effort, same as Show above.
	_ = m.driver.Hide(ctx, s.Target)
	return Report{Status: "passed", Framework: "umcode-computer-use", SessionID: s.ID, State: &state,
		Artifacts:   []Artifact{{Path: relSlash, Kind: "screenshot", MimeType: "image/png", Bytes: st.Size()}},
		ActionCount: s.ActionCount}, nil
}

// markClick draws a small ring-and-crosshair marker onto a copy of the
// session's latest screenshot at (x, y) — screenshot-pixel coordinates, the
// same space the model targets — so a person watching Computer Use live can
// see exactly what the last click landed on. Best-effort: a screenshot that
// isn't a decodable PNG (as in tests, or a driver hiccup) just means no
// marker, not a failed action.
func (m *Manager) markClick(s *Session, x, y float64) (string, error) {
	if s.LastScreenshotRel == "" {
		return "", errors.New("no screenshot to annotate")
	}
	abs := filepath.Join(s.Root, filepath.FromSlash(s.LastScreenshotRel))
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return "", err
	}
	img := image.NewRGBA(src.Bounds())
	draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)
	drawClickMarker(img, x, y)
	outAbs := strings.TrimSuffix(abs, filepath.Ext(abs)) + "-click.png"
	out, err := os.Create(outAbs)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.Root, outAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("annotated screenshot escaped the project workspace")
	}
	relSlash := filepath.ToSlash(rel)
	s.LastAnnotatedScreenshotRel = relSlash
	return relSlash, nil
}

// drawClickMarker paints a white-and-red ring with a crosshair centered on
// (x, y), clipped to the image bounds.
func drawClickMarker(img *image.RGBA, x, y float64) {
	cx, cy := int(x), int(y)
	b := img.Bounds()
	red := color.RGBA{237, 63, 63, 255}
	white := color.RGBA{255, 255, 255, 255}
	ring := func(radius, thickness int, c color.RGBA) {
		for dy := -radius; dy <= radius; dy++ {
			for dx := -radius; dx <= radius; dx++ {
				d2 := dx*dx + dy*dy
				if d2 <= radius*radius && d2 >= (radius-thickness)*(radius-thickness) {
					p := image.Pt(cx+dx, cy+dy)
					if p.In(b) {
						img.Set(p.X, p.Y, c)
					}
				}
			}
		}
	}
	line := func(x0, y0, x1, y1 int, c color.RGBA) {
		dx, dy := x1-x0, y1-y0
		steps := dx
		if dy > steps {
			steps = dy
		}
		if -dx > steps {
			steps = -dx
		}
		if -dy > steps {
			steps = -dy
		}
		if steps == 0 {
			steps = 1
		}
		for i := 0; i <= steps; i++ {
			p := image.Pt(x0+dx*i/steps, y0+dy*i/steps)
			if p.In(b) {
				img.Set(p.X, p.Y, c)
			}
		}
	}
	ring(14, 3, white)
	ring(11, 3, red)
	const armInner, armOuter = 4, 10
	line(cx-armOuter, cy, cx-armInner, cy, white)
	line(cx+armInner, cy, cx+armOuter, cy, white)
	line(cx, cy-armOuter, cx, cy-armInner, white)
	line(cx, cy+armInner, cx, cy+armOuter, white)
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
