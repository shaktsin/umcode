package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/compute"
	"github.com/shaktsin/umcode/internal/computeruse"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/preview"
	"github.com/shaktsin/umcode/internal/procutil"
	"github.com/shaktsin/umcode/internal/sandbox"
	"github.com/shaktsin/umcode/internal/visualqa"
)

const maxOutput = 64 << 10

// SkillEnvFunc returns the environment for running commands as part of a skill.
type SkillEnvFunc func(ctx context.Context, skill string) ([]string, error)

// RegisterBuiltins adds available first-party tools. Risk and approval are
// evaluated per chat; project settings only choose the compute environment.
// skillEnv may be nil.
type BuiltinServices struct {
	Previews    *preview.Manager
	VisualQA    *visualqa.Manager
	ComputerUse *computeruse.Manager
	Exec        *ExecManager
}

func RegisterBuiltins(r *Registry, cfg *config.Config, ws *Workspaces, skillEnv SkillEnvFunc, services ...BuiltinServices) {
	r.Add(&fileRead{ws})
	r.Add(&fileList{ws})
	r.Add(&fileWrite{ws})
	r.Add(&fileEdit{ws})
	r.Add(&fileSearch{ws})
	r.Add(newWebSearch())
	r.Add(newWebFetch())
	// First-party tools are always available. Their assessed risk is handled by
	// the chat's approval mode before any action runs.
	shell := &shellRun{ws: ws, forbid: cfg.Policy.ShellForbidCommands, skillEnv: skillEnv, compute: compute.NewBundledRunner()}
	if !strings.EqualFold(cfg.Tools.HostSandbox, "off") {
		shell.sandbox = sandbox.Detect()
	}
	r.Add(shell)
	if len(services) > 0 && services[0].Exec != nil {
		r.Add(&execStart{shell: shell, manager: services[0].Exec})
		r.Add(&execWrite{manager: services[0].Exec})
		r.Add(&execStop{manager: services[0].Exec})
	}
	r.Add(&verificationPlan{designedWorkflow: cfg.Models.DesignedWorkflow})
	r.Add(&verificationRun{shell: shell})
	r.Add(&browserVerify{shell: shell})
	if len(services) > 0 && services[0].Previews != nil {
		r.Add(&previewStart{manager: services[0].Previews, shell: shell})
		r.Add(&previewStop{manager: services[0].Previews})
	}
	if len(services) > 0 && services[0].Previews != nil && services[0].VisualQA != nil {
		r.Add(&visualStart{previews: services[0].Previews, manager: services[0].VisualQA})
		r.Add(&visualInspect{manager: services[0].VisualQA})
		r.Add(&visualAct{manager: services[0].VisualQA})
		r.Add(&visualStop{manager: services[0].VisualQA})
	}
	if len(services) > 0 && services[0].ComputerUse != nil {
		r.Add(&computerList{manager: services[0].ComputerUse})
		r.Add(&computerStart{manager: services[0].ComputerUse})
		r.Add(&computerInspect{manager: services[0].ComputerUse})
		r.Add(&computerAct{manager: services[0].ComputerUse})
		r.Add(&computerStop{manager: services[0].ComputerUse})
	}
}

type verificationRun struct{ shell *shellRun }

