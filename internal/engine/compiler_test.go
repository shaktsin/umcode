package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// compilerEngine is a work engine whose thread has a goal, one planned
// criterion and a transcript of two earlier exchanges.
func compilerEngine(t *testing.T, on bool) (*Engine, protocol.Thread, protocol.Turn, *store.Store) {
	t.Helper()
	e, th, turn, st := workEngine(t)
	e.Cfg.Models.ContextCompiler = on
	// A real thread the compiler can improve on: many earlier exchanges, each
	// long enough that replaying them costs more than the work record.
	var history []struct{ kind, text string }
	for i := 0; i < 20; i++ {
		history = append(history,
			struct{ kind, text string }{protocol.ItemUserMessage, fmt.Sprintf("earlier request %d: %s", i, strings.Repeat("detail ", 40))},
			struct{ kind, text string }{protocol.ItemAgentMessage, fmt.Sprintf("earlier reply %d: %s", i, strings.Repeat("explanation ", 40))})
	}
	history = append(history,
		struct{ kind, text string }{protocol.ItemUserMessage, "add the parser"},
		struct{ kind, text string }{protocol.ItemAgentMessage, "Done. Which suite should I run?"},
		struct{ kind, text string }{protocol.ItemUserMessage, "the unit tests"},
		struct{ kind, text string }{protocol.ItemAgentMessage, "Running them."})
	for i, m := range history {
		seq, err := st.NextSeq(t.Context(), th.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SaveItem(t.Context(), protocol.Item{ID: fmt.Sprintf("itm-%d", i), ThreadID: th.ID, TurnID: "earlier",
			Seq: seq, Kind: m.kind, Status: protocol.ItemCompleted, Text: m.text, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	return e, th, turn, st
}

func compiled(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn) ([]llm.Message, bool) {
	t.Helper()
	hist, err := e.history(t.Context(), th.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := e.compile(t.Context(), th, turn.ID, 200000, estimateMessageTokens(hist), items)
	return out.msgs, ok
}

func joined(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role) + ":" + m.JoinedText() + "|")
	}
	return b.String()
}

func TestFlagOffRequestUnchanged(t *testing.T) {
	e, th, turn, _ := compilerEngine(t, false)
	if _, ok := compiled(t, e, th, turn); ok {
		t.Fatal("the compiler must not run with the flag off")
	}
	// With the flag off the request is exactly what the history path builds.
	hist, err := e.history(t.Context(), th.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.history(t.Context(), th.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if joined(hist) != joined(again) {
		t.Fatal("the history path is not deterministic")
	}
}

func TestFlagOnUsesPacketsAndShortTail(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.run", risk: tools.RiskYellow, output: testRunBad})
	msgs, ok := compiled(t, e, th, turn)
	if !ok {
		t.Fatal("compile declined")
	}
	s := joined(msgs)
	if !strings.Contains(s, "Current work state") || !strings.Contains(s, "## Work") {
		t.Fatalf("no packets: %s", s)
	}
	if !strings.Contains(s, "ship the change") || !strings.Contains(s, "go test ./...") {
		t.Fatalf("goal or criterion missing: %s", s)
	}
	if !strings.Contains(s, "the unit tests") {
		t.Fatalf("tail missing: %s", s)
	}
	_ = st
}

func TestFlagOnWithoutOpenWorkUsesHistory(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	works, _ := st.ListWorks(t.Context(), th.ID)
	if err := st.CloseWork(t.Context(), works[0].ID, protocol.WorkCompleted, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok := compiled(t, e, th, turn); ok {
		t.Fatal("with no open work the history path must be used")
	}
}

func TestCompileDeclineFallsBackAndCounts(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	works, _ := st.ListWorks(t.Context(), th.ID)
	if _, err := st.DB.ExecContext(t.Context(), `UPDATE works SET goal = '' WHERE id = ?`, works[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := compiled(t, e, th, turn); ok {
		t.Fatal("a work with no goal must decline")
	}
	if e.compilerFailures.Load() != 1 {
		t.Fatalf("compilerFailures = %d, want 1", e.compilerFailures.Load())
	}
}

func TestCompilerPanicRecovered(t *testing.T) {
	e, th, turn, _ := compilerEngine(t, true)
	compileHook = func() { panic("boom") }
	defer func() { compileHook = nil }()
	if _, ok := compiled(t, e, th, turn); ok {
		t.Fatal("a panic must fall back to history")
	}
	if e.compilerFailures.Load() == 0 {
		t.Fatal("a recovered panic must be counted")
	}
}

func TestMidTurnEditMakesCriterionStaleInNextCall(t *testing.T) {
	e, th, turn, _ := compilerEngine(t, true)
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.run", risk: tools.RiskYellow, output: testRunOK})
	msgs, ok := compiled(t, e, th, turn)
	if !ok {
		t.Fatal("compile declined")
	}
	if !strings.Contains(joined(msgs), "— passed") {
		t.Fatalf("expected a passing criterion first: %s", joined(msgs))
	}
	e.Tools.Add(&engineTestTool{name: "file.edit", risk: tools.RiskYellow})
	call := llm.ToolCall{ID: "c-edit", Name: tools.ToWire("file.edit"), Args: json.RawMessage(`{"path":"a.go"}`)}
	e.runTool(t.Context(), t.Context(), th, turn, call, &fakePluginSnapshot{})
	msgs, ok = compiled(t, e, th, turn)
	if !ok {
		t.Fatal("compile declined after the edit")
	}
	if s := joined(msgs); !strings.Contains(s, "needs re-run") {
		t.Fatalf("a mid-turn edit must make the pass stale: %s", s)
	}
}

func TestBreakdownReportsPacketTokens(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{llm.Text(llm.RoleUser, strings.Repeat("a", 4000))}}
	plain := measureRequest(nil, req, RequestPackets{}, nil)
	b := measureRequest(nil, req, RequestPackets{Work: 300, Evidence: 200}, nil)
	if b.WorkPacketTokens != 300 || b.EvidencePacketTokens != 200 {
		t.Fatalf("packet tokens = %+v", b)
	}
	if b.ConversationTokens != plain.ConversationTokens-500 {
		t.Fatalf("packet tokens must come out of conversation tokens: %+v vs %+v", b, plain)
	}
	if b.TotalTokens != plain.TotalTokens {
		t.Fatalf("total must be unchanged: %d vs %d", b.TotalTokens, plain.TotalTokens)
	}
}

func TestCompiledPrefixKeepsTheLiveTurnSuffix(t *testing.T) {
	prefix := []llm.Message{llm.Text(llm.RoleUser, "Current work state")}
	live := []llm.Message{
		llm.Text(llm.RoleUser, "do the thing"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "file__write"}}},
		{Role: llm.RoleTool, ToolCallID: "c1", ToolName: "file__write", Result: "ok"},
	}
	got := requestMessages(prefix, live)
	if len(got) != len(prefix)+len(live) {
		t.Fatalf("messages = %d, want %d", len(got), len(prefix)+len(live))
	}
	for i := range live {
		if got[len(prefix)+i].Role != live[i].Role {
			t.Fatalf("live suffix reordered at %d: %+v", i, got)
		}
	}
	if got[2].ToolCalls[0].ID != "c1" || got[3].ToolCallID != "c1" {
		t.Fatalf("a tool call was separated from its result: %+v", got)
	}
	// The caller's slices must not be aliased: a later append to the result
	// cannot be allowed to overwrite the live suffix.
	got = append(got, llm.Text(llm.RoleUser, "later"))
	if live[0].Parts[0].Text != "do the thing" {
		t.Fatalf("live suffix was mutated: %+v", live)
	}
}

func TestCompilerDeclinesAfterCompactionShrinksHistory(t *testing.T) {
	e, th, turn, _ := compilerEngine(t, true)
	runWorkTool(t, e, th, turn, &engineTestTool{name: "verification.plan", risk: tools.RiskGreen, output: testPlan})
	// A compacted thread is small: the compiler must not replace a short
	// history with a larger prefix.
	if _, ok := e.compile(t.Context(), th, turn.ID, 200000, 20, nil); ok {
		t.Fatal("a tiny history must make the compiler decline")
	}
	if e.compilerFailures.Load() == 0 {
		t.Fatal("the decline must be counted")
	}
}
