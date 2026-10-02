package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/engine"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/server"
	"github.com/shaktsin/umcode/internal/store"
)

// fakeProvider replays scripted responses. Each call pops the next script.
type fakeProvider struct {
	id      string
	mu      sync.Mutex
	scripts []func(req llm.Request, key string) ([]llm.Event, error)
	calls   []llm.Request
	keys    []string
}

func (f *fakeProvider) ID() string { return f.id }

func (f *fakeProvider) Stream(ctx context.Context, cred llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.keys = append(f.keys, cred.APIKey)
	var script func(llm.Request, string) ([]llm.Event, error)
	if len(f.scripts) > 0 {
		script, f.scripts = f.scripts[0], f.scripts[1:]
	}
	f.mu.Unlock()
	if script == nil {
		script = textReply("title words")
	}
	evs, err := script(req, cred.APIKey)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.Event, len(evs))
	for _, e := range evs {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (f *fakeProvider) ListModels(ctx context.Context, cred llm.Credential) ([]string, error) {
	return []string{"fake-large", "fake-small"}, nil
}

// lastSystem returns the system prompt of the most recent call.
func (f *fakeProvider) lastSystem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].System != "" {
			return f.calls[i].System
		}
	}
	return ""
}

func (f *fakeProvider) push(s ...func(llm.Request, string) ([]llm.Event, error)) {
	f.mu.Lock()
	f.scripts = append(f.scripts, s...)
	f.mu.Unlock()
}

func textReply(text string) func(llm.Request, string) ([]llm.Event, error) {
	return func(llm.Request, string) ([]llm.Event, error) {
		return []llm.Event{
			{Type: llm.EventTextDelta, Text: text[:len(text)/2]},
			{Type: llm.EventTextDelta, Text: text[len(text)/2:]},
			{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 100, Reported: true}},
		}, nil
	}
}

func toolReply(name, args string) func(llm.Request, string) ([]llm.Event, error) {
	return func(llm.Request, string) ([]llm.Event, error) {
		return []llm.Event{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: name, Args: json.RawMessage(args)}},
			{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 500, OutputTokens: 20, Reported: true}},
		}, nil
	}
}

type harness struct {
	t    *testing.T
	fake *fakeProvider
	eng  *engine.Engine
	c    *client.Client
	ctx  context.Context
	ws   string
	proj protocol.Project
}

