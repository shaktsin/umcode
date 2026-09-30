// Package computeruse owns persistent, opt-in desktop automation sessions.
// On macOS, native capture and input are brokered by the UMCode desktop app so
// the user grants permissions to one first-party app rather than a helper.
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
	App        App       `json:"app"`
	Window     Window    `json:"window"`
	Permission string    `json:"permission,omitempty"`
	Controls   []Control `json:"controls,omitempty"`
}

// Control is a redacted accessibility-tree node. Bounds use screenshot-pixel
// coordinates, matching the coordinate space accepted by computer.act.
type Control struct {
	ID         string  `json:"id"`
	Role       string  `json:"role,omitempty"`
	Label      string  `json:"label,omitempty"`
	Identifier string  `json:"identifier,omitempty"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	Width      float64 `json:"width,omitempty"`
	Height     float64 `json:"height,omitempty"`
	Enabled    bool    `json:"enabled,omitempty"`
	Focused    bool    `json:"focused,omitempty"`
}

type Artifact struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type"`
	Bytes    int64  `json:"bytes"`
}

type Report struct {
	Status        string          `json:"status"`
	Framework     string          `json:"framework"`
	SessionID     string          `json:"session_id,omitempty"`
	State         *State          `json:"state,omitempty"`
	Apps          []App           `json:"apps,omitempty"`
	Artifacts     []Artifact      `json:"artifacts"`
	Reason        string          `json:"reason,omitempty"`
	DurationMS    int64           `json:"duration_ms,omitempty"`
	ActionCount   int             `json:"action_count,omitempty"`
	ObservationID string          `json:"observation_id,omitempty"`
	LastAction    *ActionEvidence `json:"last_action,omitempty"`
}

// ActionEvidence pairs the input with the exact observation returned to the
// model. It records dispatch, not success; the next observation must verify it.
type ActionEvidence struct {
	Type              string  `json:"type"`
	TargetDescription string  `json:"target_description,omitempty"`
	ElementID         string  `json:"element_id,omitempty"`
	X                 float64 `json:"x,omitempty"`
	Y                 float64 `json:"y,omitempty"`
	ScreenshotWidth   int     `json:"screenshot_width,omitempty"`
	ScreenshotHeight  int     `json:"screenshot_height,omitempty"`
	ObservationID     string  `json:"observation_id,omitempty"`
	Result            string  `json:"result"`
}

type Action struct {
	Type              string  `json:"type"`
	X                 float64 `json:"x,omitempty"`
	Y                 float64 `json:"y,omitempty"`
	Text              string  `json:"text,omitempty"`
	Key               string  `json:"key,omitempty"`
	Delta             int     `json:"delta,omitempty"`
	ObservationID     string  `json:"observation_id,omitempty"`
	ElementID         string  `json:"element_id,omitempty"`
	TargetDescription string  `json:"target_description,omitempty"`
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
	// LastAnnotatedScreenshotRel is LastScreenshotRel with a marker drawn at
	// the point of the most recent click/fill, for a person watching the
	// live view to see exactly what was clicked. Empty when the last
	// screenshot was not preceded by a point action, or annotation failed.
	LastAnnotatedScreenshotRel string
	// ActionCount is how many Act calls have run in this session. Surfaced on
	// every report as a lightweight, always-on audit trail of how much this
	// session has actually done, independent of the approval log.
	ActionCount      int
	ObservationCount uint64
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
		report, err := m.inspect(ctx, existing, fmt.Sprintf("start-%d.png", time.Now().UnixMilli()), true)
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
	report, err := m.inspect(ctx, s, "initial.png", true)
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
	return m.inspect(ctx, s, fmt.Sprintf("inspect-%d.png", time.Now().UnixMilli()), true)
}

// Refresh captures the current target window into one stable artifact path for
// the embedded Computer Use workspace. Unlike agent inspect calls, repeated UI
// refreshes replace this rolling screenshot instead of growing the artifact set.
func (m *Manager) Refresh(ctx context.Context, threadID string) (Report, error) {
	s := m.ForThread(threadID)
	if s == nil {
		return Report{}, errors.New("no Computer Use session is running for this task")
	}
	// Passive live-view polling must not invalidate coordinates the model just
	// selected from its own observation while it is reasoning about an action.
	return m.inspect(ctx, s, "live.png", false)
}

