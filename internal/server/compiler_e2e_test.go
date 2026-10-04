package server_test

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
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
