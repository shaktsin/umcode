package server_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/engine"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

func compilerOn(c *config.Config) { c.Models.ContextCompiler = true }

// chatItems runs one plain turn and returns the thread's items.
func chatItems(t *testing.T, mutate func(*config.Config)) []protocol.Item {
	t.Helper()
	h := newHarness(t, mutate)
	h.addKey("claude", "k", "sk-1")
	h.fake.push(textReply("Hello."))
	th := startWorkThread(h)
	if turn := runWorkTurn(h, th, "hi"); turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	var items protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: th.ID}, &items)
	return items.Items
}

func assertWorkflowPacket(t *testing.T, h *harness, identity, depth, semantic string) {
	t.Helper()
	if packet := workflowPacket(h, identity, depth, semantic); packet == "" {
		t.Fatalf("no compiled %s packet carries %s and %s", depth, identity, semantic)
	}
}

func workflowPacket(h *harness, identity, depth, semantic string) string {
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	for _, req := range h.fake.calls {
		for _, m := range req.Messages {
			for _, p := range m.Parts {
				if strings.Contains(p.Text, "Current work state") && strings.Contains(p.Text, "depth="+depth) && strings.Contains(p.Text, semantic) && strings.Contains(p.Text, identity) {
					return p.Text
				}
			}
		}
	}
	return ""
}

type workflowRequestLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (w *workflowRequestLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.Write(p)
}

func (w *workflowRequestLog) breakdowns(t *testing.T) []engine.RequestBreakdown {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []engine.RequestBreakdown
	for _, line := range strings.Split(w.Buffer.String(), "\n") {
		var record struct {
			Message   string                  `json:"msg"`
			Breakdown engine.RequestBreakdown `json:"breakdown"`
		}
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Message == "context accounting" {
			out = append(out, record.Breakdown)
		}
	}
	return out
}

func captureWorkflowRequests(h *harness) *workflowRequestLog {
	log := &workflowRequestLog{}
	h.eng.Log = slog.New(slog.NewJSONHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return log
}

// Pre-Phase-5 fixture: f0b401e exposed these tools, used the existing no-project
// prompt golden, completed plain work, and attributed no workflow overhead.
// Catches flag-off registration, prompt, lifecycle or request-accounting drift.
func TestDesignedWorkflowFlagOffCompatibility(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Models.DesignedWorkflow = false })
	log := captureWorkflowRequests(h)
	h.addKey("claude", "compatibility", "sk-1")
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{Title: "compatibility"}, &th)
	h.fake.push(textReply("Hello."))
	turn := runWorkTurn(h, th, "hi")
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn=%+v", turn)
	}
	d := workflowDetail(h, th.ID)
	if d.Work.Status != "completed" || d.Work.WorkflowDepth != "direct" || len(d.Nodes) != 1 || d.Nodes[0].Kind != "goal" {
		t.Fatalf("legacy lifecycle=%+v", d)
	}
	h.fake.mu.Lock()
	calls := append([]llm.Request(nil), h.fake.calls...)
	h.fake.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("plain turn added model calls: %d", len(calls))
	}
	req := calls[0]
	var names []string
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}
	wantNames := []string{"browser__verify", "exec__start", "exec__stop", "exec__write", "file__edit", "file__list", "file__read", "file__search", "file__write", "preview__start", "preview__stop", "shell__run", "skill__get_instructions", "skill__run_script", "task__cancel", "task__create", "task__list", "verification__plan", "verification__run", "visual__act", "visual__inspect", "visual__start", "visual__stop", "web__fetch", "web__search"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("legacy tool list=%q want=%q", names, wantNames)
	}
	golden, err := os.ReadFile(filepath.Join("..", "engine", "testdata", "system_prompt_noproject.txt"))
	if err != nil {
		t.Fatal(err)
	}
	clock := regexp.MustCompile(`Current time: [^\n]*\n`)
	if got := clock.ReplaceAllString(req.System, "Current time: <t>\n"); got != string(golden) {
		t.Fatalf("legacy prompt golden drift:\n%s", got)
	}
	breakdowns := log.breakdowns(t)
	if len(breakdowns) != 1 {
		t.Fatalf("request accounting=%+v", breakdowns)
	}
	b := breakdowns[0]
	if b.ToolCount != 25 || b.ToolSpecTokens != 2734 || b.Layers["core"] != 968 || b.Layers["notice"] != 44 || b.ConversationTokens != 5 || b.ToolResultTokens != 0 || b.WorkPacketTokens != 0 || b.EvidencePacketTokens != 0 || b.P0PacketTokens != 0 || b.P1PacketTokens != 0 || b.WorkUpdateSpecTokens != 0 || b.WorkUpdateCallTokens != 0 || b.WorkUpdateResultTokens != 0 || b.TotalTokens != b.SystemTokens+b.ToolSpecTokens+5 {
		t.Fatalf("legacy accounting=%+v", b)
	}
	var read protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: th.ID}, &read)
	if len(read.Items) != 2 || read.Items[0].Kind != protocol.ItemUserMessage || read.Items[1].Kind != protocol.ItemAgentMessage || read.Items[1].Text != "Hello." {
		t.Fatalf("legacy chat=%+v", read.Items)
	}
}

