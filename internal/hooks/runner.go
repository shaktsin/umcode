package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/procutil"
)

type Recorder func(context.Context, Record) error

type Invocation struct {
	Version       int
	Event         Event
	PluginID      string
	PluginVersion string
	ProjectID     string
	ProjectRoot   string
	ThreadID      string
	TurnID        string
	ToolName      string
	ToolArgs      json.RawMessage
	ToolOutput    string
	ToolError     string
	At            time.Time
}

type Outcome struct {
	Blocked  bool
	Reason   string
	Context  []string
	Warnings []string
	Records  []Record
}

type Set struct {
	Declarations []Declaration
}

type Record struct {
	PluginID      string
	PluginVersion string
	ProjectID     string
	ThreadID      string
	TurnID        string
	Event         Event
	Status        string
	Blocked       bool
	Reason        string
	Warning       string
	Context       string
	Error         string
	Duration      time.Duration
	CreatedAt     time.Time
}

type Runner struct {
	recorder Recorder
}

func NewRunner(recorder Recorder) *Runner { return &Runner{recorder: recorder} }

func (r *Runner) Run(ctx context.Context, set Set, invocation Invocation) Outcome {
	declarations := matchingDeclarations(set.Declarations, invocation)
	if len(declarations) == 0 {
		return Outcome{}
	}
	if invocation.Version == 0 {
		invocation.Version = EnvelopeVersion
	}
	if invocation.At.IsZero() {
		invocation.At = time.Now().UTC()
	}
	if invocation.Event.CanBlock() {
		return r.runSequential(ctx, declarations, invocation)
	}
	return r.runConcurrent(ctx, declarations, invocation)
}

func matchingDeclarations(all []Declaration, invocation Invocation) []Declaration {
	declarations := make([]Declaration, 0, len(all))
	for _, declaration := range all {
		if declaration.Event != invocation.Event {
			continue
		}
		if declaration.Matcher != "" && invocation.ToolName != "" {
			matched, err := path.Match(declaration.Matcher, invocation.ToolName)
			if err != nil || !matched {
				continue
			}
		}
		declarations = append(declarations, declaration)
	}
	sort.SliceStable(declarations, func(left, right int) bool {
		if declarations[left].PluginID == declarations[right].PluginID {
			return declarations[left].Order < declarations[right].Order
		}
		return declarations[left].PluginID < declarations[right].PluginID
	})
	return declarations
}

func (r *Runner) runSequential(ctx context.Context, declarations []Declaration, invocation Invocation) Outcome {
	var outcome Outcome
	for _, declaration := range declarations {
		execution := r.execute(ctx, declaration, invocation)
		outcome.Records = append(outcome.Records, execution.record)
		if execution.err != nil {
			if declaration.Required {
				outcome.Blocked = true
				outcome.Reason = redact("required plugin hook failed: "+execution.err.Error(), declaration.SecretValues)
				break
			}
			outcome.Warnings = append(outcome.Warnings, redact("optional plugin hook failed: "+execution.err.Error(), declaration.SecretValues))
			continue
		}
		outcome.Context = append(outcome.Context, execution.result.Context...)
		if execution.result.Warning != "" {
			outcome.Warnings = append(outcome.Warnings, execution.result.Warning)
		}
		if execution.result.Block {
			outcome.Blocked = true
			outcome.Reason = execution.result.Reason
			break
		}
	}
	return outcome
}

func (r *Runner) runConcurrent(ctx context.Context, declarations []Declaration, invocation Invocation) Outcome {
	type indexedExecution struct {
		index     int
		execution hookExecution
	}
	results := make(chan indexedExecution, len(declarations))
	semaphore := make(chan struct{}, 4)
	var group sync.WaitGroup
	for index, declaration := range declarations {
		index, declaration := index, declaration
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- indexedExecution{index: index, execution: failedExecution(declaration, invocation, ctx.Err())}
				return
			}
			results <- indexedExecution{index: index, execution: r.execute(ctx, declaration, invocation)}
		}()
	}
	group.Wait()
	close(results)
	ordered := make([]hookExecution, len(declarations))
	for result := range results {
		ordered[result.index] = result.execution
	}
	var outcome Outcome
	for _, execution := range ordered {
		outcome.Records = append(outcome.Records, execution.record)
		if execution.err == nil {
			outcome.Context = append(outcome.Context, execution.result.Context...)
			if execution.result.Warning != "" {
				outcome.Warnings = append(outcome.Warnings, execution.result.Warning)
			}
		}
	}
	return outcome
}

