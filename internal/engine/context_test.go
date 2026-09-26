package engine

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
)

func toolMsg(name string, size int) llm.Message {
	return llm.Message{Role: llm.RoleTool, ToolName: name, ToolCallID: name, Result: strings.Repeat("x", size)}
}

func TestTrimToolResultsKeepsRecentAndPairs(t *testing.T) {
	var msgs []llm.Message
	msgs = append(msgs, llm.Text(llm.RoleUser, "go"))
	for i := 0; i < 10; i++ {
		msgs = append(msgs, toolMsg("t"+string(rune('a'+i)), 40_000))
	}
	before := len(msgs)
	dropped := trimToolResults(msgs, 0, 30_000)
	if dropped == 0 || len(msgs) != before {
		t.Fatalf("dropped=%d len=%d", dropped, len(msgs))
	}
	for i, m := range msgs[len(msgs)-keepRecentToolResults:] {
		if strings.HasPrefix(m.Result, "[tool result omitted") {
			t.Fatalf("recent result %d trimmed", i)
		}
	}
	if !strings.HasPrefix(msgs[1].Result, "[tool result omitted") {
		t.Fatalf("oldest not trimmed: %.40s", msgs[1].Result)
	}
	if n := trimToolResults(msgs, 0, 10_000_000); n != 0 {
		t.Fatalf("trimmed under the limit: %d", n)
	}
}

func TestOverWindow(t *testing.T) {
	msgs := []llm.Message{llm.Text(llm.RoleUser, strings.Repeat("a", 4000))} // ~1000 tokens
	if overWindow(msgs, 0, 0, 0.5) {
		t.Error("unknown window must not trigger")
	}
	if !overWindow(msgs, 0, 1200, 0.7) || overWindow(msgs, 0, 100_000, 0.7) {
		t.Error("threshold wrong")
	}
}