func newHarness(t *testing.T, mutate func(*config.Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("UMCODE_HOME", dir)
	cfg := config.Default(dir)
	socketDir, err := os.MkdirTemp("/tmp", "umc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	cfg.Runtime.SocketPath = filepath.Join(socketDir, fmt.Sprintf("%x.sock", time.Now().UnixNano()))
	// The project folder lives outside the engine's own data folder, as it does in real use.
	wsDir := t.TempDir()
	cfg.Tools.Workspaces = []config.WorkspaceConfig{{Name: "w", Path: wsDir, Default: true}}
	if mutate != nil {
		mutate(cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(dir, "umcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &fakeProvider{id: "claude"}
	reg := llm.NewRegistry()
	reg.Register(fake)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, Logger: log,
		SchedulerPoll: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(eng, log)
	if err := srv.ListenUnix(cfg.Runtime.SocketPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Close()
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		eng.Shutdown(sctx)
	})
	c, err := client.Dial(ctx, cfg.Runtime.SocketPath, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	h := &harness{t: t, fake: fake, eng: eng, c: c, ctx: ctx, ws: wsDir}
	// Chats work inside a project folder by default; isolated worktrees are opt-in.
	if err := os.MkdirAll(h.ws, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"}, {"config", "user.name", "UMCode Test"},
		{"config", "user.email", "test@umcode.invalid"},
		{"commit", "--allow-empty", "-m", "test project baseline"},
	} {
		cmd := exec.Command("git", append([]string{"-C", h.ws}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	h.call(protocol.MethodProjectCreate, protocol.ProjectCreateParams{Root: h.ws, Name: "demo",
		Tools: protocol.ProjectTools{}}, &h.proj)
	return h
}

func boolPtr(b bool) *bool { return &b }

func (h *harness) call(method string, params, out any) {
	h.t.Helper()
	if err := h.c.Call(h.ctx, method, params, out); err != nil {
		h.t.Fatalf("%s: %v", method, err)
	}
}

// callErr makes a call that is expected to be refused and returns the error.
func (h *harness) callErr(method string, params any) error {
	h.t.Helper()
	err := h.c.Call(h.ctx, method, params, nil)
	if err == nil {
		h.t.Fatalf("%s: expected an error", method)
	}
	return err
}

// waitTurn consumes notifications until the turn completes. onApproval decides approvals.
func (h *harness) waitTurn(turnID string, onApproval func(protocol.Approval) bool) (protocol.Turn, string, []protocol.Item) {
	h.t.Helper()
	var text strings.Builder
	var tools []protocol.Item
	timeout := time.After(10 * time.Second)
	for {
		select {
		case <-timeout:
			h.t.Fatal("timed out waiting for turn")
		case n := <-h.c.Notifications():
			switch n.Method {
			case protocol.NotifyItemDelta:
				var d protocol.ItemDelta
				json.Unmarshal(n.Params, &d)
				if d.TurnID == turnID {
					text.WriteString(d.Text)
				}
			case protocol.NotifyItemCompleted:
				var ev protocol.ItemEvent
				json.Unmarshal(n.Params, &ev)
				if ev.Item.TurnID == turnID && ev.Item.Kind == protocol.ItemToolCall {
					tools = append(tools, ev.Item)
				}
			case protocol.NotifyApprovalRequest:
				var ev protocol.ApprovalEvent
				json.Unmarshal(n.Params, &ev)
				ok := onApproval != nil && onApproval(ev.Approval)
				go h.c.Call(h.ctx, protocol.MethodApprovalRespond, protocol.ApprovalRespondParams{ApprovalID: ev.Approval.ID, Approve: ok}, nil)
			case protocol.NotifyTurnCompleted:
				var ev protocol.TurnEvent
				json.Unmarshal(n.Params, &ev)
				if ev.Turn.ID == turnID {
					return ev.Turn, text.String(), tools
				}
			}
		}
	}
}

func (h *harness) addKey(provider, label, secret string) protocol.Credential {
	var c protocol.Credential
	h.call(protocol.MethodCredentialAdd, protocol.CredentialAddParams{Provider: provider, Label: label, Secret: secret}, &c)
	return c
}

func TestChatWithApprovedShellTool(t *testing.T) {
	h := newHarness(t, nil)
	h.call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: h.proj.ID,
		Tools: &protocol.ProjectTools{Network: boolPtr(true)}}, &h.proj)
	key := h.addKey("claude", "personal", "sk-test-abcd1234")
	if key.Last4 != "1234" || !key.IsDefault {
		t.Fatalf("key = %+v", key)
	}
	h.fake.push(toolReply("shell__run", `{"command":"echo hello-from-shell > /dev/stderr; echo hello-from-shell"}`), textReply("The command printed hello."))

	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID}, &th)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "run echo please",
		Override: protocol.ModelSelection{Model: "claude-sonnet-4-6", Complexity: protocol.ComplexityDeep}}, &res)
	if res.Turn.Resolved.Provider != "claude" || res.Turn.Resolved.CredentialID != key.ID || res.Turn.Resolved.Complexity != "deep" {
		t.Fatalf("resolved = %+v", res.Turn.Resolved)
	}
	var approvals []protocol.Approval
	turn, text, tools := h.waitTurn(res.Turn.ID, func(a protocol.Approval) bool {
		approvals = append(approvals, a)
		return true
	})
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	if text != "The command printed hello." {
		t.Fatalf("text = %q", text)
	}
	if len(approvals) != 1 || approvals[0].Risk != "red" || !strings.Contains(approvals[0].ActionSummary, "echo hello-from-shell") {
		t.Fatalf("approvals = %+v", approvals)
	}
	if len(tools) != 1 || !strings.Contains(tools[0].Tool.Output, "hello-from-shell") {
		t.Fatalf("tools = %+v", tools)
	}
	// The second model call saw the tool result; deep → high reasoning with adaptive style.
	h.fake.mu.Lock()
	second := h.fake.calls[1]
	h.fake.mu.Unlock()
	last := second.Messages[len(second.Messages)-1]
	if last.Role != llm.RoleTool || !strings.Contains(last.Result, "hello-from-shell") {
		t.Fatalf("tool result not sent back: %+v", last)
	}
	if second.Reasoning != llm.ReasoningHigh || second.ReasoningStyle != "adaptive" {
		t.Fatalf("reasoning = %q/%q", second.Reasoning, second.ReasoningStyle)
	}
	// Usage: 2 chat calls. Sonnet 4.6: (800*3 + 200*0.3 + 100*15)/1e6 + (500*3 + 20*15)/1e6
	want := (800*3.0+200*0.3+100*15.0)/1e6 + (500*3.0+20*15.0)/1e6
	if turn.Usage.Requests != 2 || turn.Usage.InputTokens != 1500 || abs(turn.Usage.CostUSD-want) > 1e-12 {
		t.Fatalf("turn usage = %+v (want cost %v)", turn.Usage, want)
	}
	// Per-key summary includes the background title request.
	time.Sleep(200 * time.Millisecond)
	var sum protocol.UsageSummaryResult
	h.call(protocol.MethodUsageSummary, protocol.UsageSummaryParams{GroupBy: "credential"}, &sum)
	if len(sum.Rows) != 1 || sum.Rows[0].Key != key.ID || sum.Rows[0].Usage.Requests < 2 || !strings.Contains(sum.Rows[0].Label, "personal") {
		t.Fatalf("summary = %+v", sum)
	}
	var byRole protocol.UsageSummaryResult
	h.call(protocol.MethodUsageSummary, protocol.UsageSummaryParams{GroupBy: "role"}, &byRole)
	roles := map[string]int64{}
	for _, r := range byRole.Rows {
		roles[r.Key] = r.Usage.Requests
	}
	if roles["chat"] != 2 || roles["title"] != 1 {
		t.Fatalf("roles = %v", roles)
	}
	// Title and history.
	var read protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: th.ID}, &read)
	if read.Thread.Title != "title words" {
		t.Fatalf("title = %q", read.Thread.Title)
	}
	var hits protocol.ThreadSearchResult
	h.call(protocol.MethodThreadSearch, protocol.ThreadSearchParams{Query: "hello-from"}, &hits)
	if len(hits.Hits) == 0 || hits.Hits[0].ThreadID != th.ID {
		t.Fatalf("search = %+v", hits)
	}
	var exp protocol.ThreadExportResult
	h.call(protocol.MethodThreadExport, protocol.ThreadIDParams{ThreadID: th.ID}, &exp)
	if !strings.Contains(exp.Markdown, "## You\n\nrun echo please") {
		t.Fatalf("export = %s", exp.Markdown)
	}
}