func (*verificationRun) Name() string { return "verification.run" }
func (*verificationRun) Description() string {
	return "Run an ordered verification plan for code changes, streaming each formatter, typecheck, test, or build command and returning reproducible pass/fail/blocked evidence. Prefer project-defined scripts and run this before claiming a coding task is complete."
}
func (*verificationRun) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"checks":{"type":"array","minItems":1,"maxItems":12,"items":{"type":"object","properties":{"label":{"type":"string"},"command":{"type":"string"},"directory":{"type":"string"},"reason":{"type":"string"},"required_command":{"type":"string"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"required":["label","command"]}},"continue_on_failure":{"type":"boolean"}},"required":["checks"]}`)
}
func (t *verificationRun) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct {
		Checks []struct {
			Command string `json:"command"`
		} `json:"checks"`
	}](args)
	risk := RiskYellow
	for _, check := range a.Checks {
		command, _ := json.Marshal(map[string]string{"command": check.Command})
		if commandRisk, _ := t.shell.Assess(command); commandRisk == RiskRed {
			risk = RiskRed
			break
		}
	}
	return risk, fmt.Sprintf("Run %d verification checks", len(a.Checks))
}
func (t *verificationRun) Call(ctx context.Context, args json.RawMessage) (string, error) {
	type check struct {
		Label           string `json:"label"`
		Command         string `json:"command"`
		Directory       string `json:"directory"`
		Reason          string `json:"reason"`
		RequiredCommand string `json:"required_command"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
	}
	type result struct {
		Label      string `json:"label"`
		Command    string `json:"command"`
		Directory  string `json:"directory,omitempty"`
		Reason     string `json:"reason,omitempty"`
		Status     string `json:"status"`
		ExitCode   int    `json:"exit_code,omitempty"`
		DurationMS int64  `json:"duration_ms"`
		Output     string `json:"output,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	a, err := decode[struct {
		Checks            []check `json:"checks"`
		ContinueOnFailure bool    `json:"continue_on_failure"`
	}](args)
	if err != nil {
		return "", err
	}
	if len(a.Checks) == 0 || len(a.Checks) > 12 {
		return "", errors.New("verification plan must contain between 1 and 12 checks")
	}
	scope := ScopeFrom(ctx)
	results := make([]result, 0, len(a.Checks))
	fulls := make([]string, 0, len(a.Checks)) // each check's unclipped shell output, parallel to results
	for _, c := range a.Checks {
		if strings.TrimSpace(c.Label) == "" || strings.TrimSpace(c.Command) == "" {
			return "", errors.New("each verification check needs a label and command")
		}
		if scope != nil && scope.Progress != nil {
			scope.Progress(fmt.Sprintf("\n=== %s ===\n$ %s\n", c.Label, c.Command))
		}
		started := time.Now()
		if c.RequiredCommand != "" {
			probeArgs, _ := json.Marshal(map[string]any{
				"command": "command -v " + shellQuote(c.RequiredCommand), "workspace": c.Directory, "timeout_seconds": 30,
			})
			probeOutput, probeErr := t.shell.Call(ctx, probeArgs)
			if probeErr != nil || shellExitCode(probeOutput) != 0 {
				results = append(results, result{Label: c.Label, Command: c.Command, Directory: c.Directory, Reason: c.Reason,
					Status: "not_run", DurationMS: time.Since(started).Milliseconds(), Error: "required command is unavailable: " + c.RequiredCommand})
				fulls = append(fulls, "")
				if !a.ContinueOnFailure {
					break
				}
				continue
			}
		}
		callArgs, _ := json.Marshal(map[string]any{
			"command": c.Command, "workspace": c.Directory, "timeout_seconds": c.TimeoutSeconds,
		})
		output, runErr := t.shell.Call(ctx, callArgs)
		r := result{Label: c.Label, Command: c.Command, Directory: c.Directory, Reason: c.Reason, Status: "passed", DurationMS: time.Since(started).Milliseconds(), Output: clipAt(output, 3<<10)}
		if runErr != nil {
			r.Status, r.Error = "blocked", runErr.Error()
		} else if strings.HasPrefix(output, "exit_code: ") {
			r.ExitCode = shellExitCode(output)
			if r.ExitCode != 0 {
				r.Status = "failed"
			}
		}
		results = append(results, r)
		fulls = append(fulls, output)
		if r.Status != "passed" && !a.ContinueOnFailure {
			break
		}
	}
	payload := struct {
		Status  string   `json:"status"`
		Results []result `json:"results"`
	}{Status: "passed", Results: results}
	for _, r := range results {
		if r.Status != "passed" {
			payload.Status = r.Status
			break
		}
	}
	b, _ := json.Marshal(payload)
	// The engine records attempts from the unclipped results: every check keeps
	// the shell's own (64 KB) output instead of the 3 KB shown to the model.
	rawPayload := payload
	rawPayload.Results = append([]result(nil), results...)
	for i := range rawPayload.Results {
		if i < len(fulls) && fulls[i] != "" {
			rawPayload.Results[i].Output = fulls[i]
		}
	}
	if rb, err := json.Marshal(rawPayload); err == nil {
		SetRaw(ctx, string(rb))
	}
	return clip(string(b)), nil
}

func shellExitCode(output string) int {
	line := strings.TrimPrefix(strings.SplitN(output, "\n", 2)[0], "exit_code: ")
	var code int
	if _, err := fmt.Sscanf(line, "%d", &code); err != nil {
		return -1
	}
	return code
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type previewStart struct {
	manager *preview.Manager
	shell   *shellRun
}

func (*previewStart) Name() string { return "preview.start" }
func (*previewStart) Description() string {
	return "Start a persistent frontend development server in the project's isolated microVM and open it in UMCode's Preview tab. Use the server's explicit host/port flags so it listens on 0.0.0.0 at the supplied port."
}
func (*previewStart) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"command":{"type":"string","description":"Long-running dev-server command"},"port":{"type":"integer","minimum":1,"maximum":65535,"description":"TCP port the server listens on inside the microVM"},"directory":{"type":"string","description":"Project-relative working directory"},"title":{"type":"string","description":"Short tab title"},"ready_timeout_seconds":{"type":"integer","minimum":1,"maximum":120}},"required":["command","port"]}`)
}
func (t *previewStart) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct{ Command string }](args)
	command, _ := json.Marshal(map[string]string{"command": a.Command})
	risk, _ := t.shell.Assess(command)
	return risk, "Start live preview: " + a.Command
}
func (t *previewStart) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		Command             string `json:"command"`
		Port                int    `json:"port"`
		Directory           string `json:"directory"`
		Title               string `json:"title"`
		ReadyTimeoutSeconds int    `json:"ready_timeout_seconds"`
	}](args)
	if err != nil {
		return "", err
	}
	scope := ScopeFrom(ctx)
	if scope == nil {
		return "", ErrNoProject
	}
	if !scope.UseCompute {
		return "", errors.New("live preview requires isolated compute for this project")
	}
	dir := scope.Root
	if a.Directory != "" {
		dir, err = scope.Resolve(a.Directory)
		if err != nil {
			return "", err
		}
	}
	session, err := t.manager.Start(preview.StartRequest{
		ThreadID: scope.ThreadID, ProjectID: scope.ProjectID, Title: a.Title,
		Root: scope.Root, Dir: dir, Command: a.Command, GuestPort: a.Port,
		Network: scope.AllowNet, VCPUs: scope.ComputeVCPUs, MemoryMiB: scope.ComputeMemoryMiB,
		DiskLimitMiB: scope.ComputeDiskMiB, ReadyTimeout: time.Duration(a.ReadyTimeoutSeconds) * time.Second,
		Progress: scope.Progress,
	})
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(session)
	return string(b), nil
}

