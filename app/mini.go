package main

import (
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// miniWidth/miniHeight size the overlay: small enough to sit unobtrusively
// over another app, wide enough for a truncated approval summary.
const (
	miniWidth  = 380
	miniHeight = 190
)

// ensureMini creates the mini overlay window the first time it is needed. It
// is a non-activating NSPanel: showing it never steals keyboard focus from
// whatever app the user is working in, and it stays visible over full-screen
// apps and on every Space, the same way Claude's and Codex's small floating
// windows behave. The user can drag it anywhere; its position is kept for as
// long as the app runs.
func (s *Shell) ensureMini() *application.WebviewWindow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Mini != nil {
		return s.Mini
	}
	win := s.App.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "mini",
		Title:            "UMCode",
		Width:            miniWidth,
		Height:           miniHeight,
		Frameless:        true,
		DisableResize:    true,
		AlwaysOnTop:      true,
		Hidden:           true,
		URL:              "/?mini=1",
		BackgroundColour: application.NewRGB(23, 32, 51),
		KeyBindings: map[string]func(application.Window){
			"escape": func(application.Window) { s.HideMini() },
		},
		Mac: application.MacWindow{
			WindowClass: application.MacWindowClassPanel,
			PanelPreferences: application.MacPanelPreferences{
				NonActivating: true,
			},
			WindowLevel: application.MacWindowLevelFloating,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary |
				application.MacWindowCollectionBehaviorStationary,
		},
	})
	s.Mini = win
	return win
}

// ShowMini opens the overlay. It never touches the main window's visibility
// or focus.
func (s *Shell) ShowMini() {
	application.InvokeAsync(func() { s.ensureMini().Show() })
}

// HideMini dismisses the overlay, if it exists.
func (s *Shell) HideMini() {
	s.mu.Lock()
	win := s.Mini
	s.mu.Unlock()
	if win == nil {
		return
	}
	application.InvokeAsync(win.Hide)
}

// ToggleMini flips the overlay's visibility; used by the tray menu item.
func (s *Shell) ToggleMini() {
	s.mu.Lock()
	win := s.Mini
	s.mu.Unlock()
	if win != nil && win.IsVisible() {
		s.HideMini()
		return
	}
	s.ShowMini()
}

// handleMiniAction serves the mini window's own small HTTP actions. The
// overlay keeps its own engine RPC connection (it is a separate webview) and
// only asks the Go shell to move windows around: hide itself, or bring the
// main window forward.
func (s *Shell) handleMiniAction(w http.ResponseWriter, _ *http.Request, action string) {
	switch action {
	case "hide":
		s.HideMini()
	case "openMain":
		s.ShowWindow()
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown mini action " + action})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
