package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Engine status as shown in the menu bar.
const (
	StatusStarting = "starting"
	StatusOnline   = "online"
	StatusOffline  = "offline"
)

// Shell holds the native pieces: window, menu-bar item and the HTTP endpoints
// the UI uses to learn how to reach the engine and to trigger app actions.
type Shell struct {
	App     *application.App
	Window  *application.WebviewWindow
	Mini    *application.WebviewWindow // the small overlay window; created lazily, see mini.go
	Tray    *application.SystemTray
	Engine  *EngineManager
	Watcher *Watcher
	Log     *slog.Logger

	starting      atomic.Bool
	mu            sync.Mutex
	status        string
	statusDetail  string
	pending       int
	running       int
	projects      []protocol.Project
	statusItem    *application.MenuItem
	approvalsItem *application.MenuItem
	loginItem     *application.MenuItem
}

// ---- window ----

// ShowWindow brings the main window to the front.
func (s *Shell) ShowWindow() {
	application.InvokeAsync(func() {
		s.Window.Show()
		s.Window.Focus()
		s.App.Show()
	})
}

// runJS calls one of the window.umcode hooks defined in frontend/src/main.ts.
func (s *Shell) runJS(js string) {
	application.InvokeAsync(func() {
		s.Window.ExecJS("window.umcode && " + js)
	})
}

func (s *Shell) ShowView(view string) {
	s.ShowWindow()
	s.runJS(fmt.Sprintf("window.umcode.show(%q)", view))
}

func (s *Shell) OpenThread(id string) {
	s.ShowWindow()
	b, _ := json.Marshal(id)
	s.runJS("window.umcode.openThread(" + string(b) + ")")
}

// OpenProject switches the window to a project.
func (s *Shell) OpenProject(id string) {
	s.ShowWindow()
	b, _ := json.Marshal(id)
	s.runJS("window.umcode.openProject(" + string(b) + ")")
}

// ---- menu bar ----

// SetProjects updates the menu-bar list of projects.
func (s *Shell) SetProjects(list []protocol.Project) {
	s.mu.Lock()
	same := len(list) == len(s.projects)
	if same {
		for i := range list {
			if list[i].ID != s.projects[i].ID || list[i].Name != s.projects[i].Name {
				same = false
				break
			}
		}
	}
	s.projects = list
	s.mu.Unlock()
	if !same {
		application.InvokeAsync(func() { s.buildTrayMenu() })
	}
}

func (s *Shell) buildTrayMenu() {
	s.mu.Lock()
	projects := s.projects
	s.mu.Unlock()

	m := s.App.NewMenu()
	s.statusItem = m.Add("Engine: starting…").SetEnabled(false)
	s.approvalsItem = m.Add("No pending approvals").OnClick(func(*application.Context) { s.ShowView("approvals") })
	s.approvalsItem.SetEnabled(false)
	m.AddSeparator()
	m.Add("Open UMCode").OnClick(func(*application.Context) { s.ShowWindow() })
	m.Add("New Chat").OnClick(func(*application.Context) {
		s.ShowWindow()
		s.runJS("window.umcode.newChat()")
	})
	if len(projects) > 0 {
		sub := m.AddSubmenu("Projects")
		for _, p := range projects {
			id, name := p.ID, p.Name
			if p.Missing {
				name += " (folder missing)"
			}
			sub.Add(name).OnClick(func(*application.Context) { s.OpenProject(id) })
		}
	}
	m.Add("Usage").OnClick(func(*application.Context) { s.ShowView("usage") })
	m.Add("Settings…").OnClick(func(*application.Context) { s.ShowView("settings") })
	m.AddSeparator()
	m.Add("Mini Overlay").OnClick(func(*application.Context) { s.ToggleMini() })
	m.AddSeparator()
	s.loginItem = m.AddCheckbox("Open at Login", s.launchAtLogin()).OnClick(func(ctx *application.Context) {
		if err := s.setLaunchAtLogin(ctx.ClickedMenuItem().Checked()); err != nil {
			s.Log.Warn("launch at login", "err", err)
			ctx.ClickedMenuItem().SetChecked(s.launchAtLogin())
		}
	})
	m.Add("Restart Engine").OnClick(func(*application.Context) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.Engine.Restart(ctx); err != nil {
				s.SetStatus(StatusOffline, err.Error())
			}
			s.runJS("window.umcode.reconnect()")
		}()
	})
	m.AddSeparator()
	m.Add("Quit UMCode").OnClick(func(*application.Context) { s.App.Quit() })
	s.Tray.SetMenu(m)
	s.Tray.SetTooltip("UMCode")
}