func TestDeniedToolAndHistory(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	h.fake.push(toolReply("shell__run", `{"command":"rm -rf /tmp/x"}`), textReply("Okay, I will not."))
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "cleanup"}, &th)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "delete it"}, &res)
	turn, _, tools := h.waitTurn(res.Turn.ID, func(protocol.Approval) bool { return false })
	if turn.Status != protocol.TurnCompleted || len(tools) != 1 || tools[0].Status != protocol.ItemDenied {
		t.Fatalf("turn=%+v tools=%+v", turn, tools)
	}
	// Second turn sends earlier text history and uses quick for a short message (auto).
	h.fake.push(textReply("hi!"))
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "hi"}, &res)
	if !res.Turn.AutoPicked || res.Turn.Resolved.Complexity != protocol.ComplexityQuick {
		t.Fatalf("auto: %+v", res.Turn)
	}
	h.waitTurn(res.Turn.ID, nil)
	h.fake.mu.Lock()
	req := h.fake.calls[len(h.fake.calls)-1]
	h.fake.mu.Unlock()
	if req.Messages[0].JoinedText() != "delete it" || req.Reasoning != llm.ReasoningOff {
		t.Fatalf("history/reasoning: %+v", req)
	}
}

func TestAgentFixLoop(t *testing.T) {
	h := newHarness(t, nil)
	h.call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: h.proj.ID,
		Tools: &protocol.ProjectTools{Network: boolPtr(true)}}, &h.proj)
	h.addKey("claude", "verification", "sk-verify")
	h.fake.push(
		toolReply("verification__plan", `{}`),
		toolReply("file__write", `{"path":"result.txt","content":"broken\n"}`),
		toolReply("verification__run", `{"checks":[{"label":"content test","command":"test \"$(cat result.txt)\" = fixed","reason":"Confirm the generated result is correct."}]}`),
		toolReply("file__write", `{"path":"result.txt","content":"fixed\n"}`),
		toolReply("verification__run", `{"checks":[{"label":"content test","command":"test \"$(cat result.txt)\" = fixed","reason":"Rerun the failed check after the fix."}]}`),
		textReply("Fixed the failure and reran the check successfully."),
	)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "verification loop"}, &th)
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "make result.txt correct and verify it"}, &started)
	turn, text, items := h.waitTurn(started.Turn.ID, func(protocol.Approval) bool { return true })
	if turn.Status != protocol.TurnCompleted || !strings.Contains(text, "reran the check successfully") {
		t.Fatalf("turn=%+v text=%q", turn, text)
	}
	var statuses []string
	for _, item := range items {
		if item.Tool != nil && item.Tool.Name == "verification.run" {
			var result struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(item.Tool.Output), &result); err != nil {
				t.Fatal(err)
			}
			statuses = append(statuses, result.Status)
		}
	}
	if !slices.Equal(statuses, []string{"failed", "passed"}) {
		t.Fatalf("verification statuses = %v; items=%+v", statuses, items)
	}
	data, err := os.ReadFile(filepath.Join(h.ws, "result.txt"))
	if err != nil || string(data) != "fixed\n" {
		t.Fatalf("result.txt = %q, %v", data, err)
	}
}

func TestBudgetHardStopAndFallback(t *testing.T) {
	h := newHarness(t, nil)
	k1 := h.addKey("claude", "primary", "sk-primary")
	k2 := h.addKey("claude", "backup", "sk-backup")
	fb := true
	h.call(protocol.MethodCredentialUpdate, protocol.CredentialUpdateParams{CredentialID: k2.ID, Fallback: &fb}, nil)

	// Primary is rate limited → backup answers.
	h.fake.push(func(llm.Request, string) ([]llm.Event, error) {
		return nil, &llm.Error{Provider: "claude", Status: 429, Body: "rate limited"}
	}, textReply("from backup"))
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "x"}, &th)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello there friend"}, &res)
	turn, text, _ := h.waitTurn(res.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || text != "from backup" {
		t.Fatalf("fallback failed: %+v %q", turn, text)
	}
	h.fake.mu.Lock()
	keys := append([]string(nil), h.fake.keys...)
	h.fake.mu.Unlock()
	if keys[0] != "sk-primary" || keys[1] != "sk-backup" {
		t.Fatalf("keys used = %v", keys)
	}
	var sum protocol.UsageSummaryResult
	h.call(protocol.MethodUsageSummary, protocol.UsageSummaryParams{GroupBy: "credential"}, &sum)
	byKey := map[string]protocol.UsageTotals{}
	for _, r := range sum.Rows {
		byKey[r.Key] = r.Usage
	}
	if byKey[k1.ID].Requests != 1 || byKey[k1.ID].InputTokens != 0 || byKey[k2.ID].InputTokens != 1000 {
		t.Fatalf("per-key usage = %+v", byKey)
	}

	// The turn records which routes it used: the rate-limited key, then the one
	// that answered.
	if len(turn.RouteTrail) != 2 || turn.RouteTrail[0].Status != "rate_limited" ||
		turn.RouteTrail[1].Status != "used" || turn.RouteTrail[1].CredentialID != k2.ID {
		t.Fatalf("route trail = %+v", turn.RouteTrail)
	}

	// Hard stop: pinning a key whose tiny budget is already spent refuses the
	// turn rather than quietly billing the other key.
	h.call(protocol.MethodUsageSetBudget, protocol.UsageSetBudgetParams{CredentialID: k2.ID, MonthlyBudgetUSD: 0.000001, HardStop: true}, nil)
	err := h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "again please now",
		Override: protocol.ModelSelection{CredentialID: k2.ID}})
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget not enforced: %v", err)
	}
}

