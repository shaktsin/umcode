package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/shaktsin/umcode/internal/procutil"
)

// Subscription providers run a chat turn through the official Claude Code or
// Codex command-line tool, using the sign-in that tool already holds. UMCode
// never reads or copies the OAuth token: it only starts the tool.
//
// The tools have their own agent loops and cannot be handed UMCode's tool
// definitions natively, so tool use is emulated: the prompt describes UMCode's
// tools and asks the model to reply with <tool_call> blocks, which are parsed
// back into tool-call events. The CLI's own tools are switched off (Claude
// Code) or confined to a read-only, empty scratch directory (Codex).

// Subscription provider ids; they match the identity ids.
const (
	ProviderClaudeSubscription = "claude_subscription"
	ProviderChatGPT            = "chatgpt"
)

// IsSubscription reports whether a provider id is a CLI-backed subscription.
func IsSubscription(id string) bool {
	return id == ProviderClaudeSubscription || id == ProviderChatGPT
}

// CLI is a provider backed by a command-line runtime.
type CLI struct {
	id     string
	locate func() string
	models []string
	// args builds the command line; the prompt is always sent on stdin.
	args func(req Request, dir string) []string
	// parse reads the runtime's stdout and emits events (without EventDone).
	parse func(r io.Reader, emit func(Event)) (Usage, error)
	// strip lists environment variables removed so the subscription sign-in is
	// used instead of an API key that happens to be exported.
	strip []string
}

// NewClaudeSubscription runs chats through `claude -p`.
func NewClaudeSubscription(locate func() string) *CLI {
	return &CLI{
		id: ProviderClaudeSubscription, locate: locate, models: []string{"sonnet", "opus", "haiku"},
		strip: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		args: func(req Request, dir string) []string {
			a := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
				"--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence",
				"--system-prompt", req.System + toolProtocol(req.Tools)}
			if m := req.Model; m != "" && m != "default" {
				a = append(a, "--model", m)
			}
			return a
		},
		parse: parseClaudeStream,
	}
}

// NewChatGPTSubscription runs chats through `codex exec`.
func NewChatGPTSubscription(locate func() string) *CLI {
	return &CLI{
		id: ProviderChatGPT, locate: locate, models: []string{"default"},
		strip: []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
		args: func(req Request, dir string) []string {
			a := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--ignore-rules",
				"-s", "read-only", "-C", dir, "-c", `approval_policy="never"`}
			if m := req.Model; m != "" && m != "default" {
				a = append(a, "-m", m)
			}
			return append(a, "-")
		},
		parse: parseCodexStream,
	}
}

func (c *CLI) ID() string { return c.id }

func (c *CLI) ListModels(ctx context.Context, _ Credential) ([]string, error) {
	if c.locate() == "" {
		return nil, errors.New("the runtime is not installed")
	}
	return append([]string(nil), c.models...), nil
}

func (c *CLI) Stream(ctx context.Context, _ Credential, req Request) (<-chan Event, error) {
	bin := c.locate()
	if bin == "" {
		return nil, errors.New("the Claude Code / Codex command-line tool is not installed")
	}
	dir, err := os.MkdirTemp("", "umcode-cli-*")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, c.args(req, dir)...)
	procutil.Prepare(cmd)
	cmd.Dir = dir
	cmd.Env = envWithout(os.Environ(), c.strip)
	prompt := c.prompt(req)
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer os.RemoveAll(dir)
		filter := &toolFilter{}
		emit := func(e Event) {
			if e.Type != EventTextDelta {
				out <- e
				return
			}
			if s := filter.write(e.Text); s != "" {
				out <- Event{Type: EventTextDelta, Text: s}
			}
		}
		usage, perr := c.parse(stdout, emit)
		_, _ = io.Copy(io.Discard, stdout)
		werr := cmd.Wait()
		if ctx.Err() != nil {
			out <- Event{Type: EventError, Err: ctx.Err()}
			return
		}
		if perr != nil {
			out <- Event{Type: EventError, Err: perr}
			return
		}
		if werr != nil && !filter.seen {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 500 {
				msg = msg[len(msg)-500:]
			}
			if msg == "" {
				msg = werr.Error()
			}
			out <- Event{Type: EventError, Err: fmt.Errorf("%s: %s", c.id, msg)}
			return
		}
		if tail := filter.flush(); tail != "" {
			out <- Event{Type: EventTextDelta, Text: tail}
		}
		calls := parseToolCalls(filter.full.String(), req.Tools)
		stop := "end_turn"
		for i := range calls {
			call := calls[i]
			out <- Event{Type: EventToolCall, ToolCall: &call}
			stop = "tool_use"
		}
		if !usage.Reported {
			usage.InputTokens = EstimateTokens(prompt)
			usage.OutputTokens = EstimateTokens(filter.full.String())
		}
		out <- Event{Type: EventDone, Usage: usage, StopReason: stop}
	}()
	return out, nil
}