type previewStop struct{ manager *preview.Manager }

func (*previewStop) Name() string        { return "preview.stop" }
func (*previewStop) Description() string { return "Stop a running live preview by its preview ID." }
func (*previewStop) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"preview_id":{"type":"string"}},"required":["preview_id"]}`)
}
func (*previewStop) Assess(json.RawMessage) (Risk, string) { return RiskGreen, "Stop live preview" }
func (t *previewStop) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		PreviewID string `json:"preview_id"`
	}](args)
	if err != nil {
		return "", err
	}
	scope := ScopeFrom(ctx)
	if scope == nil || scope.ThreadID == "" {
		return "", errors.New("live preview requires a project chat")
	}
	if err := t.manager.StopFor(a.PreviewID, scope.ThreadID); err != nil {
		return "", err
	}
	return "preview stopping", nil
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

func clip(s string) string {
	return clipAt(s, maxOutput)
}

func clipAt(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// Keep the start and the end: build and test output carries its verdict
	// (the failing test, the final error) at the tail.
	headN := limit * 65 / 100
	tailN := limit - headN
	head := s[:headN]
	for !utf8.ValidString(head) && len(head) > 0 {
		head = head[:len(head)-1]
	}
	tail := s[len(s)-tailN:]
	for !utf8.ValidString(tail) && len(tail) > 0 {
		tail = tail[1:]
	}
	return head + fmt.Sprintf("\n… [truncated %d bytes from the middle] …\n", len(s)-len(head)-len(tail)) + tail
}

// resolvePath resolves a tool's path argument. Inside a project every path is
// resolved against its root; without a project the configured workspaces are
// used, and only for reading.
func resolvePath(ctx context.Context, ws *Workspaces, name, path string, op Operation) (string, error) {
	if scope := ScopeFrom(ctx); scope != nil {
		if name != "" && name != "project" {
			return "", fmt.Errorf("this chat works in the project %s; %q is not one of its folders", scope.ProjectName, name)
		}
		return scope.Resolve(path)
	}
	if op != OpRead {
		return "", ErrNoProject
	}
	w, err := ws.Get(name)
	if err != nil {
		return "", err
	}
	return w.Resolve(path, op)
}

// ---- file.read ----

type fileRead struct{ ws *Workspaces }

func (*fileRead) Name() string { return "file.read" }
func (*fileRead) Description() string {
	return "Read a text file from the open project. Paths are relative to the project root."
}
func (*fileRead) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"path":{"type":"string","description":"File path, relative to the workspace root or absolute inside it"},"workspace":{"type":"string","description":"Workspace name; omit for the default"}},"required":["path"]}`)
}
func (*fileRead) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct{ Path string }](args)
	return RiskGreen, "Read " + a.Path
}
func (t *fileRead) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct{ Path, Workspace string }](args)
	if err != nil {
		return "", err
	}
	p, err := resolvePath(ctx, t.ws, a.Workspace, a.Path, OpRead)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", errors.New("file is not UTF-8 text")
	}
	captureFileExcerpt(ctx, p, b, 1, 40)
	return clip(string(b)), nil
}