func TestThreadManagementAndModels(t *testing.T) {
	h := newHarness(t, nil)
	k := h.addKey("claude", "k", "sk-xyz9")
	var a, b protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "alpha"}, &a)
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "beta"}, &b)
	h.call(protocol.MethodThreadPin, protocol.ThreadFlagParams{ThreadID: a.ID, Value: true}, nil)
	h.call(protocol.MethodThreadArchive, protocol.ThreadFlagParams{ThreadID: b.ID, Value: true}, nil)
	var list protocol.ThreadListResult
	h.call(protocol.MethodThreadList, protocol.ThreadListParams{}, &list)
	if len(list.Threads) != 1 || list.Threads[0].ID != a.ID || !list.Threads[0].Pinned {
		t.Fatalf("list = %+v", list)
	}
	var set protocol.Thread
	h.call(protocol.MethodThreadSetSettings, protocol.ThreadSetSettingsParams{ThreadID: a.ID,
		Settings: protocol.ModelSelection{Model: "fake-large", Complexity: "standard", CredentialID: k.ID}}, &set)
	if set.Settings.Provider != "claude" || set.Settings.Model != "fake-large" {
		t.Fatalf("settings = %+v", set.Settings)
	}
	if err := h.c.Call(h.ctx, protocol.MethodThreadSetSettings, protocol.ThreadSetSettingsParams{ThreadID: a.ID,
		Settings: protocol.ModelSelection{Provider: "openai", CredentialID: k.ID}}, nil); err == nil {
		t.Fatal("expected provider/key mismatch error")
	}
	var test protocol.CredentialTestResult
	h.call(protocol.MethodCredentialTest, protocol.CredentialIDParams{CredentialID: k.ID}, &test)
	if !test.OK || len(test.Models) != 2 {
		t.Fatalf("test = %+v", test)
	}
	var models protocol.ModelListResult
	h.call(protocol.MethodModelList, protocol.ModelListParams{Provider: "claude"}, &models)
	found := false
	for _, m := range models.Models {
		found = found || (m.ID == "fake-large" && m.Source == "api")
	}
	if !found {
		t.Fatalf("api models not merged: %+v", models)
	}
	var d protocol.ComplexityDefaults
	h.call(protocol.MethodComplexityGetDefaults, nil, &d)
	d.Default = protocol.ComplexityStandard
	d.Limits.MaxToolRounds = 50
	h.call(protocol.MethodComplexitySetDefaults, d, &d)
	if d.Default != "standard" || d.Limits.MaxToolRounds != 50 || d.Presets[0].MaxToolSteps != 50 {
		t.Fatalf("complexity = %+v", d)
	}
	var creds protocol.CredentialListResult
	h.call(protocol.MethodCredentialList, nil, &creds)
	raw, _ := json.Marshal(creds)
	if strings.Contains(string(raw), "sk-xyz9") {
		t.Fatal("secret leaked in credential/list")
	}
	h.call(protocol.MethodThreadDelete, protocol.ThreadIDParams{ThreadID: b.ID}, nil)
	if err := h.c.Call(h.ctx, protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: b.ID}, nil); err == nil {
		t.Fatal("deleted thread still readable")
	}
}

func TestProjectContainment(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("top secret"), 0o600)
	h.fake.push(toolReply("file__read", `{"path":"`+outside+`"}`), textReply("could not read it"))
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "x"}, &th)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "read that file for me please"}, &res)
	_, _, tools := h.waitTurn(res.Turn.ID, nil)
	if len(tools) != 1 || tools[0].Status != protocol.ItemFailed || !strings.Contains(tools[0].Tool.Error, "outside the project") {
		t.Fatalf("tools = %+v", tools)
	}
}

// A chat with a project: edits land inside it, show up as fileChange items with
// a diff, and can be undone. AGENTS.md in the project reaches the system prompt.
func TestProjectEditsDiffAndRevert(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	if err := os.WriteFile(filepath.Join(h.ws, "notes.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "AGENTS.md"), []byte("Always answer in haiku."), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fake.push(
		toolReply("file__write", `{"path":"notes.txt","content":"one\ntwo\nthree\n"}`),
		textReply("Added a line."),
	)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "edit"}, &th)
	if th.ProjectID != h.proj.ID {
		t.Fatalf("thread project = %q", th.ProjectID)
	}
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "add a third line"}, &res)
	turn, _, _ := h.waitTurn(res.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	if data, _ := os.ReadFile(filepath.Join(h.ws, "notes.txt")); string(data) != "one\ntwo\nthree\n" {
		t.Fatalf("default project-folder workspace was not modified = %q", data)
	}
	// The project's AGENTS.md is part of the prompt the model saw.
	if sys := h.fake.lastSystem(); !strings.Contains(sys, "Always answer in haiku.") || !strings.Contains(sys, h.ws) {
		t.Fatalf("system prompt = %q", sys)
	}
	// The edit is an item in the chat, with a diff.
	var read protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: th.ID}, &read)
	var change protocol.FileChangeData
	found := false
	for _, it := range read.Items {
		if it.Kind == protocol.ItemFileChange {
			json.Unmarshal(it.Data, &change)
			found = true
		}
	}
	if !found || change.Path != "notes.txt" || change.Additions != 1 || !strings.Contains(change.Diff, "+three") {
		t.Fatalf("fileChange = %+v (found %v)", change, found)
	}
	// project/diff reports the same change…
	var diff protocol.ProjectDiffResult
	h.call(protocol.MethodProjectDiff, protocol.ProjectDiffParams{ProjectID: h.proj.ID, TurnID: turn.ID}, &diff)
	if len(diff.Files) != 1 || diff.Files[0].Path != "notes.txt" {
		t.Fatalf("diff = %+v", diff)
	}
	// …and reverting the turn puts the file back.
	var rev protocol.ProjectRevertTurnResult
	h.call(protocol.MethodProjectRevertTurn, protocol.ProjectRevertTurnParams{TurnID: turn.ID}, &rev)
	if len(rev.Reverted) != 1 {
		t.Fatalf("revert = %+v", rev)
	}
	if data, _ := os.ReadFile(filepath.Join(h.ws, "notes.txt")); string(data) != "one\ntwo\n" {
		t.Fatalf("after revert = %q", data)
	}
}