// SetStatus updates the engine line of the menu.
func (s *Shell) SetStatus(status, detail string) {
	s.mu.Lock()
	s.status, s.statusDetail = status, detail
	s.mu.Unlock()
	s.refreshTray()
}

// SetCounts updates the pending-approval and running-turn counts. A negative
// running count keeps the previous value.
//
// This used to also auto-pop the mini overlay on a new approval (the 0→n
// edge), even while the main window was already open and showing that same
// approval inline in chat — a redundant second copy of the same card. The
// chat's inline ApprovalCard is now the only place an approval shows up; the
// overlay is still available from the tray menu ("Mini Overlay") for anyone
// who wants it, it just no longer appears on its own.
func (s *Shell) SetCounts(pending, running int) {
	s.mu.Lock()
	if running < 0 {
		running = s.running
	}
	changed := s.pending != pending || s.running != running
	s.pending, s.running = pending, running
	s.mu.Unlock()
	if changed {
		s.refreshTray()
	}
}

func (s *Shell) refreshTray() {
	s.mu.Lock()
	status, detail, pending, running := s.status, s.statusDetail, s.pending, s.running
	s.mu.Unlock()
	mode, _ := s.Engine.Mode()
	line := "Engine: starting…"
	switch status {
	case StatusOnline:
		line = "Engine: running"
		if mode == ModeChild {
			line += " (from the app)"
		}
		if running > 0 {
			line += fmt.Sprintf(" · %d working", running)
		}
	case StatusOffline:
		line = "Engine: offline"
		if detail != "" {
			if len(detail) > 60 {
				detail = detail[:60] + "…"
			}
			line += " - " + detail
		}
	}
	approvals := "No pending approvals"
	if pending == 1 {
		approvals = "1 approval waiting…"
	} else if pending > 1 {
		approvals = fmt.Sprintf("%d approvals waiting…", pending)
	}
	label := ""
	if pending > 0 {
		label = fmt.Sprint(pending)
	}
	tooltip := "UMCode - " + strings.TrimPrefix(line, "Engine: ")
	application.InvokeAsync(func() {
		s.statusItem.SetLabel(line)
		s.approvalsItem.SetLabel(approvals)
		s.approvalsItem.SetEnabled(pending > 0)
		s.Tray.SetLabel(label)
		s.Tray.SetTooltip(tooltip)
	})
}

// ---- login item and CLI ----

func (s *Shell) launchAtLogin() bool {
	ok, err := s.App.Autostart.IsEnabled()
	return err == nil && ok
}

func (s *Shell) setLaunchAtLogin(on bool) error {
	if on {
		return s.App.Autostart.Enable()
	}
	return s.App.Autostart.Disable()
}

const cliLink = "/usr/local/bin/umcode"

func cliInstalled() (string, bool) {
	target, err := os.Readlink(cliLink)
	if err != nil {
		return "", false
	}
	bin, err := EngineBinary()
	return cliLink, err == nil && (target == bin || strings.HasSuffix(target, "/Contents/Resources/umcode"))
}

