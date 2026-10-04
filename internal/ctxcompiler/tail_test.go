package ctxcompiler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

type itemOpt func(*protocol.Item)

func inTurn(id string) itemOpt { return func(it *protocol.Item) { it.TurnID = id } }
func status(s string) itemOpt  { return func(it *protocol.Item) { it.Status = s } }

var seq int64

func item(kind, text string, opts ...itemOpt) protocol.Item {
	seq++
	it := protocol.Item{ID: fmt.Sprintf("i%d", seq), Seq: seq, Kind: kind, Text: text,
		Status: protocol.ItemCompleted, TurnID: "old", CreatedAt: base.Add(time.Duration(seq) * time.Second)}
	for _, o := range opts {
		o(&it)
	}
	return it
}

// pairs builds n user/assistant exchanges, numbered from 1.
func pairs(n int) []protocol.Item {
	var out []protocol.Item
	for i := 1; i <= n; i++ {
		out = append(out, item(protocol.ItemUserMessage, fmt.Sprintf("u%d", i)))
		out = append(out, item(protocol.ItemAgentMessage, fmt.Sprintf("a%d", i)))
	}
	return out
}

func texts(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role) + ":" + m.Parts[0].Text + "|")
	}
	return b.String()
}

func TestTailKeepsLastTenPairs(t *testing.T) {
	msgs, _ := tail(pairs(30), "now", 0)
	users := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			users++
		}
	}
	if users != tailPairs {
		t.Fatalf("user messages = %d, want %d: %s", users, tailPairs, texts(msgs))
	}
	if !strings.Contains(texts(msgs), "u30") || strings.Contains(texts(msgs), "u20") {
		t.Fatalf("wrong window: %s", texts(msgs))
	}
}

func TestTailDropsToolAndFileChangeItems(t *testing.T) {
	items := []protocol.Item{
		item(protocol.ItemUserMessage, "u1"),
		item(protocol.ItemToolCall, ""),
		item(protocol.ItemFileChange, "wrote a.go"),
		item(protocol.ItemAgentMessage, "a1"),
	}
	items[1].Tool = &protocol.ToolCallData{Name: "file.write"}
	msgs, dropped := tail(items, "now", 0)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	if s := texts(msgs); strings.Contains(s, "earlier tool call") || strings.Contains(s, "wrote a.go") {
		t.Fatalf("tool or file-change text leaked: %s", s)
	}
}

func TestTailKeepsAnsweredQuestionOutsideWindow(t *testing.T) {
	items := []protocol.Item{
		item(protocol.ItemUserMessage, "u0"),
		item(protocol.ItemAgentMessage, "Which one should I use?"),
		item(protocol.ItemUserMessage, "the second one"),
	}
	items = append(items, pairs(10)...)
	msgs, _ := tail(items, "now", 0)
	s := texts(msgs)
	if !strings.Contains(s, "Which one should I use?") || !strings.Contains(s, "the second one") {
		t.Fatalf("answered question dropped: %s", s)
	}
}

func TestTailStartsAtNewestCompaction(t *testing.T) {
	items := pairs(3)
	items = append(items, item(protocol.ItemContextCompaction, "first summary"))
	items = append(items, pairs(2)...)
	items = append(items, item(protocol.ItemContextCompaction, "second summary"))
	items = append(items, item(protocol.ItemUserMessage, "after"), item(protocol.ItemAgentMessage, "ok"))
	msgs, _ := tail(items, "now", 0)
	s := texts(msgs)
	if !strings.Contains(s, "second summary") || strings.Contains(s, "first summary") || strings.Contains(s, "u1") {
		t.Fatalf("compaction not honored: %s", s)
	}
	if !strings.HasPrefix(s, "user:Earlier conversation summary") {
		t.Fatalf("summary must be the first user message: %s", s)
	}
}

func TestTailStartsWithUserAndAlternates(t *testing.T) {
	items := []protocol.Item{
		item(protocol.ItemAgentMessage, "a0"), // leading assistant must be dropped
		item(protocol.ItemUserMessage, "u1"),
		item(protocol.ItemUserMessage, "u2"), // consecutive same-role must merge
	}
	msgs, _ := tail(items, "now", 0)
	if msgs[0].Role != llm.RoleUser {
		t.Fatalf("first role = %s: %s", msgs[0].Role, texts(msgs))
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			t.Fatalf("roles do not alternate: %s", texts(msgs))
		}
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant || last.Parts[0].Text != "(no reply)" {
		t.Fatalf("a trailing user message needs the filler reply: %s", texts(msgs))
	}
	if !strings.Contains(texts(msgs), "u1\n\nu2") {
		t.Fatalf("same-role messages did not merge: %s", texts(msgs))
	}
}

func TestTailSkipsCurrentTurnAndInProgress(t *testing.T) {
	items := []protocol.Item{
		item(protocol.ItemUserMessage, "u1"),
		item(protocol.ItemAgentMessage, "a1"),
		item(protocol.ItemUserMessage, "current", inTurn("now")),
		item(protocol.ItemAgentMessage, "streaming", status(protocol.ItemInProgress)),
	}
	msgs, _ := tail(items, "now", 0)
	if s := texts(msgs); strings.Contains(s, "current") || strings.Contains(s, "streaming") {
		t.Fatalf("current turn or in-progress item included: %s", s)
	}
}

func TestTailDropsOldestPairsToFitBudget(t *testing.T) {
	items := pairs(10)
	full, _ := tail(items, "now", 0)
	budget := estimate(full) / 2
	small, _ := tail(items, "now", budget)
	if estimate(small) > budget {
		t.Fatalf("tail over budget: %d > %d", estimate(small), budget)
	}
	s := texts(small)
	if !strings.Contains(s, "u10") {
		t.Fatalf("newest pair must survive: %s", s)
	}
	if strings.Contains(s, "u1|") {
		t.Fatalf("oldest pair should have been dropped: %s", s)
	}
}
