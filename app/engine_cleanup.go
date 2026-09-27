package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procInfo is one row of `ps`.
type procInfo struct {
	PID     int
	Command string // full command line
}

// parsePS parses `ps -axo pid=,command=` output.
func parsePS(out string) []procInfo {
	var procs []procInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidStr, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		procs = append(procs, procInfo{PID: pid, Command: strings.TrimSpace(rest)})
	}
	return procs
}

// isEngineCommand reports whether a command line is a UMCode engine:
// an executable named "umcode" run with the "engine" subcommand.
func isEngineCommand(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) < 2 {
		return false
	}
	return filepath.Base(f[0]) == "umcode" && f[1] == "engine"
}

// findEngines lists running UMCode engine processes other than this app.
func findEngines(procs []procInfo, self int) []int {
	var pids []int
	for _, p := range procs {
		if p.PID != self && isEngineCommand(p.Command) {
			pids = append(pids, p.PID)
		}
	}
	return pids
}

func listProcesses() ([]procInfo, error) {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	return parsePS(string(out)), nil
}

// portHolder describes what listens on a TCP port ("" when it is free or
// unknown), using lsof.
func portHolder(port int) (pid int, name string) {
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil {
		return 0, ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "c") && pid != 0:
			return pid, line[1:]
		}
	}
	return 0, ""
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// terminate asks a process to stop, then kills it if it lingers.
func terminate(pid int, grace time.Duration) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
}

// stopRunningEngines stops every UMCode engine that is not a child of this app
// (a previous app run, a terminal `umcode engine`, an old build), unregisters
// nothing, and removes a stale socket. When the background service manages
// the engine it is stopped through launchd first so it is not respawned into
// the way. It returns how many processes were stopped.
func (m *EngineManager) stopRunningEngines(ctx context.Context) int {
	_ = ctx
	m.mu.Lock()
	var own int
	if m.child != nil && m.child.Process != nil {
		own = m.child.Process.Pid
	}
	m.mu.Unlock()

	procs, err := listProcesses()
	if err != nil {
		m.log.Warn("could not list processes", "err", err)
		return 0
	}
	stopped := 0
	for _, pid := range findEngines(procs, os.Getpid()) {
		if pid == own {
			continue
		}
		m.log.Info("stopping an existing engine", "pid", pid)
		terminate(pid, 4*time.Second)
		stopped++
	}
	if ep, err := loadEndpoints(); err == nil && ep.Socket != "" && !m.Reachable() {
		_ = os.Remove(ep.Socket) // left behind by a crashed engine
	}
	return stopped
}

// describePortConflict explains who is holding the engine's WebSocket port
// when it is not one of our own engines.
func describePortConflict(port int) string {
	pid, name := portHolder(port)
	if pid == 0 {
		return ""
	}
	return fmt.Sprintf("port %d is in use by %s (pid %d); quit it or set runtime.engine_ws_port in ~/.umcode/config.yaml", port, name, pid)
}