func TestNonGitCoding(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	root := t.TempDir() // Deliberately not a Git repository.
	var project protocol.Project
	h.call(protocol.MethodProjectCreate, protocol.ProjectCreateParams{Root: root, Name: "plain folder"}, &project)
	h.fake.push(
		toolReply("file__write", `{"path":"hello.txt","content":"worked without git\n"}`),
		textReply("Created the file."),
	)
	var thread protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: project.ID}, &thread)
	if thread.WorkspaceMode != "local" {
		t.Fatalf("new project chat workspace mode = %q, want local", thread.WorkspaceMode)
	}
	// Simulate a pre-workspace-picker chat upgraded from the previous build.
	if err := h.eng.Store.UpdateThread(h.ctx, thread.ID, map[string]any{"workspace_mode": "legacy"}); err != nil {
		t.Fatal(err)
	}
	var opened protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: thread.ID}, &opened)
	if opened.Thread.WorkspaceMode != "local" {
		t.Fatalf("legacy chat without a saved worktree resolved to %q, want local", opened.Thread.WorkspaceMode)
	}
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: thread.ID, Text: "create hello.txt"}, &started)
	turn, _, _ := h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil || string(data) != "worked without git\n" {
		t.Fatalf("file from non-Git coding task = %q, %v", data, err)
	}
}

func TestKeepDropTask(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	if err := os.WriteFile(filepath.Join(h.ws, "notes.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fake.push(
		toolReply("file__write", `{"path":"notes.txt","content":"original\ntask edit\n"}`),
		textReply("Updated the task copy."),
	)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "keep", WorkspaceMode: "worktree"}, &th)
	var start protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "edit notes"}, &start)
	turn, _, _ := h.waitTurn(start.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	if err := h.eng.Store.UpdateThread(h.ctx, th.ID, map[string]any{"workspace_mode": "legacy"}); err != nil {
		t.Fatal(err)
	}
	var file protocol.ProjectReadFileResult
	h.call(protocol.MethodProjectReadFile, protocol.ProjectReadFileParams{ProjectID: h.proj.ID, ThreadID: th.ID, Path: "notes.txt"}, &file)
	if file.Content != "original\ntask edit\n" {
		t.Fatalf("task-scoped file read = %q", file.Content)
	}
	if saved, err := h.eng.Store.GetThread(h.ctx, th.ID); err != nil || saved.WorkspaceMode != "worktree" {
		t.Fatalf("legacy chat with a saved worktree resolved to %+v, %v", saved, err)
	}
	if data, _ := os.ReadFile(filepath.Join(h.ws, "notes.txt")); string(data) != "original\n" {
		t.Fatalf("task edit escaped into source before approval = %q", data)
	}
	var kept protocol.TaskWorkspaceResult
	h.call(protocol.MethodProjectKeepTask, protocol.TaskWorkspaceParams{ThreadID: th.ID}, &kept)
	if kept.ChangedFiles != 1 {
		t.Fatalf("keep result = %+v", kept)
	}
	if data, _ := os.ReadFile(filepath.Join(h.ws, "notes.txt")); string(data) != "original\ntask edit\n" {
		t.Fatalf("kept edit did not reach source = %q", data)
	}
	workspace, err := h.eng.Worktrees.Ensure(h.ctx, th.ID, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "unkept.txt"), []byte("discard me"), 0o644); err != nil {
		t.Fatal(err)
	}
	var discarded protocol.TaskWorkspaceResult
	h.call(protocol.MethodProjectDiscardTask, protocol.TaskWorkspaceParams{ThreadID: th.ID}, &discarded)
	if _, err := os.Stat(workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("discard did not remove task workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "unkept.txt")); !os.IsNotExist(err) {
		t.Fatalf("discard copied unkept data into source: %v", err)
	}
}

