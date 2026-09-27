package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"encoding/json"

	"github.com/shaktsin/umcode/internal/procutil"
	"github.com/shaktsin/umcode/internal/sandbox"
)

// Exec sessions let the model run a long-lived or interactive command (a dev
// server, a REPL, an installer that prompts) across several tool calls:
// exec.start launches it and returns the first output, exec.write sends input
// or polls for more output, exec.stop ends it. Sessions belong to a chat and
// are killed when they idle out, when the chat's cap is exceeded, or when the
// engine shuts down. They use the same sandbox and approval rules as shell.run.

const (
	execRingBytes      = 256 << 10
	execIdleTimeout    = 30 * time.Minute
	execMaxPerThread   = 8
	execDefaultYield   = 10 * time.Second
	execMaxYield       = 60 * time.Second
	execQuietAfterData = 400 * time.Millisecond
)

// ExecManager owns the running exec sessions.
type ExecManager struct {
	mu       sync.Mutex
	sessions map[string]*execSession
	base     context.Context
	stop     context.CancelFunc
	idle     time.Duration
	closed   bool
}

// NewExecManager returns a manager and starts its idle reaper.
func NewExecManager() *ExecManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &ExecManager{sessions: map[string]*execSession{}, base: ctx, stop: cancel, idle: execIdleTimeout}
	go m.reap()
	return m
}

func (m *ExecManager) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.base.Done():
			return
		case <-t.C:
			m.reapIdle(time.Now())
		}
	}
}

func (m *ExecManager) reapIdle(now time.Time) {
	m.mu.Lock()
	var victims []*execSession
	for id, s := range m.sessions {
		if now.Sub(s.lastUsed()) > m.idle {
			victims = append(victims, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range victims {
		s.kill()
	}
}

// Close kills every session.
func (m *ExecManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	all := m.sessions
	m.sessions = map[string]*execSession{}
	m.mu.Unlock()
	m.stop()
	for _, s := range all {
		s.kill()
	}
}

// Count returns the number of live sessions (running or finished but unread).
func (m *ExecManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *ExecManager) add(threadID string, s *execSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("exec sessions are shut down")
	}
	var mine []*execSession
	for _, o := range m.sessions {
		if o.thread == threadID {
			mine = append(mine, o)
		}
	}
	if len(mine) >= execMaxPerThread {
		// Make room by dropping the least recently used finished session.
		sort.Slice(mine, func(i, j int) bool { return mine[i].lastUsed().Before(mine[j].lastUsed()) })
		var dropped bool
		for _, o := range mine {
			if o.finished() {
				delete(m.sessions, o.id)
				dropped = true
				break
			}
		}
		if !dropped {
			return fmt.Errorf("this chat already has %d running sessions; stop one with exec.stop first", execMaxPerThread)
		}
	}
	m.sessions[s.id] = s
	return nil
}

func (m *ExecManager) get(id, threadID string) (*execSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.thread != threadID {
		return nil, fmt.Errorf("no exec session %q in this chat (it may have ended or idled out)", id)
	}
	return s, nil
}

func (m *ExecManager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

type execSession struct {
	id, thread, command string
	cancel              context.CancelFunc
	stdin               io.WriteCloser
	done                chan struct{}
	notify              chan struct{}

	mu       sync.Mutex
	buf      []byte // last execRingBytes of output
	total    int64  // bytes ever written
	consumed int64  // bytes already returned to the model
	exit     int
	used     time.Time
}

// Write implements io.Writer for the process's combined output.
func (s *execSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf = append(s.buf, p...)
	if extra := len(s.buf) - execRingBytes; extra > 0 {
		s.buf = append(s.buf[:0], s.buf[extra:]...)
	}
	s.total += int64(len(p))
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (s *execSession) touch() {
	s.mu.Lock()
	s.used = time.Now()
	s.mu.Unlock()
}

func (s *execSession) lastUsed() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

func (s *execSession) finished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *execSession) kill() {
	s.cancel()
	_ = s.stdin.Close()
}

// unread returns output not yet returned, and how many bytes were lost to the
// ring buffer since the last read.
func (s *execSession) unread() (out string, lost int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.total - int64(len(s.buf)) // absolute offset of buf[0]
	from := s.consumed
	if from < start {
		lost = start - from
		from = start
	}
	out = string(s.buf[from-start:])
	s.consumed = s.total
	return out, lost
}

// wait blocks until the process exits, the yield time passes, or output has
// arrived and gone quiet.
func (s *execSession) wait(ctx context.Context, yield time.Duration) {
	deadline := time.NewTimer(yield)
	defer deadline.Stop()
	var quiet *time.Timer
	var quietC <-chan time.Time
	defer func() {
		if quiet != nil {
			quiet.Stop()
		}
	}()
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-quietC:
			return
		case <-s.notify:
			if quiet == nil {
				quiet = time.NewTimer(execQuietAfterData)
			} else {
				if !quiet.Stop() {
					select {
					case <-quiet.C:
					default:
					}
				}
				quiet.Reset(execQuietAfterData)
			}
			quietC = quiet.C
		}
	}
}

func (s *execSession) report() string {
	out, lost := s.unread()
	var b strings.Builder
	fmt.Fprintf(&b, "session_id: %s\n", s.id)
	if s.finished() {
		s.mu.Lock()
		code := s.exit
		s.mu.Unlock()
		fmt.Fprintf(&b, "status: exited (exit_code %d)\n", code)
	} else {
		b.WriteString("status: running (use exec.write to send input or poll, exec.stop to end it)\n")
	}
	if lost > 0 {
		fmt.Fprintf(&b, "[%d bytes of earlier output were dropped]\n", lost)
	}
	b.WriteString("--- output ---\n")
	b.WriteString(out)
	return clip(b.String())
}

func newExecID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "exec_" + hex.EncodeToString(b[:])
}