type hookExecution struct {
	result hookResult
	record Record
	err    error
}

type hookResult struct {
	Version  int      `json:"version"`
	Continue bool     `json:"continue"`
	Block    bool     `json:"block"`
	Reason   string   `json:"reason"`
	Context  []string `json:"context"`
	Warning  string   `json:"warning"`
}

func (r *Runner) execute(ctx context.Context, declaration Declaration, invocation Invocation) hookExecution {
	invocation = attributedInvocation(declaration, invocation)
	started := time.Now()
	record := baseRecord(declaration, invocation)
	fail := func(err error) hookExecution {
		errText := redact(err.Error(), declaration.SecretValues)
		record.Status = "failed"
		record.Error = errText
		record.Duration = time.Since(started)
		r.record(ctx, record)
		return hookExecution{record: record, err: errors.New(errText)}
	}
	executable, args, err := resolveHookCommand(declaration)
	if err != nil {
		return fail(err)
	}
	envelope, err := json.Marshal(makeEnvelope(invocation))
	if err != nil {
		return fail(err)
	}
	timeout := declaration.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaximumTimeout {
		timeout = MaximumTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, executable, args...)
	procutil.Prepare(command)
	command.Dir = invocation.ProjectRoot
	if command.Dir == "" {
		command.Dir = declaration.Root
	}
	command.Env = hookEnvironment(declaration.Env)
	command.Stdin = bytes.NewReader(envelope)
	stdout := &boundedBuffer{limit: MaxStdout}
	stderr := &boundedBuffer{limit: MaxStderr}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return fail(fmt.Errorf("hook timed out after %s", timeout))
		}
		if stdout.overflow || stderr.overflow {
			return fail(errors.New("hook output exceeded its limit"))
		}
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fail(fmt.Errorf("hook exited: %w: %s", err, message))
		}
		return fail(fmt.Errorf("hook exited: %w", err))
	}
	if stdout.overflow || stderr.overflow {
		return fail(errors.New("hook output exceeded its limit"))
	}
	var result hookResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return fail(fmt.Errorf("invalid hook result JSON: %w", err))
	}
	if err := validateResult(result); err != nil {
		return fail(err)
	}
	result.Reason = redact(result.Reason, declaration.SecretValues)
	result.Warning = redact(result.Warning, declaration.SecretValues)
	for index := range result.Context {
		result.Context[index] = redact(result.Context[index], declaration.SecretValues)
	}
	record.Status = "continued"
	if result.Block && invocation.Event.CanBlock() {
		record.Status = "blocked"
		record.Blocked = true
		record.Reason = result.Reason
	} else {
		result.Block = false
		result.Reason = ""
	}
	record.Warning = result.Warning
	record.Context = strings.Join(result.Context, "\n")
	record.Duration = time.Since(started)
	r.record(ctx, record)
	return hookExecution{result: result, record: record}
}

func failedExecution(declaration Declaration, invocation Invocation, err error) hookExecution {
	record := baseRecord(declaration, invocation)
	record.Status, record.Error = "failed", redact(err.Error(), declaration.SecretValues)
	return hookExecution{record: record, err: errors.New(record.Error)}
}

func (r *Runner) record(ctx context.Context, record Record) {
	if r.recorder != nil {
		_ = r.recorder(ctx, record)
	}
}

func baseRecord(declaration Declaration, invocation Invocation) Record {
	invocation = attributedInvocation(declaration, invocation)
	return Record{PluginID: invocation.PluginID, PluginVersion: invocation.PluginVersion, ProjectID: invocation.ProjectID, ThreadID: invocation.ThreadID, TurnID: invocation.TurnID, Event: invocation.Event, CreatedAt: invocation.At}
}