// Without a project a chat can read and answer, but not change anything.
func TestChatWithoutProjectIsReadOnly(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	h.fake.push(toolReply("file__write", `{"path":"x.txt","content":"nope"}`), textReply("I cannot write without a project."))
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{Title: "loose"}, &th)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "write a file"}, &res)
	_, _, tools := h.waitTurn(res.Turn.ID, nil)
	if len(tools) != 1 || !strings.Contains(tools[0].Tool.Error, "no project") {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestProjectMethods(t *testing.T) {
	h := newHarness(t, nil)
	// Listing, opening and instructions.
	var list protocol.ProjectListResult
	h.call(protocol.MethodProjectList, protocol.ProjectListParams{}, &list)
	resolvedWS, _ := filepath.EvalSymlinks(h.ws)
	if len(list.Projects) != 1 || list.Projects[0].Root != resolvedWS {
		t.Fatalf("projects = %+v", list.Projects)
	}
	var ins protocol.ProjectInstructionsResult
	text := "Run the tests with `make test`."
	h.call(protocol.MethodProjectInstructions, protocol.ProjectInstructionsParams{ProjectID: h.proj.ID, Content: &text}, &ins)
	if ins.Path != filepath.Join(resolvedWS, "UMCODE.md") || !strings.Contains(ins.Composed, "make test") {
		t.Fatalf("instructions = %+v", ins)
	}
	// A repo written for Codex or Claude Code works unchanged.
	os.Remove(filepath.Join(h.ws, "UMCODE.md"))
	os.WriteFile(filepath.Join(h.ws, "CLAUDE.md"), []byte("Prefer small commits."), 0o644)
	h.call(protocol.MethodProjectInstructions, protocol.ProjectInstructionsParams{ProjectID: h.proj.ID}, &ins)
	if !strings.Contains(ins.Composed, "Prefer small commits.") {
		t.Fatalf("CLAUDE.md fallback: %+v", ins)
	}
	// Files and reads are scoped to the project.
	os.MkdirAll(filepath.Join(h.ws, "src"), 0o755)
	os.WriteFile(filepath.Join(h.ws, "src", "main.go"), []byte("package main\n"), 0o644)
	var files protocol.ProjectFilesResult
	h.call(protocol.MethodProjectFiles, protocol.ProjectFilesParams{ProjectID: h.proj.ID, Depth: 2}, &files)
	var paths []string
	for _, e := range files.Entries {
		paths = append(paths, e.Path)
	}
	if !slices.Contains(paths, "src/main.go") {
		t.Fatalf("files = %v", paths)
	}
	var file protocol.ProjectReadFileResult
	h.call(protocol.MethodProjectReadFile, protocol.ProjectReadFileParams{ProjectID: h.proj.ID, Path: "src/main.go"}, &file)
	if file.Content != "package main\n" {
		t.Fatalf("read = %+v", file)
	}
	if err := h.c.Call(h.ctx, protocol.MethodProjectReadFile,
		protocol.ProjectReadFileParams{ProjectID: h.proj.ID, Path: "../escape.txt"}, &file); err == nil {
		t.Fatal("read outside the project was allowed")
	}
	// Renaming and archiving.
	name := "renamed"
	var updated protocol.Project
	h.call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: h.proj.ID, Name: &name}, &updated)
	if updated.Name != "renamed" {
		t.Fatalf("update = %+v", updated)
	}
	// A folder can only be a project once.
	if err := h.c.Call(h.ctx, protocol.MethodProjectCreate, protocol.ProjectCreateParams{Root: h.ws}, nil); err == nil {
		t.Fatal("duplicate project was allowed")
	}
	h.call(protocol.MethodProjectDelete, protocol.ProjectIDParams{ProjectID: h.proj.ID}, nil)
	h.call(protocol.MethodProjectList, protocol.ProjectListParams{}, &list)
	if len(list.Projects) != 0 {
		t.Fatalf("after delete: %+v", list.Projects)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestWebSocketTransport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UMCODE_HOME", dir)
	cfg := config.Default(dir)
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(eng, log)
	tokenPath := filepath.Join(dir, "token")
	if err := srv.ListenWebSocket("127.0.0.1", 18767, tokenPath); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	tok, _ := os.ReadFile(tokenPath)

	// Wrong token is rejected.
	if _, _, err := websocket.Dial(ctx, "ws://127.0.0.1:18767/ws?token=nope", nil); err == nil {
		t.Fatal("expected unauthorized")
	}
	bearer := strings.TrimSpace(string(tok))
	// Browser origins: the Mac app's webview and local dev servers are allowed; other sites are not.
	for origin, ok := range map[string]bool{"wails://wails": true, "wails://wails.localhost": true,
		"http://localhost:5173": true, "https://evil.example": false} {
		c, _, err := websocket.Dial(ctx, "ws://127.0.0.1:18767/ws?token="+bearer, &websocket.DialOptions{
			HTTPHeader: map[string][]string{"Origin": {origin}}})
		if (err == nil) != ok {
			t.Fatalf("origin %s: err=%v, want allowed=%v", origin, err, ok)
		}
		if c != nil {
			c.CloseNow()
		}
	}
	ws, _, err := websocket.Dial(ctx, "ws://127.0.0.1:18767/ws", &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Authorization": {"Bearer " + bearer}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	send := func(id int, method string, params any) map[string]any {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(data, &m)
		return m
	}
	if m := send(1, "engine/status", nil); m["error"] == nil {
		t.Fatal("expected initialize-first error")
	}
	if m := send(2, "initialize", protocol.InitializeParams{ClientName: "ws", ProtocolVersion: "2.0.0"}); m["error"] == nil {
		t.Fatal("expected version mismatch")
	}
	send(3, "initialize", protocol.InitializeParams{ClientName: "ws", ProtocolVersion: protocol.Version})
	m := send(4, "engine/status", nil)
	if res, _ := m["result"].(map[string]any); res == nil || res["protocolVersion"] != protocol.Version {
		t.Fatalf("status = %v", m)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestScheduledTasks(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	h.fake.push(textReply("Inbox: 3 new emails."))
	var task protocol.Task
	min := 7
	h.call(protocol.MethodTaskCreate, protocol.TaskCreateParams{Name: "inbox", Prompt: "summarise my inbox",
		TaskType: protocol.TaskPeriodic, Schedule: protocol.Schedule{Frequency: "hourly", Minute: &min}, Timezone: "UTC",
		Settings: protocol.ModelSelection{Complexity: protocol.ComplexityQuick}}, &task)
	if task.NextRunAt == nil || task.NextRunAt.Minute() != 7 || task.Status != "active" {
		t.Fatalf("task = %+v", task)
	}
	h.call(protocol.MethodTaskRunNow, protocol.TaskIDParams{TaskID: task.ID}, nil)
	var runs protocol.TaskRunsResult
	waitFor(t, "task run", func() bool {
		h.call(protocol.MethodTaskRuns, protocol.TaskIDParams{TaskID: task.ID}, &runs)
		return len(runs.Runs) == 1 && runs.Runs[0].Status != "running"
	})
	run := runs.Runs[0]
	if run.Status != "success" || run.Result != "Inbox: 3 new emails." || run.TurnID == "" {
		t.Fatalf("run = %+v", run)
	}
	var list protocol.TaskListResult
	h.call(protocol.MethodTaskList, protocol.TaskListParams{}, &list)
	got := list.Tasks[0]
	if got.Status != "active" || got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) || got.ThreadID == "" || got.LastResult != run.Result {
		t.Fatalf("after run = %+v", got)
	}
	var read protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: got.ThreadID}, &read)
	if read.Thread.Title != "Task: inbox" || read.Thread.Channel != "task" || read.Turns[0].Resolved.Complexity != "quick" {
		t.Fatalf("task thread = %+v turns=%+v", read.Thread, read.Turns)
	}

	// One-time task completes after its run.
	h.fake.push(textReply("done"))
	var once protocol.Task
	h.call(protocol.MethodTaskCreate, protocol.TaskCreateParams{Prompt: "remind me", TaskType: protocol.TaskOneTime,
		Schedule: protocol.Schedule{RunAt: time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)}}, &once)
	h.call(protocol.MethodTaskRunNow, protocol.TaskIDParams{TaskID: once.ID}, nil)
	waitFor(t, "one-time completion", func() bool {
		h.call(protocol.MethodTaskList, protocol.TaskListParams{Status: "completed"}, &list)
		return len(list.Tasks) == 1 && list.Tasks[0].ID == once.ID
	})
	if err := h.c.Call(h.ctx, protocol.MethodTaskCreate, protocol.TaskCreateParams{Prompt: "x", TaskType: protocol.TaskOneTime,
		Schedule: protocol.Schedule{RunAt: "2020-01-01T00:00"}}, nil); err == nil {
		t.Fatal("past one-time task accepted")
	}
	h.call(protocol.MethodTaskCancel, protocol.TaskIDParams{TaskID: task.ID}, &task)
	if task.Status != "cancelled" {
		t.Fatalf("cancel = %+v", task)
	}
}

