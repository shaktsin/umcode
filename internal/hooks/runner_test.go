package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("UMCODE_HOOK_TEST_MODE"); mode != "" {
		runHookTestHelper(mode)
		return
	}
	os.Exit(m.Run())
}

func TestHookEnvelopeVersionAndBounds(t *testing.T) {
	root, command := hookTestCommand(t)
	capture := filepath.Join(t.TempDir(), "envelope.json")
	large := strings.Repeat("x", MaxEventField+1024)
	runner := NewRunner(nil)
	outcome := runner.Run(t.Context(), Set{Declarations: []Declaration{{
		PluginID: "capture", Root: root, Event: BeforeToolUse, Command: command,
		Env: map[string]string{"UMCODE_HOOK_TEST_MODE": "capture", "CAPTURE_PATH": capture}, Required: true,
	}}}, Invocation{Version: EnvelopeVersion, Event: BeforeToolUse, ToolName: "shell.run", ToolArgs: json.RawMessage(`{"value":"` + large + `"}`), ToolOutput: large, ToolError: large})
	if outcome.Blocked {
		t.Fatalf("capture hook blocked: %#v", outcome)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Version int `json:"version"`
		Tool    struct {
			Arguments string `json:"arguments"`
			Output    string `json:"output"`
			Error     string `json:"error"`
			Truncated struct {
				Arguments bool `json:"arguments"`
				Output    bool `json:"output"`
				Error     bool `json:"error"`
			} `json:"truncated"`
		} `json:"tool"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != EnvelopeVersion {
		t.Fatalf("envelope version = %d", envelope.Version)
	}
	if len(envelope.Tool.Arguments) != MaxEventField || len(envelope.Tool.Output) != MaxEventField || len(envelope.Tool.Error) != MaxEventField {
		t.Fatalf("bounded lengths = (%d,%d,%d), want %d", len(envelope.Tool.Arguments), len(envelope.Tool.Output), len(envelope.Tool.Error), MaxEventField)
	}
	if !envelope.Tool.Truncated.Arguments || !envelope.Tool.Truncated.Output || !envelope.Tool.Truncated.Error {
		t.Fatalf("truncation metadata = %#v", envelope.Tool.Truncated)
	}
}

func TestBlockingHooksRunDeterministically(t *testing.T) {
	root, command := hookTestCommand(t)
	orderPath := filepath.Join(t.TempDir(), "order")
	declarations := []Declaration{
		{PluginID: "zeta", Root: root, Event: BeforeToolUse, Command: command, Order: 0, Env: helperEnv("append", map[string]string{"ORDER_PATH": orderPath, "MARK": "zeta"})},
		{PluginID: "alpha", Root: root, Event: BeforeToolUse, Command: command, Order: 1, Env: helperEnv("append", map[string]string{"ORDER_PATH": orderPath, "MARK": "alpha-1"})},
		{PluginID: "alpha", Root: root, Event: BeforeToolUse, Command: command, Order: 0, Env: helperEnv("append", map[string]string{"ORDER_PATH": orderPath, "MARK": "alpha-0"})},
	}
	outcome := NewRunner(nil).Run(t.Context(), Set{Declarations: declarations}, Invocation{Version: 1, Event: BeforeToolUse})
	if outcome.Blocked {
		t.Fatalf("hooks blocked: %#v", outcome)
	}
	data, err := os.ReadFile(orderPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "alpha-0\nalpha-1\nzeta\n" {
		t.Fatalf("hook order = %q", got)
	}
}

func TestObservationalHooksRespectConcurrencyLimit(t *testing.T) {
	root, command := hookTestCommand(t)
	var mu sync.Mutex
	current, maximum := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if request.URL.Query().Get("event") == "start" {
			current++
			if current > maximum {
				maximum = current
			}
		} else {
			current--
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var declarations []Declaration
	for n := 0; n < 8; n++ {
		declarations = append(declarations, Declaration{PluginID: fmt.Sprintf("plugin-%d", n), Root: root, Event: AfterToolUse, Command: command, Env: helperEnv("concurrency", map[string]string{"SLEEP_MS": "200", "COUNTER_URL": server.URL})})
	}
	outcome := NewRunner(nil).Run(t.Context(), Set{Declarations: declarations}, Invocation{Version: 1, Event: AfterToolUse})
	if len(outcome.Records) != 8 {
		t.Fatalf("records = %d, want 8", len(outcome.Records))
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum != 4 || current != 0 {
		t.Fatalf("observed concurrency max=%d current=%d, want max=4 current=0", maximum, current)
	}
}

func TestRequiredBeforeHookFailsClosed(t *testing.T) {
	root, command := hookTestCommand(t)
	outcome := NewRunner(nil).Run(t.Context(), Set{Declarations: []Declaration{{PluginID: "required", Root: root, Event: BeforeToolUse, Command: command, Required: true, Env: helperEnv("exit", nil)}}}, Invocation{Version: 1, Event: BeforeToolUse})
	if !outcome.Blocked || outcome.Reason == "" {
		t.Fatalf("required failure outcome = %#v, want blocked", outcome)
	}
}

func TestOptionalBeforeHookWarnsAndContinues(t *testing.T) {
	root, command := hookTestCommand(t)
	outcome := NewRunner(nil).Run(t.Context(), Set{Declarations: []Declaration{{PluginID: "optional", Root: root, Event: BeforeToolUse, Command: command, Env: helperEnv("exit", nil)}}}, Invocation{Version: 1, Event: BeforeToolUse})
	if outcome.Blocked || len(outcome.Warnings) != 1 {
		t.Fatalf("optional failure outcome = %#v, want one warning", outcome)
	}
}

func TestAfterHookFailureCannotChangeResult(t *testing.T) {
	root, command := hookTestCommand(t)
	declarations := []Declaration{
		{PluginID: "block", Root: root, Event: AfterToolUse, Command: command, Required: true, Env: helperEnv("result", map[string]string{"RESULT": `{"version":1,"block":true,"reason":"too late"}`})},
		{PluginID: "fail", Root: root, Event: AfterToolUse, Command: command, Required: true, Env: helperEnv("exit", nil)},
	}
	outcome := NewRunner(nil).Run(t.Context(), Set{Declarations: declarations}, Invocation{Version: 1, Event: AfterToolUse, ToolOutput: "completed"})
	if outcome.Blocked {
		t.Fatalf("after hook changed completed result: %#v", outcome)
	}
	if len(outcome.Records) != 2 {
		t.Fatalf("records = %#v", outcome.Records)
	}
}

func TestHookRejectsOversizedAndMalformedResult(t *testing.T) {
	root, command := hookTestCommand(t)
	for _, mode := range []string{"oversize", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			outcome := NewRunner(nil).Run(t.Context(), Set{Declarations: []Declaration{{PluginID: mode, Root: root, Event: BeforeToolUse, Command: command, Required: true, Env: helperEnv(mode, nil)}}}, Invocation{Version: 1, Event: BeforeToolUse})
			if !outcome.Blocked {
				t.Fatalf("%s result outcome = %#v, want fail closed", mode, outcome)
			}
		})
	}
}

func TestHookEnvironmentDoesNotLeakEngineSecrets(t *testing.T) {
	t.Setenv("ENGINE_SECRET", "engine-secret-value")
	root, command := hookTestCommand(t)
	secret := "configured-plugin-secret"
	declarations := []Declaration{
		{PluginID: "env", Root: root, Event: TurnStart, Command: command, Env: helperEnv("check-env", nil)},
		{PluginID: "redact", Root: root, Event: TurnStart, Command: command, Env: helperEnv("result", map[string]string{"RESULT": `{"version":1,"warning":"configured-plugin-secret","context":["configured-plugin-secret"]}`}), SecretValues: []string{secret}},
	}
	outcome := NewRunner(nil).Run(t.Context(), Set{Declarations: declarations}, Invocation{Version: 1, Event: TurnStart})
	joined := strings.Join(append(append([]string{}, outcome.Context...), outcome.Warnings...), " ")
	if strings.Contains(joined, "engine-secret-value") || strings.Contains(joined, secret) {
		t.Fatalf("secret leaked in outcome: %#v", outcome)
	}
	if !strings.Contains(joined, "environment-clean") || !strings.Contains(joined, "[REDACTED]") {
		t.Fatalf("safe/redacted outcome missing: %#v", outcome)
	}
}

func hookTestCommand(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "hook-helper")
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return root, "./hook-helper"
}

func helperEnv(mode string, extra map[string]string) map[string]string {
	env := map[string]string{"UMCODE_HOOK_TEST_MODE": mode}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func runHookTestHelper(mode string) {
	input, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "capture":
		_ = os.WriteFile(os.Getenv("CAPTURE_PATH"), input, 0o600)
		fmt.Print(`{"version":1,"continue":true}`)
	case "append":
		file, _ := os.OpenFile(os.Getenv("ORDER_PATH"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		_, _ = fmt.Fprintln(file, os.Getenv("MARK"))
		_ = file.Close()
		fmt.Print(`{"version":1,"continue":true}`)
	case "concurrency":
		counterURL := os.Getenv("COUNTER_URL")
		_, _ = http.Get(counterURL + "?event=start")
		milliseconds, _ := strconv.Atoi(os.Getenv("SLEEP_MS"))
		time.Sleep(time.Duration(milliseconds) * time.Millisecond)
		_, _ = http.Get(counterURL + "?event=end")
		fmt.Print(`{"version":1,"continue":true}`)
	case "exit":
		fmt.Fprint(os.Stderr, "fixture failure")
		os.Exit(9)
	case "result":
		fmt.Print(os.Getenv("RESULT"))
	case "oversize":
		fmt.Print(strings.Repeat("x", MaxStdout+1))
	case "malformed":
		fmt.Print("not json")
	case "check-env":
		if secret := os.Getenv("ENGINE_SECRET"); secret != "" {
			fmt.Printf(`{"version":1,"context":[%q]}`, "leaked:"+secret)
		} else {
			fmt.Print(`{"version":1,"context":["environment-clean"]}`)
		}
	default:
		os.Exit(2)
	}
}