func TestFlagOffSendsNoWorkState(t *testing.T) {
	off := verifyThread(t, nil)
	for _, m := range off.Messages {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, "Current work state") {
				t.Fatal("the compiler ran with the flag off")
			}
		}
	}
	if requestChars(off) == 0 {
		t.Fatal("the history path sent nothing")
	}
}

func TestChatItemsUnchangedWithCompilerOnAndOff(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*config.Config)
	}{{"off", nil}, {"on", compilerOn}} {
		t.Run(c.name, func(t *testing.T) {
			items := chatItems(t, c.mutate)
			if len(items) != 2 || items[0].Kind != protocol.ItemUserMessage || items[1].Kind != protocol.ItemAgentMessage {
				t.Fatalf("items = %+v", items)
			}
		})
	}
}

// requestChars is the size of a request's messages, in characters.
func requestChars(req llm.Request) int {
	n := 0
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text)
		}
		n += len(m.Result)
	}
	return n
}

// verifyThread runs a thread with a real transcript and a work record, then one
// more turn, and returns that last request.
func verifyThread(t *testing.T, mutate func(*config.Config)) llm.Request {
	t.Helper()
	h := newHarness(t, mutate)
	h.call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: h.proj.ID,
		Tools: &protocol.ProjectTools{Network: boolPtr(true)}}, &h.proj)
	h.addKey("claude", "k", "sk-1")
	// A model with a real context window: with none, the engine compacts every
	// turn and there is no transcript left for the compiler to improve on.
	var th0 protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "sized"}, &th0)
	long := strings.Repeat("a detailed explanation of the parser change ", 40)
	// More exchanges than the tail keeps: until history exceeds the tail window
	// the compiler can only add to the request, and correctly declines.
	const turns = 14
	for i := 0; i < turns; i++ {
		h.fake.push(textReply("Explaining: " + long))
	}
	h.fake.push(toolReply("verification__plan", `{}`), textReply("Planned."))
	h.fake.push(textReply("Done."))

	th := th0
	h.call(protocol.MethodThreadSetSettings, protocol.ThreadSetSettingsParams{ThreadID: th.ID,
		Settings: protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-5", Complexity: "standard"}}, &th)
	for i := 0; i < turns; i++ {
		runWorkTurn(h, th, "tell me more, in detail: "+long)
	}
	runWorkTurn(h, th, "plan the checks")
	runWorkTurn(h, th, "now summarize")

	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if len(h.fake.calls) == 0 {
		t.Fatal("no model calls recorded")
	}
	return h.fake.calls[len(h.fake.calls)-1]
}

func TestCompiledRequestIsSmallerThanHistory(t *testing.T) {
	off := verifyThread(t, nil)
	on := verifyThread(t, compilerOn)
	var packets bool
	for _, m := range on.Messages {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, "Current work state") {
				packets = true
			}
		}
	}
	if !packets {
		t.Fatalf("the compiled request carries no work state (%d messages, %d chars)", len(on.Messages), requestChars(on))
	}
	if requestChars(on) >= requestChars(off) {
		t.Fatalf("compiled request %d chars is not smaller than the replayed transcript %d chars",
			requestChars(on), requestChars(off))
	}
}