func TestAgentUsesSkillAndCreatesTask(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	home := h.eng.Cfg.Home
	dir := filepath.Join(home, "skills", "greeter")
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: greeter\ndescription: Greet people by name\nrisk_level: green\nscripts:\n  greet:\n    path: scripts/greet.sh\n    description: greet\n---\nCall greet.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "scripts", "greet.sh"), []byte("#!/bin/bash\nread -r j\necho \"hello from skill $j\"\n"), 0o755)

	h.fake.push(
		toolReply("skill__run_script", `{"skill":"greeter","script":"greet","args":{"name":"Ada"}}`),
		toolReply("task__create", `{"name":"standup","prompt":"post standup notes","task_type":"periodic","frequency":"weekly","day_of_week":"mon","time":"09:30","timezone":"Europe/Berlin"}`),
		textReply("Greeted Ada and scheduled standup."),
	)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "x"}, &th)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "please greet Ada by name and schedule standup"}, &res)
	turn, _, tools := h.waitTurn(res.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || len(tools) != 2 {
		t.Fatalf("turn=%+v tools=%+v", turn, tools)
	}
	if tools[0].Tool.Name != "skill.run_script" || !strings.Contains(tools[0].Tool.Output, `hello from skill {"config":{},"input":{"name":"Ada"}}`) {
		t.Fatalf("skill tool = %+v", tools[0].Tool)
	}
	if tools[1].Tool.Name != "task.create" || tools[1].Status != protocol.ItemCompleted {
		t.Fatalf("task tool = %+v", tools[1])
	}
	h.fake.mu.Lock()
	sys := h.fake.calls[0].System
	h.fake.mu.Unlock()
	if !strings.Contains(sys, "greeter: Greet people by name") || !strings.Contains(sys, `may match the "greeter" skill`) {
		t.Fatalf("system prompt lacks skills: %s", sys)
	}
	var list protocol.TaskListResult
	h.call(protocol.MethodTaskList, protocol.TaskListParams{}, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].Timezone != "Europe/Berlin" || list.Tasks[0].CreatedBy != "agent" {
		t.Fatalf("tasks = %+v", list.Tasks)
	}
	var tl protocol.ToolListResult
	h.call(protocol.MethodToolList, nil, &tl)
	names := map[string]string{}
	for _, x := range tl.Tools {
		names[x.Name] = x.Source
	}
	if names["skill.run_script"] != "skill" || names["task.create"] != "task" || names["shell.run"] != "builtin" {
		t.Fatalf("tools = %v", names)
	}
	var sl protocol.SkillListResult
	h.call(protocol.MethodSkillList, nil, &sl)
	if len(sl.Skills) != 1 || !sl.Skills[0].Removable || sl.Skills[0].Scripts[0] != "greet" {
		t.Fatalf("skills = %+v", sl)
	}
	var ml protocol.MCPListResult
	h.call(protocol.MethodMCPList, nil, &ml)
	if len(ml.Servers) != 0 {
		t.Fatalf("mcp = %+v", ml)
	}
}

