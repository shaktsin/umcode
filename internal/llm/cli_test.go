package llm

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	p := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func runCLI(t *testing.T, p Provider, req Request) (text string, calls []ToolCall, done Event, err error) {
	t.Helper()
	ch, serr := p.Stream(context.Background(), Credential{}, req)
	if serr != nil {
		return "", nil, done, serr
	}
	var b strings.Builder
	for e := range ch {
		switch e.Type {
		case EventTextDelta:
			b.WriteString(e.Text)
		case EventToolCall:
			calls = append(calls, *e.ToolCall)
		case EventDone:
			done = e
		case EventError:
			err = e.Err
		}
	}
	return b.String(), calls, done, err
}

var shellTool = []ToolSpec{{Name: "shell__run", Description: "Run a command", Schema: []byte(`{"type":"object"}`)}}

func TestClaudeSubscriptionStreamsTextAndParsesToolCalls(t *testing.T) {
	bin := fakeCLI(t, `cat >/dev/null
echo '{"type":"system","subtype":"init"}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Let me look. <tool"}}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"_call>{\"name\":\"shell__run\",\"arguments\":{\"command\":\"ls\"}}</tool_call>"}}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"x","usage":{"input_tokens":10,"cache_read_input_tokens":5,"output_tokens":7}}'
`)
	p := NewClaudeSubscription(func() string { return bin })
	text, calls, done, err := runCLI(t, p, Request{Model: "sonnet", System: "sys", Tools: shellTool, Messages: []Message{Text(RoleUser, "hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text) != "Let me look." {
		t.Fatalf("text = %q (tool markup leaked or text lost)", text)
	}
	if len(calls) != 1 || calls[0].Name != "shell__run" || !strings.Contains(string(calls[0].Args), `"ls"`) {
		t.Fatalf("calls = %+v", calls)
	}
	if done.StopReason != "tool_use" || done.Usage.InputTokens != 15 || done.Usage.CachedInputTokens != 5 || done.Usage.OutputTokens != 7 || !done.Usage.Reported {
		t.Fatalf("done = %+v", done)
	}
}

func TestClaudeSubscriptionPlainAnswerAndEnvStripped(t *testing.T) {
	bin := fakeCLI(t, `cat >/dev/null
[ -z "$ANTHROPIC_API_KEY" ] || { echo "api key leaked" >&2; exit 3; }
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Hello there"}]}}'
echo '{"type":"result","subtype":"success","usage":{"input_tokens":3,"output_tokens":2}}'
`)
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-pass")
	p := NewClaudeSubscription(func() string { return bin })
	text, calls, done, err := runCLI(t, p, Request{Messages: []Message{Text(RoleUser, "hi")}})
	if err != nil || text != "Hello there" || len(calls) != 0 || done.StopReason != "end_turn" {
		t.Fatalf("text=%q calls=%v done=%+v err=%v", text, calls, done, err)
	}
}

func TestClaudeSubscriptionErrorResult(t *testing.T) {
	bin := fakeCLI(t, `cat >/dev/null
echo '{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}'
exit 1
`)
	p := NewClaudeSubscription(func() string { return bin })
	_, _, _, err := runCLI(t, p, Request{Messages: []Message{Text(RoleUser, "hi")}})
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v", err)
	}
}

func TestChatGPTSubscriptionCodexEvents(t *testing.T) {
	bin := fakeCLI(t, `cat >/dev/null
echo '{"type":"thread.started","thread_id":"t"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"Done.\n<tool_call>{\"name\":\"shell__run\",\"arguments\":{\"command\":\"pwd\"}}</tool_call>"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":9}}'
`)
	p := NewChatGPTSubscription(func() string { return bin })
	text, calls, done, err := runCLI(t, p, Request{Model: "default", Tools: shellTool, Messages: []Message{Text(RoleUser, "hi")}})
	if err != nil || strings.TrimSpace(text) != "Done." || len(calls) != 1 || done.Usage.CachedInputTokens != 40 {
		t.Fatalf("text=%q calls=%v done=%+v err=%v", text, calls, done, err)
	}
}

func TestChatGPTSubscriptionFailure(t *testing.T) {
	bin := fakeCLI(t, `cat >/dev/null
echo '{"type":"error","message":"not signed in"}'
echo '{"type":"turn.failed","error":{"message":"not signed in"}}'
exit 1
`)
	p := NewChatGPTSubscription(func() string { return bin })
	_, _, _, err := runCLI(t, p, Request{Messages: []Message{Text(RoleUser, "hi")}})
	if err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
}

func TestSubscriptionNotInstalled(t *testing.T) {
	p := NewClaudeSubscription(func() string { return "" })
	if _, err := p.Stream(context.Background(), Credential{}, Request{}); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := p.ListModels(context.Background(), Credential{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTranscriptAndUnknownToolsIgnored(t *testing.T) {
	msgs := []Message{
		Text(RoleUser, "list files"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "shell__run", Args: []byte(`{"command":"ls"}`)}}},
		{Role: RoleTool, ToolCallID: "1", ToolName: "shell__run", Result: "a.txt"},
	}
	s := renderTranscript(msgs)
	for _, want := range []string{"[user]\nlist files", `<tool_call>{"name":"shell__run","arguments":{"command":"ls"}}</tool_call>`, "[tool result: shell__run]\na.txt"} {
		if !strings.Contains(s, want) {
			t.Errorf("transcript missing %q:\n%s", want, s)
		}
	}
	if got := parseToolCalls(`<tool_call>{"name":"rm_rf","arguments":{}}</tool_call>`, shellTool); len(got) != 0 {
		t.Errorf("unknown tool accepted: %+v", got)
	}
}

func TestToolFilterHoldsPartialMarker(t *testing.T) {
	f := &toolFilter{}
	var out string
	for _, c := range []string{"a <", "tool_c", "all>{}</tool_call>"} {
		out += f.write(c)
	}
	out += f.flush()
	if strings.TrimSpace(out) != "a" {
		t.Fatalf("out = %q", out)
	}
	g := &toolFilter{}
	out = g.write("1 < 2 and <b>bold</b>") + g.flush()
	if out != "1 < 2 and <b>bold</b>" {
		t.Fatalf("out = %q", out)
	}
}