func yieldFrom(seconds int) time.Duration {
	d := time.Duration(seconds) * time.Second
	if d <= 0 {
		d = execDefaultYield
	}
	if d > execMaxYield {
		d = execMaxYield
	}
	return d
}

// ---- tools -----------------------------------------------------------------

type execStart struct {
	shell   *shellRun
	manager *ExecManager
}

func (*execStart) Name() string { return "exec.start" }
func (*execStart) Description() string {
	return "Start a long-running or interactive command (dev server, watcher, REPL, prompting installer) that keeps running between tool calls. Returns a session_id and the first output. Then use exec.write to send input or poll for new output, and exec.stop to end it. For commands that simply finish, use shell.run. Sandboxed like shell.run."
}
func (*execStart) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"command":{"type":"string"},"workspace":{"type":"string","description":"Folder inside the project to run in; default the project root"},"yield_seconds":{"type":"integer","description":"How long to wait for first output. Default 10, max 60"}},"required":["command"]}`)
}
func (t *execStart) Assess(args json.RawMessage) (Risk, string) {
	risk, summary := t.shell.Assess(args)
	return risk, strings.Replace(summary, "Run", "Start session", 1)
}
func (t *execStart) Forbidden(args json.RawMessage) (string, bool) { return t.shell.Forbidden(args) }

func (t *execStart) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		Command      string
		Workspace    string
		YieldSeconds int `json:"yield_seconds"`
	}](args)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", errors.New("command is empty")
	}
	scope := ScopeFrom(ctx)
	if scope == nil {
		return "", ErrNoProject
	}
	if !scope.AllowShell {
		return "", fmt.Errorf("shell commands are switched off for the project %s; do not try to run commands through Computer Use as a workaround — tell the user that Shell needs to be turned on in this project's settings (or in the chat's approval controls) before commands can run", scope.ProjectName)
	}
	if scope.UseCompute {
		return "", errors.New("exec sessions run on the host and are not available while the project uses the isolated microVM; use shell.run")
	}
	sandboxed := t.shell.sandbox != nil
	if !scope.AllowNet && !sandboxed {
		return "", errors.New("exec sessions need the host sandbox or network access enabled for the project")
	}
	dir := scope.Root
	if a.Workspace != "" {
		d, err := scope.Resolve(a.Workspace)
		if err != nil {
			return "", err
		}
		dir = d
	}
	sctx, cancel := context.WithCancel(t.manager.base)
	cmd := exec.CommandContext(sctx, "/bin/sh", "-c", a.Command)
	procutil.Prepare(cmd)
	cmd.Dir = dir
	cmd.Env = safeEnv()
	s := &execSession{
		id: newExecID(), thread: scope.ThreadID, command: a.Command, cancel: cancel,
		done: make(chan struct{}), notify: make(chan struct{}, 1), used: time.Now(),
	}
	cmd.Stdout, cmd.Stderr = s, s
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return "", err
	}
	s.stdin = stdin
	if sandboxed {
		if err := t.shell.sandbox.Wrap(cmd, sandbox.DefaultPolicy(scope.Root, scope.AllowNet)); err != nil {
			cancel()
			return "", fmt.Errorf("could not start the command sandbox: %w", err)
		}
	}
	if err := t.manager.add(scope.ThreadID, s); err != nil {
		cancel()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		t.manager.remove(s.id)
		cancel()
		return "", err
	}
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			code = -1
		}
		s.mu.Lock()
		s.exit = code
		s.mu.Unlock()
		close(s.done)
		cancel()
	}()
	s.wait(ctx, yieldFrom(a.YieldSeconds))
	s.touch()
	return s.report(), nil
}

type execWrite struct{ manager *ExecManager }

func (*execWrite) Name() string { return "exec.write" }
func (*execWrite) Description() string {
	return "Send input (stdin) to a running exec session and return the new output. Include a trailing newline to submit a line. With empty input it just polls for new output."
}
func (*execWrite) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"session_id":{"type":"string"},"input":{"type":"string","description":"Text to send to stdin; empty to only poll"},"yield_seconds":{"type":"integer","description":"How long to wait for output. Default 10, max 60"}},"required":["session_id"]}`)
}
func (*execWrite) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct {
		SessionID string `json:"session_id"`
		Input     string
	}](args)
	if a.Input == "" {
		return RiskGreen, "Read output of session " + a.SessionID
	}
	return RiskYellow, "Send input to session " + a.SessionID
}
func (t *execWrite) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		SessionID    string `json:"session_id"`
		Input        string
		YieldSeconds int `json:"yield_seconds"`
	}](args)
	if err != nil {
		return "", err
	}
	scope := ScopeFrom(ctx)
	if scope == nil {
		return "", ErrNoProject
	}
	s, err := t.manager.get(a.SessionID, scope.ThreadID)
	if err != nil {
		return "", err
	}
	s.touch()
	if a.Input != "" {
		if s.finished() {
			return s.report(), nil
		}
		if _, err := io.WriteString(s.stdin, a.Input); err != nil {
			if !s.finished() {
				return "", fmt.Errorf("could not write to the session: %w", err)
			}
		}
	}
	s.wait(ctx, yieldFrom(a.YieldSeconds))
	s.touch()
	out := s.report()
	if s.finished() {
		t.manager.remove(s.id)
	}
	return out, nil
}

type execStop struct{ manager *ExecManager }

func (*execStop) Name() string { return "exec.stop" }
func (*execStop) Description() string {
	return "Stop an exec session and return any output it had not yet reported."
}
func (*execStop) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`)
}
func (*execStop) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct {
		SessionID string `json:"session_id"`
	}](args)
	return RiskGreen, "Stop session " + a.SessionID
}
func (t *execStop) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		SessionID string `json:"session_id"`
	}](args)
	if err != nil {
		return "", err
	}
	scope := ScopeFrom(ctx)
	if scope == nil {
		return "", ErrNoProject
	}
	s, err := t.manager.get(a.SessionID, scope.ThreadID)
	if err != nil {
		return "", err
	}
	s.kill()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
	t.manager.remove(s.id)
	return s.report(), nil
}