func (m *Manager) Act(ctx context.Context, threadID string, action Action) (Report, error) {
	s := m.ForThread(threadID)
	if s == nil {
		return Report{}, errors.New("no Computer Use session is running for this task")
	}
	switch action.Type {
	case "move", "click", "double_click":
		if action.ElementID == "" && (action.X < 0 || action.Y < 0) {
			return Report{}, errors.New("click coordinates must be non-negative")
		}
	case "fill":
		if (action.ElementID == "" && (action.X < 0 || action.Y < 0)) || action.Text == "" {
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
	pointAction := action.Type == "move" || action.Type == "click" || action.Type == "double_click" || action.Type == "fill"
	visualPoint := pointAction
	if action.ObservationID != "" && action.ObservationID != observationID(s) {
		return Report{}, errors.New("Computer Use action used a stale screenshot; inspect the target app again before acting")
	}
	sourceObservationID := observationID(s)
	sourceWidth, sourceHeight := s.Last.Window.PixelWidth, s.Last.Window.PixelHeight
	if action.ElementID != "" {
		control, ok := findControl(s.Last.Controls, action.ElementID)
		if !ok {
			return Report{}, fmt.Errorf("accessibility control %q is not in the latest observation; inspect and re-target", action.ElementID)
		}
		if !control.Enabled {
			return Report{}, fmt.Errorf("accessibility control %q is disabled", action.ElementID)
		}
		if control.Width > 0 && control.Height > 0 {
			action.X, action.Y = control.X+control.Width/2, control.Y+control.Height/2
			if sourceWidth > 0 && action.X >= float64(sourceWidth) {
				action.X = float64(sourceWidth - 1)
			}
			if sourceHeight > 0 && action.Y >= float64(sourceHeight) {
				action.Y = float64(sourceHeight - 1)
			}
			if action.X < 0 {
				action.X = 0
			}
			if action.Y < 0 {
				action.Y = 0
			}
		} else {
			visualPoint = false
		}
	}
	clickX, clickY := action.X, action.Y
	if pointAction && s.Last.Window.PixelWidth > 0 && s.Last.Window.PixelHeight > 0 {
		action.X *= s.Last.Window.Width / float64(s.Last.Window.PixelWidth)
		action.Y *= s.Last.Window.Height / float64(s.Last.Window.PixelHeight)
	}
	// The desktop broker targets the selected app and refreshes its in-app view
	// after every action; the engine itself does not synthesize host input.
	if err := m.driver.Act(ctx, s.Target, action); err != nil {
		return Report{}, err
	}
	s.ActionCount++
	report, err := m.inspect(ctx, s, fmt.Sprintf("action-%d.png", time.Now().UnixMilli()), true)
	if err != nil {
		return Report{}, err
	}
	// The native event being dispatched is not proof that the target app
	// accepted it or changed state. Keep this distinct from verification; the
	// agent must inspect the returned screenshot before reporting an outcome.
	report.Status = "dispatched"
	if visualPoint {
		if rel, aerr := m.markClick(s, clickX, clickY); aerr == nil {
			report.Artifacts = append(report.Artifacts, Artifact{Path: rel, Kind: "screenshot_action", MimeType: "image/png"})
		}
	}
	report.LastAction = &ActionEvidence{Type: action.Type, TargetDescription: action.TargetDescription, ElementID: action.ElementID, X: clickX, Y: clickY,
		ScreenshotWidth: sourceWidth, ScreenshotHeight: sourceHeight,
		ObservationID: sourceObservationID, Result: "dispatched; verify against this fresh screenshot"}
	return report, nil
}

func findControl(controls []Control, id string) (Control, bool) {
	for _, control := range controls {
		if control.ID == id {
			return control, true
		}
	}
	return Control{}, false
}

func (m *Manager) inspect(ctx context.Context, s *Session, name string, advanceObservation bool) (Report, error) {
	if err := os.MkdirAll(s.ArtifactDir, 0o700); err != nil {
		return Report{}, err
	}
	abs := filepath.Join(s.ArtifactDir, name)
	state, err := m.driver.Inspect(ctx, s.Target, abs)
	if err != nil {
		return Report{}, err
	}
	s.Last = state
	if advanceObservation {
		s.ObservationCount++
	}
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
	// No hide/show step here either: the helper captures this screenshot
	// with CGWindowListCreateImage, which works on an occluded or
	// non-frontmost window just as well as a visible one.
	return Report{Status: "passed", Framework: "umcode-computer-use", SessionID: s.ID, State: &state, ObservationID: observationID(s),
		Artifacts:   []Artifact{{Path: relSlash, Kind: "screenshot", MimeType: "image/png", Bytes: st.Size()}},
		ActionCount: s.ActionCount}, nil
}

func observationID(s *Session) string {
	return fmt.Sprintf("%s-%d", s.ID, s.ObservationCount)
}

// markClick draws a pointer marker onto a copy of the session's latest
// screenshot at (x, y) — screenshot-pixel coordinates, the same space the
// model targets — so both the model and a person watching Computer Use can
// inspect where the action was aimed. Best-effort: a screenshot that
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

// drawClickMarker paints a prominent white-and-blue ring with a crosshair centered on
// (x, y), clipped to the image bounds.
func drawClickMarker(img *image.RGBA, x, y float64) {
	cx, cy := int(x), int(y)
	b := img.Bounds()
	blue := color.RGBA{56, 189, 248, 255}
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
	ring(30, 5, white)
	ring(25, 4, blue)
	const armInner, armOuter = 7, 26
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