func envWithout(env []string, names []string) []string {
	out := make([]string, 0, len(env))
outer:
	for _, kv := range env {
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

// prompt renders the conversation as the text the runtime is asked to continue.
// Claude Code carries the system prompt in a flag; Codex has none, so it goes first.
func (c *CLI) prompt(req Request) string {
	var b strings.Builder
	if c.id == ProviderChatGPT {
		b.WriteString(req.System)
		b.WriteString(toolProtocol(req.Tools))
		b.WriteString("\n\nDo not run commands or edit files yourself: you are only the language model; " +
			"the host application runs tools for you when you emit <tool_call> blocks.\n\n")
	}
	b.WriteString(renderTranscript(req.Messages))
	b.WriteString("\n[assistant]\n")
	return b.String()
}

func renderTranscript(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			b.WriteString("[user]\n")
			for _, p := range m.Parts {
				if p.Type == "image" {
					b.WriteString("[image omitted]\n")
				} else {
					b.WriteString(p.Text + "\n")
				}
			}
		case RoleAssistant:
			b.WriteString("[assistant]\n")
			if t := m.JoinedText(); t != "" {
				b.WriteString(t + "\n")
			}
			for _, tc := range m.ToolCalls {
				args := string(tc.Args)
				if args == "" {
					args = "{}"
				}
				fmt.Fprintf(&b, "<tool_call>{\"name\":%q,\"arguments\":%s}</tool_call>\n", tc.Name, args)
			}
		case RoleTool:
			status := "result"
			if m.IsError {
				status = "error"
			}
			fmt.Fprintf(&b, "[tool %s: %s]\n%s\n", status, m.ToolName, m.Result)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func toolProtocol(tools []ToolSpec) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n# Tools\n\nYou can call tools. To call one, reply with a block exactly like this and nothing after it:\n" +
		"<tool_call>{\"name\":\"TOOL_NAME\",\"arguments\":{...}}</tool_call>\n" +
		"You may emit several blocks in one reply. Use the exact tool names below and arguments matching each JSON schema. " +
		"After a tool runs you will see its result as a [tool result: NAME] message. When you need no tool, answer normally.\n\nAvailable tools:\n")
	for _, t := range tools {
		fmt.Fprintf(&b, "\n- %s: %s\n  schema: %s\n", t.Name, oneLine(t.Description), strings.TrimSpace(string(t.Schema)))
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ---- tool-call parsing ----------------------------------------------------

const toolOpen, toolClose = "<tool_call>", "</tool_call>"

var toolCallRe = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)

func parseToolCalls(text string, tools []ToolSpec) []ToolCall {
	known := map[string]bool{}
	for _, t := range tools {
		known[t.Name] = true
	}
	var calls []ToolCall
	for i, m := range toolCallRe.FindAllStringSubmatch(text, -1) {
		var v struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(m[1]), &v) != nil || !known[v.Name] {
			continue
		}
		if len(v.Arguments) == 0 || string(v.Arguments) == "null" {
			v.Arguments = json.RawMessage("{}")
		}
		calls = append(calls, ToolCall{ID: fmt.Sprintf("call_%d_%s", i, shortID(m[1])), Name: v.Name, Args: v.Arguments})
	}
	return calls
}

func shortID(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return fmt.Sprintf("%08x", h)
}

// toolFilter passes text through until a <tool_call> marker appears, holding
// back a possible partial marker at the end of a chunk. The whole text is kept
// in full so tool calls can be parsed at the end.
type toolFilter struct {
	full    strings.Builder
	pending string
	inCall  bool
	seen    bool // any output at all arrived
}

func (f *toolFilter) write(s string) string {
	if s == "" {
		return ""
	}
	f.seen = true
	f.full.WriteString(s)
	if f.inCall {
		return ""
	}
	f.pending += s
	if i := strings.Index(f.pending, toolOpen); i >= 0 {
		out := f.pending[:i]
		f.pending, f.inCall = "", true
		return strings.TrimRight(out, " \n")
	}
	// Hold back the longest suffix that could begin a marker.
	keep := 0
	for n := len(toolOpen) - 1; n > 0; n-- {
		if n <= len(f.pending) && strings.HasSuffix(f.pending, toolOpen[:n]) {
			keep = n
			break
		}
	}
	out := f.pending[:len(f.pending)-keep]
	f.pending = f.pending[len(f.pending)-keep:]
	return out
}

func (f *toolFilter) flush() string {
	if f.inCall {
		return ""
	}
	out := f.pending
	f.pending = ""
	return out
}

// ---- Claude Code stream-json ------------------------------------------------

func parseClaudeStream(r io.Reader, emit func(Event)) (Usage, error) {
	var usage Usage
	sawDelta := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
			Event   struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				CacheRead    int64 `json:"cache_read_input_tokens"`
				CacheCreate  int64 `json:"cache_creation_input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "stream_event":
			if ev.Event.Type == "content_block_delta" && ev.Event.Delta.Type == "text_delta" && ev.Event.Delta.Text != "" {
				sawDelta = true
				emit(Event{Type: EventTextDelta, Text: ev.Event.Delta.Text})
			}
		case "assistant":
			if !sawDelta {
				for _, c := range ev.Message.Content {
					if c.Type == "text" && c.Text != "" {
						emit(Event{Type: EventTextDelta, Text: c.Text})
					}
				}
			}
		case "result":
			if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
				msg := strings.TrimSpace(ev.Result)
				if msg == "" {
					msg = "Claude Code returned " + ev.Subtype
				}
				return usage, errors.New(msg)
			}
			u := ev.Usage
			usage = Usage{InputTokens: u.InputTokens + u.CacheRead + u.CacheCreate, CachedInputTokens: u.CacheRead,
				OutputTokens: u.OutputTokens, Reported: u.InputTokens+u.OutputTokens+u.CacheRead > 0}
		}
	}
	return usage, sc.Err()
}

// ---- Codex exec --json --------------------------------------------------------

func parseCodexStream(r io.Reader, emit func(Event)) (Usage, error) {
	var usage Usage
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				CachedInput  int64 `json:"cached_input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				Reasoning    int64 `json:"reasoning_output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				emit(Event{Type: EventTextDelta, Text: ev.Item.Text})
			}
		case "turn.completed":
			u := ev.Usage
			usage = Usage{InputTokens: u.InputTokens, CachedInputTokens: u.CachedInput, OutputTokens: u.OutputTokens,
				ReasoningTokens: u.Reasoning, Reported: u.InputTokens+u.OutputTokens > 0}
		case "turn.failed", "error":
			msg := firstNonEmpty(ev.Error.Message, ev.Message)
			if msg == "" {
				msg = "Codex reported an error"
			}
			return usage, errors.New(msg)
		}
	}
	return usage, sc.Err()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