// installCLI links /usr/local/bin/umcode to the engine binary inside the
// bundle, asking for an administrator password via the standard macOS prompt.
func installCLI() error {
	bin, err := EngineBinary()
	if err != nil {
		return err
	}
	if strings.ContainsAny(bin, `"'\`) {
		return errors.New("the app is installed at a path with quotes in it; move it to /Applications")
	}
	script := fmt.Sprintf(`do shell script "mkdir -p /usr/local/bin && ln -sf '%s' '%s'" with administrator privileges with prompt "UMCode wants to install the command-line tool."`, bin, cliLink)
	out, err := exec.Command("/usr/bin/osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") {
			return errors.New("cancelled")
		}
		return fmt.Errorf("install failed: %s", msg)
	}
	return nil
}

func reveal(path string) error {
	return exec.Command("/usr/bin/open", path).Run()
}

// ensureEngine starts the engine if it is not running, at most one attempt at
// a time.
func (s *Shell) ensureEngine() {
	if !s.starting.CompareAndSwap(false, true) {
		return
	}
	defer s.starting.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if s.Engine.Reachable() {
		return
	}
	if err := s.Engine.Ensure(ctx); err != nil {
		s.Log.Warn("engine start", "err", err)
		s.SetStatus(StatusOffline, err.Error())
	}
}

// ---- HTTP endpoints for the UI ----

// Middleware answers /__umcode/* inside the webview's asset server; all
// other requests go to the embedded UI.
func (s *Shell) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/__umcode/connection":
			s.handleConnection(w)
		case r.URL.Path == "/__umcode/shell" && r.Method == http.MethodGet:
			s.handleShellInfo(w)
		case strings.HasPrefix(r.URL.Path, "/__umcode/shell/") && r.Method == http.MethodPost:
			s.handleShellAction(w, r, strings.TrimPrefix(r.URL.Path, "/__umcode/shell/"))
		case strings.HasPrefix(r.URL.Path, "/__umcode/mini/") && r.Method == http.MethodPost:
			s.handleMiniAction(w, r, strings.TrimPrefix(r.URL.Path, "/__umcode/mini/"))
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Shell) handleConnection(w http.ResponseWriter) {
	// Every answer says which shell this is, so the UI words itself correctly
	// even when there is no engine to connect to yet.
	fail := func(msg string) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": msg, "shell": "mac", "version": Version})
	}
	ep, err := s.Engine.Endpoints()
	if err != nil {
		fail("config: " + err.Error())
		return
	}
	if ep.WSPort == 0 {
		fail("the engine's WebSocket is switched off (runtime.engine_ws_port is 0 in config.yaml)")
		return
	}
	// The token file is intentionally persistent, so its presence does not
	// prove the engine is alive. Check the socket before returning connection
	// details; this also makes the UI's Retry button actually start a dead
	// engine instead of repeatedly reconnecting to a stale endpoint.
	if !s.Engine.Reachable() {
		go s.ensureEngine()
		mode, detail := s.Engine.Mode()
		msg := "starting the engine…"
		if mode == ModeStopped && detail != "" {
			msg = "the engine could not be started: " + detail
		}
		fail(msg)
		return
	}
	tok, err := os.ReadFile(ep.TokenPath)
	if err != nil {
		// Asking again is the user pressing "Retry now": take it as a cue to
		// start the engine, in case the first attempt failed.
		go s.ensureEngine()
		mode, detail := s.Engine.Mode()
		msg := "starting the engine…"
		if mode == ModeStopped && detail != "" {
			msg = "the engine could not be started: " + detail
		}
		fail(msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"url":     fmt.Sprintf("ws://%s/ws", netJoin(ep.WSHost, ep.WSPort)),
		"token":   strings.TrimSpace(string(tok)),
		"shell":   "mac",
		"version": Version,
	})
}

func netJoin(host string, port int) string {
	if strings.Contains(host, ":") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func (s *Shell) handleShellInfo(w http.ResponseWriter) {
	mode, detail := s.Engine.Mode()
	home, _ := appHome()
	path, installed := cliInstalled()
	_, autoErr := s.App.Autostart.IsEnabled()
	writeJSON(w, http.StatusOK, map[string]any{
		"appVersion":             Version,
		"engineMode":             mode,
		"engineDetail":           detail,
		"launchAtLogin":          s.launchAtLogin(),
		"launchAtLoginSupported": autoErr == nil,
		"cliPath":                path,
		"cliInstalled":           installed,
		"homeDir":                home,
	})
}

func (s *Shell) handleShellAction(w http.ResponseWriter, r *http.Request, action string) {
	var body struct {
		Enabled bool   `json:"enabled"`
		URL     string `json:"url"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var msg string
	var err error
	switch action {
	case "restartEngine":
		err = s.Engine.Restart(ctx)
		msg = "Engine restarted."
	case "chooseFolder":
		if s.App == nil || s.Window == nil {
			err = errors.New("folder picker is only available in the desktop app")
			break
		}
		var folder string
		folder, err = s.App.Dialog.OpenFile().CanChooseFiles(false).CanChooseDirectories(true).
			CanCreateDirectories(true).SetTitle("Choose project folder").SetButtonText("Use folder").
			AttachToWindow(s.Window).PromptForSingleSelection()
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]string{"path": folder})
			return
		}
	case "installService":
		err = s.Engine.InstallService(ctx)
		msg = "The engine now runs in the background and starts at login."
		if err != nil && strings.Contains(err.Error(), "Login Items") {
			openLoginItemsSettings()
		}
	case "uninstallService":
		err = s.Engine.UninstallService(ctx)
		msg = "Background service removed; the engine now runs while the app is open."
	case "setLaunchAtLogin":
		err = s.setLaunchAtLogin(body.Enabled)
		if s.loginItem != nil {
			on := s.launchAtLogin()
			application.InvokeAsync(func() { s.loginItem.SetChecked(on) })
		}
	case "installCLI":
		err = installCLI()
		msg = "Installed " + cliLink + "."
	case "openDataFolder":
		var home string
		if home, err = appHome(); err == nil {
			err = reveal(home)
		}
	case "openLogs":
		var home string
		if home, err = appHome(); err == nil {
			err = reveal(filepath.Join(home, "logs"))
		}
	case "openURL":
		u, perr := url.Parse(body.URL)
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "mailto") {
			err = errors.New("only web and mailto links can be opened")
		} else {
			err = s.App.Browser.OpenURL(u.String())
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action " + action})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}
