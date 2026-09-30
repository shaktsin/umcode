package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/compute"
	"github.com/shaktsin/umcode/internal/computeruse"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/sandbox"
)

func TestWorkspaceACLAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "s.txt"), []byte("secret"), 0o600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	f := false
	cfg := config.Default(t.TempDir())
	cfg.Tools.Workspaces = []config.WorkspaceConfig{{Name: "ro", Path: root, ACL: config.WorkspaceACL{Write: &f, CreateFiles: &f}}}
	ws, _ := NewWorkspaces(cfg).Get("")
	if _, err := ws.Resolve("escape/s.txt", OpRead); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape allowed: %v", err)
	}
	if _, err := ws.Resolve("../x", OpRead); err == nil {
		t.Fatal("dotdot escape allowed")
	}
	if _, err := ws.Resolve("new.txt", OpCreate); err == nil || !strings.Contains(err.Error(), "does not allow create") {
		t.Fatalf("ACL not enforced: %v", err)
	}
	if _, err := ws.Resolve("sub/new.txt", OpRead); err != nil {
		t.Fatalf("read inside denied: %v", err)
	}
}

func TestShellRunAndRisk(t *testing.T) {
	cfg := config.Default(t.TempDir())
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	r := NewRegistry()
	RegisterBuiltins(r, cfg, NewWorkspaces(cfg), nil)
	project := t.TempDir()
	ctx := WithScope(context.Background(), &Scope{ProjectID: "prj_1", ProjectName: "demo", Root: project, AllowNet: true})
	sh, ok := r.Get("shell__run")
	if !ok {
		t.Fatal("shell.run not registered")
	}
	for cmd, want := range map[string]Risk{"ls -la": RiskGreen, "git status": RiskGreen, "ls; rm -rf /": RiskRed, "rm x": RiskRed} {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		if got, _ := sh.Assess(args); got != want {
			t.Errorf("%q risk = %s, want %s", cmd, got, want)
		}
	}
	// Without a project there is no place to run.
	if _, err := sh.Call(context.Background(), json.RawMessage(`{"command":"ls"}`)); !errors.Is(err, ErrNoProject) {
		t.Fatalf("shell without a project: %v", err)
	}
	out, err := sh.Call(ctx, json.RawMessage(`{"command":"echo $OPENAI_API_KEY; pwd; exit 3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "must-not-leak") || !strings.Contains(out, "exit_code: 3") || !strings.Contains(out, project) {
		t.Fatalf("out = %s", out)
	}
	if _, err := sh.Call(ctx, json.RawMessage(`{"command":"sleep 5","timeout_seconds":1}`)); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestDiscoveredToolPathIncludesPerUserNodeWithoutLoadingShellConfig(t *testing.T) {
	home := t.TempDir()
	nodeBin := filepath.Join(home, ".nvm", "versions", "node", "v24.3.0", "bin")
	if err := os.MkdirAll(nodeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	path := discoveredToolPath("/usr/bin", home)
	parts := filepath.SplitList(path)
	found := false
	for _, part := range parts {
		if part == nodeBin {
			found = true
		}
	}
	if !found {
		t.Fatalf("per-user Node bin missing from PATH %q", path)
	}
	if got := strings.Count(path, "/usr/bin"); got != 1 {
		t.Fatalf("existing PATH entry should remain once, got %q", path)
	}
}

func TestShellStreamsOutputBeforeCommandExits(t *testing.T) {
	cfg := config.Default(t.TempDir())
	r := NewRegistry()
	RegisterBuiltins(r, cfg, NewWorkspaces(cfg), nil)
	sh, _ := r.Get("shell__run")
	progress := make(chan string, 4)
	ctx := WithScope(context.Background(), &Scope{ProjectName: "demo", Root: t.TempDir(), AllowNet: true,
		Progress: func(s string) { progress <- s }})
	done := make(chan error, 1)
	go func() {
		_, err := sh.Call(ctx, json.RawMessage(`{"command":"printf first; sleep 1; printf second"}`))
		done <- err
	}()
	select {
	case chunk := <-progress:
		if !strings.Contains(chunk, "first") {
			t.Fatalf("first streamed chunk = %q", chunk)
		}
	case err := <-done:
		t.Fatalf("shell returned before streaming output: %v", err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("command output was not streamed before exit")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type fakeComputeRunner struct{ request compute.Request }

func (f *fakeComputeRunner) Run(_ context.Context, req compute.Request) (int, error) {
	f.request = req
	_, _ = req.Stdout.Write([]byte("offline guest output"))
	return 0, nil
}

func TestNetworkOffRequiresMicroVMAndPassesDenySetting(t *testing.T) {
	root := t.TempDir()
	args := json.RawMessage(`{"command":"echo safe"}`)
	host := &shellRun{}
	ctx := WithScope(context.Background(), &Scope{ProjectName: "demo", Root: root})
	if _, err := host.Call(ctx, args); err == nil || !strings.Contains(err.Error(), "microVM") {
		t.Fatalf("host shell should fail closed with network off: %v", err)
	}
	fake := &fakeComputeRunner{}
	vm := &shellRun{compute: fake}
	ctx = WithScope(context.Background(), &Scope{ProjectName: "demo", Root: root, UseCompute: true})
	output, err := vm.Call(ctx, args)
	if err != nil || fake.request.Network || !strings.Contains(output, "offline guest output") {
		t.Fatalf("microVM offline call = %q, %+v, %v", output, fake.request, err)
	}
}

type verificationCompute struct{ commands []string }

func (f *verificationCompute) Run(_ context.Context, req compute.Request) (int, error) {
	f.commands = append(f.commands, req.Command)
	if strings.Contains(req.Command, "fail") {
		_, _ = req.Stderr.Write([]byte("check failed"))
		return 2, nil
	}
	_, _ = req.Stdout.Write([]byte("check passed"))
	return 0, nil
}

func TestVerificationRunReportsEvidenceAndStopsOnFailure(t *testing.T) {
	fake := &verificationCompute{}
	tool := &verificationRun{shell: &shellRun{compute: fake}}
	ctx := WithScope(context.Background(), &Scope{
		ProjectID: "prj_1", ProjectName: "demo", Root: t.TempDir(), AllowNet: true, UseCompute: true,
	})
	out, err := tool.Call(ctx, json.RawMessage(`{"checks":[{"label":"typecheck","command":"check"},{"label":"tests","command":"fail"},{"label":"build","command":"build"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.commands, ",") != "check,fail" || !strings.Contains(out, `"status":"failed"`) || !strings.Contains(out, `"exit_code":2`) {
		t.Fatalf("verification output = %s; commands = %v", out, fake.commands)
	}
}

func TestVerificationPlanUsesConfiguredScriptsAndReasons(t *testing.T) {
	root := t.TempDir()
	pkg := `{"scripts":{"check":"svelte-check","test":"vitest run","build":"vite build","test:e2e":"playwright test"}}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	checks, manifests := planChecks(root, []string{"src/App.svelte"})
	if len(manifests) != 1 || manifests[0] != "package.json" || len(checks) != 4 {
		t.Fatalf("manifests=%v checks=%+v", manifests, checks)
	}
	for _, check := range checks {
		if check.Reason == "" || check.RequiredCommand != "npm" {
			t.Fatalf("check lacks review metadata: %+v", check)
		}
	}
	if checks[3].Kind != "browser" || checks[3].Command != "npm run test:e2e" {
		t.Fatalf("browser check = %+v", checks[3])
	}
}

func TestFrontendChangedRecognizesVisualSources(t *testing.T) {
	for _, files := range [][]string{{"src/App.svelte"}, {"web/components/Button.ts"}, {"pages/index.jsx"}} {
		if !frontendChanged(files) {
			t.Fatalf("frontendChanged(%v) = false", files)
		}
	}
	if frontendChanged([]string{"internal/store/db.go", "README.md"}) {
		t.Fatal("backend and documentation changes unexpectedly require Visual QA")
	}
}

type missingCommandCompute struct{}

func (*missingCommandCompute) Run(_ context.Context, req compute.Request) (int, error) {
	if strings.HasPrefix(req.Command, "command -v ") {
		return 127, nil
	}
	return 0, nil
}

func TestVerificationReportsMissingPrerequisiteAsNotRun(t *testing.T) {
	tool := &verificationRun{shell: &shellRun{compute: &missingCommandCompute{}}}
	ctx := WithScope(context.Background(), &Scope{ProjectID: "prj_1", ProjectName: "demo", Root: t.TempDir(), AllowNet: true, UseCompute: true})
	out, err := tool.Call(ctx, json.RawMessage(`{"checks":[{"label":"browser","command":"pnpm test","required_command":"pnpm"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"not_run"`) || !strings.Contains(out, `required command is unavailable: pnpm`) {
		t.Fatalf("verification output = %s", out)
	}
}

type browserCompute struct{}

func (*browserCompute) Run(_ context.Context, req compute.Request) (int, error) {
	if err := os.MkdirAll(filepath.Join(req.Dir, "test-results", "flow"), 0o755); err != nil {
		return -1, err
	}
	if err := os.WriteFile(filepath.Join(req.Dir, "test-results", "flow", "failure.png"), []byte("png"), 0o644); err != nil {
		return -1, err
	}
	if err := os.WriteFile(filepath.Join(req.Dir, "test-results", "flow", "trace.zip"), []byte("zip"), 0o644); err != nil {
		return -1, err
	}
	_, _ = req.Stdout.Write([]byte("console.error: broken widget\nrequestfailed: /api/items\n"))
	return 1, nil
}

func TestBrowserVerifyCapturesDiagnosticsAndArtifacts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", ".bin", "playwright"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := &browserVerify{shell: &shellRun{compute: &browserCompute{}}}
	ctx := WithScope(context.Background(), &Scope{ProjectID: "prj_1", ProjectName: "demo", Root: root, AllowNet: true, UseCompute: true})
	out, err := tool.Call(ctx, json.RawMessage(`{"framework":"playwright","command":"npm run test:e2e"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"failed"`) || !strings.Contains(out, `console.error`) ||
		!strings.Contains(out, `failure.png`) || !strings.Contains(out, `trace.zip`) {
		t.Fatalf("browser result = %s", out)
	}
}

func TestBrowserVerifyReportsMissingDependencyAsNotRun(t *testing.T) {
	root := t.TempDir()
	tool := &browserVerify{shell: &shellRun{compute: &browserCompute{}}}
	ctx := WithScope(context.Background(), &Scope{ProjectID: "prj_1", ProjectName: "demo", Root: root, AllowNet: true, UseCompute: true})
	out, err := tool.Call(ctx, json.RawMessage(`{"framework":"playwright","command":"npm run test:e2e"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"not_run"`) || !strings.Contains(out, `dependencies were not installed automatically`) {
		t.Fatalf("browser result = %s", out)
	}
}

type missingBrowserRuntimeCompute struct{}

func (*missingBrowserRuntimeCompute) Run(_ context.Context, req compute.Request) (int, error) {
	_, _ = req.Stderr.Write([]byte("Executable doesn't exist. Run playwright install."))
	return 1, nil
}

func TestBrowserVerifyReportsMissingRuntimeAsNotRun(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", ".bin", "playwright"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := &browserVerify{shell: &shellRun{compute: &missingBrowserRuntimeCompute{}}}
	ctx := WithScope(context.Background(), &Scope{ProjectID: "prj_1", ProjectName: "demo", Root: root, AllowNet: true, UseCompute: true})
	out, err := tool.Call(ctx, json.RawMessage(`{"framework":"playwright","command":"npm run test:e2e"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"not_run"`) || !strings.Contains(out, `compatible browser executable is unavailable`) {
		t.Fatalf("browser result = %s", out)
	}
}

func TestProjectScope(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\ntwo\n"), 0o644)

	var changed []string
	scope := &Scope{ProjectID: "prj_1", ProjectName: "demo", Root: root, Record: func(_ context.Context, abs string, before *string, deleted bool) {
		kind := "modified"
		if before == nil {
			kind = "created"
		}
		changed = append(changed, kind+" "+filepath.Base(abs))
	}}
	ctx := WithScope(context.Background(), scope)
	cfg := config.Default(t.TempDir())
	cfg.Tools.HostSandbox = "off" // exercise the guard used when no sandbox is available
	r := NewRegistry()
	RegisterBuiltins(r, cfg, NewWorkspaces(cfg), nil)

	write, _ := r.Get("file__write")
	read, _ := r.Get("file__read")

	// Paths outside the project are refused, including through a symlink.
	for _, path := range []string{"../escape.txt", outside + "/secret.txt", "escape/secret.txt"} {
		args, _ := json.Marshal(map[string]string{"path": path, "content": "x"})
		if _, err := write.Call(ctx, args); err == nil || !strings.Contains(err.Error(), "outside the project") {
			t.Fatalf("write to %s: %v", path, err)
		}
		args, _ = json.Marshal(map[string]string{"path": path})
		if _, err := read.Call(ctx, args); err == nil {
			t.Fatalf("read of %s was allowed", path)
		}
	}

	// Writing inside records the change.
	args, _ := json.Marshal(map[string]string{"path": "notes.txt", "content": "one\ntwo\nthree\n"})
	if _, err := write.Call(ctx, args); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]string{"path": "sub/new.txt", "content": "hello"})
	if _, err := write.Call(ctx, args); err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, ", ") != "modified notes.txt, created new.txt" {
		t.Fatalf("recorded changes = %v", changed)
	}

	// Network commands are refused while the project has networking off.
	sh, _ := r.Get("shell__run")
	if _, err := sh.Call(ctx, json.RawMessage(`{"command":"curl https://example.com"}`)); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("network guard: %v", err)
	}
	scope.AllowNet = true
	if _, err := sh.Call(ctx, json.RawMessage(`{"command":"echo ok"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestComputerToolsAreFirstPartyAndClassifyApprovals(t *testing.T) {
	cfg := config.Default(t.TempDir())
	r := NewRegistry()
	RegisterBuiltins(r, cfg, NewWorkspaces(cfg), nil, BuiltinServices{ComputerUse: nil})
	if _, ok := r.Get("computer__act"); ok {
		t.Fatal("computer tools registered without a manager")
	}
	r = NewRegistry()
	RegisterBuiltins(r, cfg, NewWorkspaces(cfg), nil, BuiltinServices{ComputerUse: computeruse.NewManager(context.Background())})
	for _, name := range []string{"computer__list", "computer__start", "computer__inspect", "computer__act", "computer__stop"} {
		if _, ok := r.Get(name); !ok {
			t.Fatalf("%s was not registered", name)
		}
	}
	tool := &computerAct{}
	cases := []struct {
		args string
		want Risk
	}{
		{`{"action":"move","x":10,"y":20}`, RiskYellow},
		{`{"action":"scroll","delta":400}`, RiskYellow},
		{`{"action":"click","target_description":"Open settings"}`, RiskYellow},
		{`{"action":"fill","target_description":"Email address"}`, RiskYellow},
		{`{"action":"fill","target_description":"Password field"}`, RiskRed},
		{`{"action":"click","target_description":"Submit order"}`, RiskRed},
		{`{"action":"key","key":"Enter"}`, RiskRed},
	}
	for _, tc := range cases {
		if risk, _ := tool.Assess(json.RawMessage(tc.args)); risk != tc.want {
			t.Errorf("Assess(%s) = %s, want %s", tc.args, risk, tc.want)
		}
	}
	start := &computerStart{}
	if risk, _ := start.Assess(json.RawMessage(`{"app_name":"Google Chrome"}`)); risk != RiskRed {
		t.Fatalf("computer.start risk = %s, want red", risk)
	}
	ctx := WithScope(context.Background(), &Scope{ThreadID: "thread-1", Root: t.TempDir()})
	if _, err := requireComputerScope(ctx); err != nil {
		t.Fatalf("Computer Use should not require a project opt-in: %v", err)
	}
}

// wrapSandbox is a stand-in Sandbox that marks the command as sandboxed.
type wrapSandbox struct{ policies []sandboxPolicy }

type sandboxPolicy struct {
	root    string
	network bool
}

func (w *wrapSandbox) Name() string { return "fake" }
func (w *wrapSandbox) Wrap(cmd *exec.Cmd, p sandbox.Policy) error {
	w.policies = append(w.policies, sandboxPolicy{p.Root, p.Network})
	inner := append([]string{cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/usr/bin/env"
	cmd.Args = append([]string{"/usr/bin/env", "SANDBOXED=yes"}, inner...)
	return nil
}

func TestHostShellRunsInSandboxWhenNetworkIsOff(t *testing.T) {
	project := t.TempDir()
	sb := &wrapSandbox{}
	sh := &shellRun{sandbox: sb}
	ctx := WithScope(context.Background(), &Scope{ProjectID: "p", ProjectName: "demo", Root: project, AllowNet: false})
	args, _ := json.Marshal(map[string]string{"command": "echo $SANDBOXED"})
	out, err := sh.Call(ctx, args)
	if err != nil {
		t.Fatalf("sandboxed shell with network off was refused: %v", err)
	}
	if !strings.Contains(out, "yes") {
		t.Errorf("command did not run under the sandbox:\n%s", out)
	}
	if len(sb.policies) != 1 || sb.policies[0].network || filepath.Base(sb.policies[0].root) != filepath.Base(project) {
		t.Errorf("policy = %+v", sb.policies)
	}
	// Commands that reach for the network are not pre-empted: the sandbox decides.
	if _, err := sh.Call(ctx, json.RawMessage(`{"command":"echo curl"}`)); err != nil {
		t.Errorf("sandboxed command refused by the heuristic: %v", err)
	}
}

func TestShellRunRejectsMissingWorkspaceBeforeSandboxLaunch(t *testing.T) {
	project := t.TempDir()
	sb := &wrapSandbox{}
	sh := &shellRun{sandbox: sb}
	ctx := WithScope(context.Background(), &Scope{ProjectID: "p", ProjectName: "Demo app", Root: project})
	_, err := sh.Call(ctx, json.RawMessage(`{"command":"echo should-not-run","workspace":"Demo app"}`))
	if err == nil || !strings.Contains(err.Error(), `workspace "Demo app" does not exist`) || !strings.Contains(err.Error(), "omit workspace") {
		t.Fatalf("expected a clear missing-workspace error, got %v", err)
	}
	if len(sb.policies) != 0 {
		t.Fatalf("invalid workspace reached sandbox launch: %+v", sb.policies)
	}
}

func TestSandboxHint(t *testing.T) {
	if h := sandboxHint("touch: /etc/x: Operation not permitted", false); !strings.Contains(h, "sandbox") {
		t.Errorf("write hint = %q", h)
	}
	if h := sandboxHint("curl: (6) Could not resolve host: example.com", false); !strings.Contains(h, "network access is off") {
		t.Errorf("network hint = %q", h)
	}
	if h := sandboxHint("curl: (6) Could not resolve host: example.com", true); h != "" {
		t.Errorf("network hint with network on = %q", h)
	}
	if h := sandboxHint("all good", false); h != "" {
		t.Errorf("unexpected hint %q", h)
	}
}
