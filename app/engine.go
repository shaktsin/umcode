package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
)

// Engine run modes, as shown in Settings → Engine & app.
const (
	ModeService  = "service"  // LaunchAgent registered with SMAppService (or `umcode service install`)
	ModeChild    = "child"    // started by this app; stops when the app quits
	ModeExternal = "external" // already running when the app started (terminal, old LaunchAgent…)
	ModeStopped  = "stopped"
)

// ServiceLabel is the launchd label of the engine agent. It must match the
// plist in Contents/Library/LaunchAgents and `umcode service install`.
const ServiceLabel = "com.umcode.engine"

// EngineManager finds, starts and supervises the engine process.
type EngineManager struct {
	log *slog.Logger

	ensureMu sync.Mutex
	mu       sync.Mutex
	mode     string
	detail   string
	child    *exec.Cmd
	exited   chan struct{}
}

func NewEngineManager(log *slog.Logger) (*EngineManager, error) {
	return &EngineManager{log: log, mode: ModeStopped}, nil
}

func appHome() (string, error) { return config.HomeDir() }

// Settings the shell needs from config.yaml: where the engine listens.
type engineEndpoints struct {
	Home      string
	Socket    string
	WSHost    string
	WSPort    int
	TokenPath string
}

func loadEndpoints() (engineEndpoints, error) {
	cfg, err := config.Load("")
	if err != nil {
		return engineEndpoints{}, err
	}
	host := cfg.Runtime.WSHost
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return engineEndpoints{
		Home:      cfg.Home,
		Socket:    cfg.Runtime.SocketPath,
		WSHost:    host,
		WSPort:    cfg.Runtime.EngineWSPort,
		TokenPath: filepath.Join(filepath.Dir(cfg.Runtime.SocketPath), "token"),
	}, nil
}

// Endpoints re-reads config.yaml (cheap) so edits take effect on reconnect.
func (m *EngineManager) Endpoints() (engineEndpoints, error) { return loadEndpoints() }