func attributedInvocation(declaration Declaration, invocation Invocation) Invocation {
	if declaration.PluginID != "" {
		invocation.PluginID = declaration.PluginID
	}
	if declaration.PluginVersion != "" {
		invocation.PluginVersion = declaration.PluginVersion
	}
	return invocation
}

type hookEnvelope struct {
	Version       int          `json:"version"`
	Event         Event        `json:"event"`
	PluginID      string       `json:"pluginId"`
	PluginVersion string       `json:"pluginVersion,omitempty"`
	ProjectID     string       `json:"projectId,omitempty"`
	ProjectRoot   string       `json:"projectRoot,omitempty"`
	ThreadID      string       `json:"threadId,omitempty"`
	TurnID        string       `json:"turnId,omitempty"`
	At            time.Time    `json:"at"`
	Tool          hookToolData `json:"tool"`
}

type hookToolData struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
	Truncated struct {
		Arguments bool `json:"arguments"`
		Output    bool `json:"output"`
		Error     bool `json:"error"`
	} `json:"truncated"`
}

func makeEnvelope(invocation Invocation) hookEnvelope {
	arguments, argsTruncated := boundedString(string(invocation.ToolArgs), MaxEventField)
	output, outputTruncated := boundedString(invocation.ToolOutput, MaxEventField)
	toolError, errorTruncated := boundedString(invocation.ToolError, MaxEventField)
	envelope := hookEnvelope{Version: EnvelopeVersion, Event: invocation.Event, PluginID: invocation.PluginID, PluginVersion: invocation.PluginVersion, ProjectID: invocation.ProjectID, ProjectRoot: invocation.ProjectRoot, ThreadID: invocation.ThreadID, TurnID: invocation.TurnID, At: invocation.At}
	envelope.Tool.Name, envelope.Tool.Arguments, envelope.Tool.Output, envelope.Tool.Error = invocation.ToolName, arguments, output, toolError
	envelope.Tool.Truncated.Arguments, envelope.Tool.Truncated.Output, envelope.Tool.Truncated.Error = argsTruncated, outputTruncated, errorTruncated
	return envelope
}

func boundedString(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	return value[:limit], true
}

func validateResult(result hookResult) error {
	if result.Version != EnvelopeVersion {
		return fmt.Errorf("unsupported hook result version %d", result.Version)
	}
	if len(result.Reason) > MaxBlockReason {
		return errors.New("hook block reason exceeds limit")
	}
	if len(result.Warning) > MaxWarning {
		return errors.New("hook warning exceeds limit")
	}
	contextBytes := 0
	for _, value := range result.Context {
		contextBytes += len(value)
	}
	if contextBytes > MaxContext {
		return errors.New("hook context exceeds limit")
	}
	if result.Block && strings.TrimSpace(result.Reason) == "" {
		return errors.New("blocking hook result requires a reason")
	}
	return nil
}

func resolveHookCommand(declaration Declaration) (string, []string, error) {
	fields := strings.Fields(declaration.Command)
	if len(fields) == 0 {
		return "", nil, errors.New("hook command is empty")
	}
	commandPath := filepath.Clean(fields[0])
	if filepath.IsAbs(commandPath) || commandPath == ".." || strings.HasPrefix(commandPath, ".."+string(filepath.Separator)) {
		return "", nil, errors.New("hook executable must be package-relative")
	}
	root, err := filepath.EvalSymlinks(declaration.Root)
	if err != nil {
		return "", nil, fmt.Errorf("resolve hook root: %w", err)
	}
	executable, err := filepath.EvalSymlinks(filepath.Join(root, commandPath))
	if err != nil {
		return "", nil, fmt.Errorf("resolve hook executable: %w", err)
	}
	relative, err := filepath.Rel(root, executable)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", nil, errors.New("hook executable resolves outside the plugin root")
	}
	args := append([]string(nil), fields[1:]...)
	args = append(args, declaration.Args...)
	return executable, args, nil
}

func hookEnvironment(declared map[string]string) []string {
	values := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "SHELL", "TERM"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if values["PATH"] == "" {
		values["PATH"] = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	values["PATH"] += ":/opt/homebrew/bin:/usr/local/bin"
	for key, value := range declared {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func redact(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.overflow = true
		return len(data), nil
	}
	return b.Buffer.Write(data)
}
