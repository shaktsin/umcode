package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
)

func execHarness(t *testing.T) (context.Context, *ExecManager, Registry2) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exec sessions need /bin/sh")
	}
	cfg := config.Default(t.TempDir())
	cfg.Tools.ShellEnabled = true
	cfg.Tools.HostSandbox = "off"
	m := NewExecManager()
	t.Cleanup(m.Close)
	r := NewRegistry()
	RegisterBuiltins(r, cfg, NewWorkspaces(cfg), nil, BuiltinServices{Exec: m})
	ctx := WithScope(context.Background(), &Scope{ThreadID: "thr_1", ProjectID: "prj_1", ProjectName: "demo", Root: t.TempDir(), AllowShell: true, AllowNet: true})
	return ctx, m, Registry2{r}
}

// Registry2 wraps a registry with a JSON-call helper for tests.
type Registry2 struct{ *Registry }

func (r Registry2) call(t *testing.T, ctx context.Context, name string, args map[string]any) string {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	b, _ := json.Marshal(args)
	out, err := tool.Call(ctx, b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func TestExecSessionInteractive(t *testing.T) {
	ctx, m, r := execHarness(t)
	out := r.call(t, ctx, "exec__start", map[string]any{"command": "read name; echo hello-$name; sleep 30", "yield_seconds": 1})
	if !strings.Contains(out, "status: running") {
		t.Fatalf("start: %s", out)
	}
	id := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(out, "session_id: "), "\n", 2)[0])
	out = r.call(t, ctx, "exec__write", map[string]any{"session_id": id, "input": "bob\n", "yield_seconds": 5})
	if !strings.Contains(out, "hello-bob") || !strings.Contains(out, "status: running") {
		t.Fatalf("write: %s", out)
	}
	// Output is returned once: a poll right after has nothing new.
	out = r.call(t, ctx, "exec__write", map[string]any{"session_id": id, "yield_seconds": 1})
	if strings.Contains(out, "hello-bob") {
		t.Fatalf("output repeated: %s", out)
	}
	out = r.call(t, ctx, "exec__stop", map[string]any{"session_id": id})
	if !strings.Contains(out, "exited") || m.Count() != 0 {
		t.Fatalf("stop: %s (count %d)", out, m.Count())
	}
}

func TestExecSessionExitReported(t *testing.T) {
	ctx, m, r := execHarness(t)
	out := r.call(t, ctx, "exec__start", map[string]any{"command": "echo done; exit 3", "yield_seconds": 5})
	if !strings.Contains(out, "exit_code 3") || !strings.Contains(out, "done") {
		t.Fatalf("start: %s", out)
	}
	if m.Count() != 1 {
		t.Fatalf("finished session should stay until read: %d", m.Count())
	}
}

func TestExecSessionIsolationAndLimits(t *testing.T) {
	ctx, m, r := execHarness(t)
	out := r.call(t, ctx, "exec__start", map[string]any{"command": "sleep 30", "yield_seconds": 1})
	id := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(out, "session_id: "), "\n", 2)[0])
	other := WithScope(context.Background(), &Scope{ThreadID: "thr_other", Root: t.TempDir(), AllowShell: true})
	tool, _ := r.Get("exec__write")
	if _, err := tool.Call(other, json.RawMessage(`{"session_id":"`+id+`"}`)); err == nil {
		t.Fatal("another chat reached the session")
	}
	for i := 1; i < execMaxPerThread; i++ {
		r.call(t, ctx, "exec__start", map[string]any{"command": "sleep 30", "yield_seconds": 1})
	}
	start, _ := r.Get("exec__start")
	if _, err := start.Call(ctx, json.RawMessage(`{"command":"sleep 30","yield_seconds":1}`)); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("cap not enforced: %v", err)
	}
	m.Close()
	if m.Count() != 0 {
		t.Fatal("Close left sessions")
	}
}

func TestExecIdleReaper(t *testing.T) {
	ctx, m, r := execHarness(t)
	m.idle = 10 * time.Millisecond
	r.call(t, ctx, "exec__start", map[string]any{"command": "sleep 30", "yield_seconds": 1})
	time.Sleep(30 * time.Millisecond)
	m.reapIdle(time.Now())
	if m.Count() != 0 {
		t.Fatal("idle session not reaped")
	}
}

func TestExecRingKeepsTail(t *testing.T) {
	s := &execSession{notify: make(chan struct{}, 1)}
	_, _ = s.Write([]byte(strings.Repeat("a", execRingBytes)))
	_, _ = s.Write([]byte("TAIL"))
	out, lost := s.unread()
	if lost != 4 || !strings.HasSuffix(out, "TAIL") || len(out) != execRingBytes {
		t.Fatalf("lost=%d len=%d", lost, len(out))
	}
}

func TestExecStartUsesShellPolicy(t *testing.T) {
	_, _, r := execHarness(t)
	tool, _ := r.Get("exec__start")
	g, ok := tool.(Guard)
	if !ok {
		t.Fatal("exec.start is not a Guard")
	}
	if _, bad := g.Forbidden(json.RawMessage(`{"command":"sudo reboot"}`)); !bad {
		t.Fatal("sudo allowed")
	}
	if risk, _ := tool.Assess(json.RawMessage(`{"command":"npm run dev"}`)); risk != RiskRed {
		t.Fatalf("risk = %s", risk)
	}
}