// TestRoutePreviewAndHealth covers the router's read-only surface: what the
// next message would run on, and what the engine has learned about each key.
func TestRoutePreviewAndHealth(t *testing.T) {
	h := newHarness(t, nil)
	k1 := h.addKey("claude", "primary", "sk-primary")
	k2 := h.addKey("claude", "backup", "sk-backup")
	fb := true
	h.call(protocol.MethodCredentialUpdate, protocol.CredentialUpdateParams{CredentialID: k2.ID, Fallback: &fb}, nil)

	var preview protocol.ModelRouteResult
	h.call(protocol.MethodModelRoute, protocol.ModelRouteParams{Text: "write me a haiku"}, &preview)
	if preview.Chosen == nil || preview.Chosen.Provider != "claude" || preview.Chosen.CredentialID == "" {
		t.Fatalf("preview = %+v", preview)
	}
	if len(preview.Alternatives) == 0 {
		t.Fatalf("no fallbacks offered: %+v", preview)
	}
	if preview.Complexity == "" || preview.Complexity == protocol.ComplexityAuto {
		t.Fatalf("complexity not resolved: %+v", preview)
	}

	// Pinning a model is honoured.
	h.call(protocol.MethodModelRoute, protocol.ModelRouteParams{
		Override: protocol.ModelSelection{Provider: "claude", Model: "claude-haiku-4-5", CredentialID: k2.ID}}, &preview)
	if preview.Chosen == nil || preview.Chosen.Model != "claude-haiku-4-5" || preview.Chosen.CredentialID != k2.ID {
		t.Fatalf("pin ignored: %+v", preview.Chosen)
	}

	// A rate limit on the first key puts it in cooldown, and health says so.
	h.fake.push(func(llm.Request, string) ([]llm.Event, error) {
		return nil, &llm.Error{Provider: "claude", Status: 429, Body: "slow down"}
	}, textReply("second key answered"))
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "x"}, &th)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello there friend"}, &res)
	turn, text, _ := h.waitTurn(res.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || text != "second key answered" {
		t.Fatalf("turn = %+v %q", turn, text)
	}

	var health protocol.ModelHealthResult
	h.call(protocol.MethodModelHealth, protocol.ModelHealthParams{}, &health)
	var cooling *protocol.ModelHealthRow
	for i, row := range health.Rows {
		if row.CredentialID == k1.ID && row.CooldownEnd != nil {
			cooling = &health.Rows[i]
		}
	}
	if cooling == nil || cooling.LastStatus != "rate_limited" || cooling.ErrCount != 1 {
		t.Fatalf("health = %+v", health.Rows)
	}

	// The cooling pair is offered as an alternative, but marked unavailable, and
	// nothing is routed to it.
	h.call(protocol.MethodModelRoute, protocol.ModelRouteParams{}, &preview)
	if preview.Chosen == nil ||
		(preview.Chosen.CredentialID == cooling.CredentialID && preview.Chosen.Model == cooling.Model) {
		t.Fatalf("still routing to the rate-limited model and key: %+v", preview.Chosen)
	}
	var explained bool
	for _, alt := range preview.Alternatives {
		if alt.CredentialID == k1.ID && alt.Model == cooling.Model {
			if alt.Unavailable == "" {
				t.Fatalf("cooldown not explained: %+v", alt)
			}
			explained = true
		}
	}
	if !explained {
		t.Fatalf("cooling route missing from the preview: %+v", preview.Alternatives)
	}

	// Clearing the cooldown brings it back.
	h.call(protocol.MethodModelHealth, protocol.ModelHealthParams{
		Clear: &protocol.RouteRef{Provider: cooling.Provider, Model: cooling.Model, CredentialID: k1.ID}}, &health)
	for _, row := range health.Rows {
		if row.CredentialID == k1.ID && row.CooldownEnd != nil {
			t.Fatalf("cooldown not cleared: %+v", row)
		}
	}
}

func TestPoolFailover(t *testing.T) {
	h := newHarness(t, nil)
	claudeKey := h.addKey("claude", "claude-key", "sk-claude")
	openai := &fakeProvider{id: "openai"}
	h.eng.LLMs.Register(openai)
	h.addKey("openai", "openai-key", "sk-openai")

	routing := protocol.RoutingConfig{
		Models: []protocol.ConfiguredModel{
			{ID: "claude-primary", Name: "Claude primary", Provider: "claude", Model: "claude-test", Enabled: true},
			{ID: "openai-backup", Name: "OpenAI backup", Provider: "openai", Model: "gpt-test", Enabled: true},
		},
		Pools: []protocol.ModelPool{{
			ID: "coding", Name: "Coding", Strategy: "priority", Models: []string{"claude-primary", "openai-backup"}, Enabled: true,
		}},
		DefaultPool: "coding",
	}
	h.call(protocol.MethodRoutingSet, routing, &routing)
	h.fake.push(func(llm.Request, string) ([]llm.Event, error) {
		return nil, &llm.Error{Provider: "claude", Status: 429, Body: "subscription quota exhausted"}
	})
	openai.push(textReply("answered by OpenAI"))

	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "pool",
		Settings: protocol.ModelSelection{Provider: "pool", Model: "coding"}}, &th)
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "use the pool"}, &started)
	turn, text, _ := h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || text != "answered by OpenAI" {
		t.Fatalf("pool fallback failed: turn=%+v text=%q", turn, text)
	}
	if turn.Resolved.Provider != "openai" || turn.Resolved.Model != "gpt-test" {
		t.Fatalf("resolved route not updated: %+v", turn.Resolved)
	}
	openai.mu.Lock()
	keys := append([]string(nil), openai.keys...)
	openai.mu.Unlock()
	if len(keys) != 1 || keys[0] != "sk-openai" {
		t.Fatalf("OpenAI keys = %v", keys)
	}

	// The configured default pool is used when a chat has no explicit model.
	// A locally enforced key budget also advances to the next pool choice.
	h.call(protocol.MethodUsageSetBudget, protocol.UsageSetBudgetParams{
		CredentialID: claudeKey.ID, MonthlyBudgetUSD: 0.01, HardStop: true,
	}, nil)
	if err := h.eng.Store.InsertUsage(h.ctx, store.UsageRecord{
		CredentialID: claudeKey.ID, Provider: "claude", Model: "claude-test",
		Usage: protocol.UsageTotals{CostUSD: 0.02}, Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	openai.push(textReply("default pool fallback"))
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "default pool"}, &th)
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "use the default"}, &started)
	turn, text, _ = h.waitTurn(started.Turn.ID, nil)
	if turn.Status != protocol.TurnCompleted || text != "default pool fallback" || turn.Resolved.Provider != "openai" {
		t.Fatalf("default pool budget fallback failed: turn=%+v text=%q", turn, text)
	}
}