// Reachable reports whether an engine answers on the socket.
func (m *EngineManager) Reachable() bool {
	ep, err := loadEndpoints()
	if err != nil {
		return false
	}
	c, err := net.DialTimeout("unix", ep.Socket, 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Mode returns how the engine is running and a detail line.
func (m *EngineManager) Mode() (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode, m.detail
}

func (m *EngineManager) setMode(mode, detail string) {
	m.mu.Lock()
	m.mode, m.detail = mode, detail
	m.mu.Unlock()
}

// EngineBinary finds the `umcode` executable: inside the app bundle first,
// then next to this executable, then on PATH.
//
// In the bundle it lives in Contents/Resources, not beside the app binary:
// the Mac's file system ignores case, so Contents/MacOS/umcode would be the
// same file as Contents/MacOS/UMCode.
func EngineBinary() (string, error) {
	if p := os.Getenv("UMCODE_ENGINE_BIN"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		for _, cand := range []string{
			filepath.Join(dir, "..", "Resources", "umcode"), // inside the app bundle
			filepath.Join(dir, "umcode"),                    // a plain build next to it
		} {
			cand = filepath.Clean(cand)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() && !sameFile(cand, exe) {
				return cand, nil
			}
		}
	}
	if p, err := exec.LookPath("umcode"); err == nil {
		return p, nil
	}
	return "", errors.New("the umcode engine binary was not found in the app bundle or on PATH")
}

// sameFile guards against a case-insensitive file system handing us the app
// binary when we asked for the engine.
func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

// Ensure makes sure an engine is running. Order: already running → the
// registered background service → register the service (macOS 13+, bundled
// app) → run it as a child process of the app.
func (m *EngineManager) Ensure(ctx context.Context) error {
	// The initial webview request can arrive while the app's eager startup is
	// still in progress. Serialize both paths so they cannot launch two engines.
	m.ensureMu.Lock()
	defer m.ensureMu.Unlock()
	if m.Reachable() {
		if st := serviceStatus(); st == serviceEnabled {
			if BuildID != "" {
				engine, err := m.engineStatus(ctx)
				if err == nil && engine.BuildID != BuildID {
					if err := m.refreshRegisteredService(ctx, engine); err != nil {
						m.setMode(ModeService, err.Error())
						return err
					}
				}
			}
			m.setMode(ModeService, "")
		} else {
			m.setMode(ModeExternal, "")
		}
		return nil
	}
	if os.Getenv("UMCODE_NO_SERVICE") != "1" {
		switch serviceStatus() {
		case serviceEnabled:
			// launchd should start it; give it a moment (it may be restarting).
			if m.waitReachable(ctx, 8*time.Second) {
				m.setMode(ModeService, "")
				return nil
			}
			m.log.Warn("background service is registered but the engine is not answering; starting it from the app")
		case serviceNotRegistered:
			if err := registerService(); err == nil {
				if m.waitReachable(ctx, 10*time.Second) {
					m.setMode(ModeService, "")
					return nil
				}
				m.log.Warn("background service registered but did not start in time")
			} else if !errors.Is(err, errServiceUnsupported) {
				m.log.Warn("could not register the background service", "err", err)
				m.setMode(ModeStopped, err.Error())
			}
		case serviceRequiresApproval:
			m.log.Info("background service needs approval in System Settings → General → Login Items")
		}
	}
	return m.startChild(ctx)
}

func (m *EngineManager) engineStatus(ctx context.Context) (protocol.EngineStatus, error) {
	ep, err := loadEndpoints()
	if err != nil {
		return protocol.EngineStatus{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := client.Dial(cctx, ep.Socket, "umcode-app", false)
	if err != nil {
		return protocol.EngineStatus{}, err
	}
	defer c.Close()
	var status protocol.EngineStatus
	if err := c.Call(cctx, protocol.MethodEngineStatus, nil, &status); err != nil {
		return protocol.EngineStatus{}, err
	}
	return status, nil
}

// refreshRegisteredService replaces a stale bundled engine with the binary in
// the app bundle that was just launched. SMAppService keeps the original bundle
// executable registered across app updates, so reachability alone is not enough.
func (m *EngineManager) refreshRegisteredService(ctx context.Context, old protocol.EngineStatus) error {
	if old.ActiveTurns > 0 || old.PendingApprovals > 0 {
		return fmt.Errorf("the background engine is an older build and has active work; let it finish, then relaunch this app to update it")
	}
	if err := unregisterService(); err != nil {
		return fmt.Errorf("unregister the older background engine: %w", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for m.Reachable() && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if m.Reachable() {
		return errors.New("the older background engine did not stop; quit UMCode and try again")
	}
	if err := registerService(); err != nil {
		return fmt.Errorf("register the engine from this app build: %w", err)
	}
	if !m.waitReachable(ctx, 15*time.Second) {
		return errors.New("the updated background engine did not start; check macOS Login Items settings")
	}
	updated, err := m.engineStatus(ctx)
	if err != nil {
		return fmt.Errorf("check the updated background engine: %w", err)
	}
	if updated.BuildID != BuildID {
		return fmt.Errorf("the background engine is still an older build (%s); quit UMCode and relaunch this app bundle", updated.BuildID)
	}
	return nil
}

func (m *EngineManager) waitReachable(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if m.Reachable() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return m.Reachable()
}

func (m *EngineManager) startChild(ctx context.Context) error {
	bin, err := EngineBinary()
	if err != nil {
		m.setMode(ModeStopped, err.Error())
		return err
	}
	home, err := appHome()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	childLogPath := filepath.Join(logDir, "engine-child.log")
	logf, err := os.OpenFile(childLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "engine")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Dir = home
	// Own process group so a terminal Ctrl-C on the app does not hit it twice,
	// and so Shutdown can stop the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logf.Close()
		m.setMode(ModeStopped, err.Error())
		return fmt.Errorf("start %s engine: %w", bin, err)
	}
	exited := make(chan struct{})
	m.mu.Lock()
	m.child, m.exited = cmd, exited
	m.mu.Unlock()
	go func() {
		err := cmd.Wait()
		logf.Close()
		close(exited)
		m.mu.Lock()
		stillOurs := m.child == cmd
		if stillOurs {
			m.child = nil
			m.mode = ModeStopped
			if err != nil {
				m.detail = "engine exited: " + err.Error()
			} else {
				m.detail = "engine exited"
			}
		}
		m.mu.Unlock()
		if stillOurs {
			m.log.Warn("engine process exited", "err", err)
		}
	}()
	m.setMode(ModeChild, bin)
	if !m.waitReachable(ctx, 15*time.Second) {
		select {
		case <-exited:
			return fmt.Errorf("the engine exited during startup; see %s", childLogPath)
		default:
		}
		return errors.New("the engine did not start listening in time")
	}
	m.log.Info("engine started as a child process", "bin", bin, "pid", cmd.Process.Pid)
	return nil
}

// stopChild terminates the engine this app started, if any.
func (m *EngineManager) stopChild() {
	m.mu.Lock()
	cmd, exited := m.child, m.exited
	m.child = nil
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(8 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
	}
}

// Restart restarts the engine in its current mode.
func (m *EngineManager) Restart(ctx context.Context) error {
	mode, _ := m.Mode()
	switch mode {
	case ModeChild:
		m.stopChild()
		return m.startChild(ctx)
	case ModeService:
		if err := restartService(); err != nil {
			return err
		}
		if !m.waitReachable(ctx, 15*time.Second) {
			return errors.New("the background service did not come back in time")
		}
		return nil
	case ModeExternal:
		return errors.New("the engine was started outside the app (for example from a terminal); restart it there")
	default:
		return m.Ensure(ctx)
	}
}

// InstallService switches to the background service.
func (m *EngineManager) InstallService(ctx context.Context) error {
	m.stopChild()
	if err := registerService(); err != nil {
		if errors.Is(err, errServiceUnsupported) {
			// Fall back to a plain LaunchAgent via the CLI.
			bin, berr := EngineBinary()
			if berr != nil {
				return berr
			}
			out, cerr := exec.CommandContext(ctx, bin, "service", "install").CombinedOutput()
			if cerr != nil {
				return fmt.Errorf("umcode service install: %v: %s", cerr, strings.TrimSpace(string(out)))
			}
		} else {
			_ = m.startChild(ctx)
			return err
		}
	}
	if serviceStatus() == serviceRequiresApproval {
		_ = m.startChild(ctx)
		return errors.New("allow umcode in System Settings → General → Login Items, then try again")
	}
	if !m.waitReachable(ctx, 15*time.Second) {
		_ = m.startChild(ctx)
		return errors.New("the background service did not start; running the engine from the app instead")
	}
	m.setMode(ModeService, "")
	return nil
}

// UninstallService stops the background service and runs the engine from the app.
func (m *EngineManager) UninstallService(ctx context.Context) error {
	if err := unregisterService(); err != nil && !errors.Is(err, errServiceUnsupported) {
		return err
	}
	// Wait for launchd to stop it before starting our own.
	deadline := time.Now().Add(8 * time.Second)
	for m.Reachable() && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	return m.startChild(ctx)
}

// Shutdown stops a child engine when the app quits. A background service keeps running.
func (m *EngineManager) Shutdown() { m.stopChild() }