// ---- file.list ----

type fileList struct{ ws *Workspaces }

func (*fileList) Name() string { return "file.list" }
func (*fileList) Description() string {
	return "List files in a directory of the open project."
}
func (*fileList) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"path":{"type":"string","description":"Directory, relative to the workspace root; default is the root"},"workspace":{"type":"string"}}}`)
}
func (*fileList) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct{ Path string }](args)
	return RiskGreen, "List " + a.Path
}
func (t *fileList) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct{ Path, Workspace string }](args)
	if err != nil {
		return "", err
	}
	p, err := resolvePath(ctx, t.ws, a.Workspace, a.Path, OpRead)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		} else if info.Mode()&fs.ModeSymlink != 0 {
			kind = "link"
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\n", kind, e.Name(), info.Size())
	}
	if b.Len() == 0 {
		return "(empty directory)", nil
	}
	return clip(b.String()), nil
}

// ---- file.write ----

type fileWrite struct{ ws *Workspaces }

func (*fileWrite) Name() string { return "file.write" }
func (*fileWrite) Description() string {
	return "Create or overwrite a text file inside the open project. The change is recorded and can be undone."
}
func (*fileWrite) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"workspace":{"type":"string"}},"required":["path","content"]}`)
}
func (*fileWrite) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct {
		Path    string
		Content string
	}](args)
	return RiskYellow, fmt.Sprintf("Write %d bytes to %s", len(a.Content), a.Path)
}
func (t *fileWrite) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct{ Path, Content, Workspace string }](args)
	if err != nil {
		return "", err
	}
	scope := ScopeFrom(ctx)
	if scope == nil {
		return "", ErrNoProject
	}
	p, err := resolvePath(ctx, t.ws, a.Workspace, a.Path, OpWrite)
	if err != nil {
		return "", err
	}
	before := Snapshot(p)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	if scope.Record != nil {
		scope.Record(ctx, p, before, false)
	}
	what := "wrote"
	if before == nil {
		what = "created"
	}
	return fmt.Sprintf("%s %s (%d bytes)", what, scope.Rel(p), len(a.Content)), nil
}

// ---- shell.run ----

type shellRun struct {
	ws       *Workspaces
	forbid   []string
	skillEnv SkillEnvFunc
	compute  compute.Runner
	// sandbox confines host commands; nil means they run unconfined.
	sandbox sandbox.Sandbox
}

type liveOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	label    string
	progress func(string)
	sent     int
}

func (w *liveOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := maxOutput - w.buf.Len()
	if remaining > 0 {
		chunk := p
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		_, _ = w.buf.Write(chunk)
		if w.progress != nil && len(chunk) > 0 {
			prefix := ""
			if w.sent == 0 {
				prefix = w.label
			}
			w.progress(prefix + string(chunk))
			w.sent += len(chunk)
		}
	}
	return len(p), nil
}

func (w *liveOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *liveOutput) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Len()
}

func (*shellRun) Name() string { return "shell.run" }
func (*shellRun) Description() string {
	return "Run a shell command (sh -c) in the open project. Returns exit code, stdout and stderr. Commands run in the project's configured compute environment and are subject to UMCode's workspace sandbox and chat approval policy."
}
func (*shellRun) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"command":{"type":"string"},"workspace":{"type":"string","description":"Existing folder path inside the project, not the project's display name; omit to run in the project root"},"timeout_seconds":{"type":"integer","description":"Default 60, max 600"},"skill":{"type":"string","description":"Run with this skill's environment (its venv, PATH and env vars; SKILL_DIR is set)"}},"required":["command"]}`)
}

func (t *shellRun) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct{ Command string }](args)
	cmd := strings.TrimSpace(a.Command)
	class, reason := ClassifyShell(cmd, t.forbid)
	switch {
	case class == ClassForbidden:
		return RiskRed, "Blocked (" + reason + "): " + cmd
	case class == ClassSafe:
		return RiskGreen, "Run (read-only): " + cmd
	}
	return RiskRed, "Run: " + cmd
}

// Forbidden refuses destructive commands before an approval is requested.
func (t *shellRun) Forbidden(args json.RawMessage) (string, bool) {
	a, _ := decode[struct{ Command string }](args)
	if class, reason := ClassifyShell(strings.TrimSpace(a.Command), t.forbid); class == ClassForbidden {
		return reason, true
	}
	return "", false
}

// safeEnv is the environment passed to shell commands: no API keys or tokens.
func safeEnv() []string {
	keep := []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "SHELL", "TERM"}
	var env []string
	values := make(map[string]string, len(keep))
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			values[k] = v
		}
	}
	values["PATH"] = discoveredToolPath(values["PATH"], values["HOME"])
	for _, k := range keep {
		if v, ok := values[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// discoveredToolPath adds common per-user toolchain locations that GUI-launched
// macOS apps do not inherit from interactive shell startup files. It does not
// evaluate shell rc files or import any additional environment variables.
func discoveredToolPath(path, home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	var candidates []string
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".volta", "bin"),
			filepath.Join(home, ".local", "share", "mise", "shims"),
			filepath.Join(home, ".asdf", "shims"),
			filepath.Join(home, ".fnm", "current", "bin"),
		)
		if matches, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); len(matches) > 0 {
			// Glob is sorted lexically; newest Node releases sort last.
			for i := len(matches) - 1; i >= 0; i-- {
				candidates = append(candidates, matches[i])
			}
		}
	}
	candidates = append(candidates, "/opt/homebrew/bin", "/usr/local/bin")
	parts := filepath.SplitList(path)
	seen := make(map[string]bool, len(parts)+len(candidates))
	var result []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return
		}
		seen[p] = true
		result = append(result, p)
	}
	for _, p := range parts {
		add(p)
	}
	for _, p := range candidates {
		add(p)
	}
	return strings.Join(result, string(os.PathListSeparator))
}

func (t *shellRun) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		Command        string
		Workspace      string
		Skill          string
		TimeoutSeconds int `json:"timeout_seconds"`
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
	sandboxed := !scope.UseCompute && t.sandbox != nil
	trusted := scope.TrustedAutonomy()
	if !scope.AllowNet && !scope.UseCompute && !sandboxed && !trusted {
		return "", errors.New("shell execution is blocked while network access is off unless the project's microVM is enabled, the host sandbox is available, or the chat's approval mode is auto_workspace/auto_all; host shell networking cannot be safely restricted otherwise")
	}
	if !scope.AllowNet && !sandboxed && !trusted {
		if prog, yes := NeedsNetwork(a.Command); yes {
			return "", fmt.Errorf("%s needs the network, which is off for the project %s (turn it on in the project's settings, or set this chat's approval mode to auto_workspace/auto_all)", prog, scope.ProjectName)
		}
	}
	dir, err := scope.CommandDir(a.Workspace)
	if err != nil {
		return "", err
	}
	timeout := time.Duration(a.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var progress func(string)
	if scope.Progress != nil {
		progress = scope.Progress
	}
	stdout := &liveOutput{label: "--- stdout ---\n", progress: progress}
	stderr := &liveOutput{label: "\n--- stderr ---\n", progress: progress}
	if scope.UseCompute {
		if a.Skill != "" {
			return "", errors.New("skill-specific environments are not available in isolated compute yet")
		}
		if t.compute == nil {
			return "", errors.New("isolated compute is unavailable")
		}
		code, runErr := t.compute.Run(cctx, compute.Request{
			Root: scope.Root, Dir: dir, Command: a.Command,
			Network: scope.AllowNet, Timeout: int(timeout.Seconds()),
			VCPUs: scope.ComputeVCPUs, MemoryMiB: scope.ComputeMemoryMiB,
			DiskLimitBytes: int64(scope.ComputeDiskMiB) << 20, Stdout: stdout, Stderr: stderr,
		})
		if runErr != nil {
			return "", runErr
		}
		return shellOutput(code, stdout, stderr), nil
	}
	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", a.Command)
	procutil.Prepare(cmd)
	cmd.Dir = dir
	cmd.Env = safeEnv()
	if a.Skill != "" {
		if t.skillEnv == nil {
			return "", errors.New("skills are not available")
		}
		env, err := t.skillEnv(ctx, a.Skill)
		if err != nil {
			return "", err
		}
		cmd.Env = env
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if sandboxed {
		if err := t.sandbox.Wrap(cmd, sandbox.DefaultPolicy(scope.Root, scope.AllowNet)); err != nil {
			return "", fmt.Errorf("could not start the command sandbox: %w", err)
		}
	}
	runErr := cmd.Run()
	code := 0
	var ee *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &ee):
		code = ee.ExitCode()
	case cctx.Err() != nil:
		return "", fmt.Errorf("command timed out after %s", timeout)
	default:
		return "", runErr
	}
	if cctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out after %s", timeout)
	}
	out := shellOutput(code, stdout, stderr)
	if sandboxed && code != 0 {
		out += sandboxHint(stdout.String()+stderr.String(), scope.AllowNet)
	}
	return out, nil
}

// sandboxHint explains a failure that looks like the sandbox's doing, so the
// model does not retry the same thing or try to work around it.
func sandboxHint(output string, network bool) string {
	low := strings.ToLower(output)
	switch {
	case strings.Contains(low, "operation not permitted") || strings.Contains(low, "read-only file system") || strings.Contains(low, "permission denied"):
		return "\n\n[sandbox: this command runs sandboxed. Writes are limited to the project folder and temp/cache folders, and credential folders (~/.ssh, ~/.aws, ...) are unreadable. If the failure is a blocked path, do not work around it; explain it to the user.]"
	case !network && (strings.Contains(low, "could not resolve host") || strings.Contains(low, "network is unreachable") ||
		strings.Contains(low, "temporary failure in name resolution") || strings.Contains(low, "getaddrinfo") ||
		strings.Contains(low, "connection refused") || strings.Contains(low, "no route to host")):
		return "\n\n[sandbox: network access is off for this project, so the command could not reach the network. Ask the user to enable network access in the project's settings if it is needed.]"
	}
	return ""
}

func shellOutput(code int, stdout, stderr interface {
	String() string
	Len() int
}) string {
	out := fmt.Sprintf("exit_code: %d\n--- stdout ---\n%s", code, stdout.String())
	if stderr.Len() > 0 {
		out += "\n--- stderr ---\n" + stderr.String()
	}
	return clip(out)
}
