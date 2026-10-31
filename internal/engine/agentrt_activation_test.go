package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

type suggestEnv struct {
	e    *Engine
	th   protocol.Thread
	root string
	p    *delegationProvider
}

func suggestFixture(t *testing.T) suggestEnv {
	t.Helper()
	e, th, root, p := delegationFixtureWith(t, runtimeV2On)
	return suggestEnv{e: e, th: th, root: root, p: p}
}

// leadTurn runs one ordinary (not admitted) user turn and returns the lead's
// tool context for it.
func (env suggestEnv) leadTurn(t *testing.T) context.Context {
	t.Helper()
	turn := delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "hello"})
	return tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: env.root})
}

func (env suggestEnv) suggest(t *testing.T, ctx context.Context) (string, error) {
	t.Helper()
	tool, ok := env.e.Tools.Get("agent.suggest")
	if !ok {
		t.Fatal("agent.suggest not registered with runtime_v2 on")
	}
	return tool.Call(ctx, json.RawMessage(`{"reason":"three independent modules can be built in parallel"}`))
}

func (env suggestEnv) count(t *testing.T) int {
	t.Helper()
	var n int
	if err := env.e.Store.DB.QueryRow(`SELECT count(*) FROM agent_suggestions WHERE thread_id=?`, env.th.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireRefused(t *testing.T, err error, rule string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), rule) {
		t.Fatalf("want refusal naming %q, got %v", rule, err)
	}
}

func suggestionID(t *testing.T, out string) string {
	t.Helper()
	var r struct {
		SuggestionID, State, Message string
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.SuggestionID == "" || r.State != "pending" {
		t.Fatalf("suggest result %q: %v", out, err)
	}
	if r.Message != "Stop now: the user decides whether to enable agents. Do not call agent.* tools or continue the task in this turn." {
		t.Fatalf("message = %q", r.Message)
	}
	return r.SuggestionID
}

func TestSuggestToolLeadOnly(t *testing.T) {
	env := suggestFixture(t)
	lead := env.leadTurn(t)
	turn := lead.Value(executionTurnKey{}).(protocol.Turn)
	full := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true, SpawnMax: 8}
	for name, ctx := range map[string]context.Context{
		"runtime agent": withRuntimeAgent(withChildExecution(WithDelegationChild(lead), &childExecution{runtime: &runtimeExecution{agent: protocol.RuntimeAgent{ID: "a1", Capabilities: full}}}), "a1"),
		"legacy child":  withChildExecution(WithDelegationChild(lead), &childExecution{}),
		"child marker":  WithDelegationChild(lead),
	} {
		_, err := env.suggest(t, ctx)
		requireRefused(t, err, "lead-only")
		_, err = env.e.suggestAgents(ctx, "parallel work")
		requireRefused(t, err, "lead-only")
		tool, _ := env.e.Tools.Get("agent.suggest")
		if env.e.permittedToolFor(ctx, turnTool{tool: tool}, nil) {
			t.Fatalf("%s: agent.suggest offered", name)
		}
		call := llm.ToolCall{ID: "c1", Name: tools.ToWire("agent.suggest"), Args: json.RawMessage(`{"reason":"parallel work"}`)}
		if r := env.e.runTool(ctx, t.Context(), env.th, turn, call, nil); !r.IsError {
			t.Fatalf("%s: runTool accepted agent.suggest: %s", name, r.Output)
		}
	}
	if n := env.count(t); n != 0 {
		t.Fatalf("non-lead callers stored %d suggestions", n)
	}
	// The lead itself may suggest, through the real tool gate.
	tool, _ := env.e.Tools.Get("agent.suggest")
	if !env.e.permittedToolFor(lead, turnTool{tool: tool}, nil) {
		t.Fatal("agent.suggest not offered to the lead's ordinary turn")
	}
	call := llm.ToolCall{ID: "c1", Name: tools.ToWire("agent.suggest"), Args: json.RawMessage(`{"reason":"parallel work"}`)}
	if r := env.e.runTool(lead, t.Context(), env.th, turn, call, nil); r.IsError {
		t.Fatalf("lead runTool refused: %s", r.Output)
	}
	if n := env.count(t); n != 1 {
		t.Fatalf("lead suggestion stored %d", n)
	}
}

func TestSuggestRequiresRuntimeV2(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	if _, ok := e.Tools.Get("agent.suggest"); ok {
		t.Fatal("agent.suggest registered with runtime_v2 off")
	}
	turn := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello"})
	ctx := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
	_, err := e.suggestAgents(ctx, "parallel work")
	requireRefused(t, err, "agents.runtime_v2")
}

func TestSuggestOncePerTurn(t *testing.T) {
	env := suggestFixture(t)
	lead := env.leadTurn(t)
	out, err := env.suggest(t, lead)
	if err != nil {
		t.Fatal(err)
	}
	id := suggestionID(t, out)
	_, err = env.suggest(t, lead)
	requireRefused(t, err, "one per user message")
	if n := env.count(t); n != 1 {
		t.Fatalf("stored %d suggestions, want 1", n)
	}
	got, err := env.e.Store.GetAgentSuggestion(t.Context(), id)
	if err != nil || got.State != store.SuggestionPending || got.TurnID != lead.Value(executionTurnKey{}).(protocol.Turn).ID {
		t.Fatalf("stored %+v %v", got, err)
	}
	// A new user message may suggest again.
	if _, err = env.suggest(t, env.leadTurn(t)); err != nil {
		t.Fatalf("next turn: %v", err)
	}
}

func TestSuggestRefusedAfterDeclineUntilUserEnables(t *testing.T) {
	env := suggestFixture(t)
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: suggestionID(t, out), Decision: protocol.SuggestionDecline})
	if err != nil || res.Suggestion.State != store.SuggestionDeclined || res.ActivationToken != "" {
		t.Fatalf("decline: %+v %v", res, err)
	}
	_, err = env.suggest(t, env.leadTurn(t))
	requireRefused(t, err, "declined")
	if n := env.count(t); n != 1 {
		t.Fatalf("stored %d suggestions after a refused one", n)
	}
	// The user enables agents themselves: suggestions are allowed again.
	if _, err = env.e.PrepareDelegation(t.Context(), env.th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = env.suggest(t, env.leadTurn(t)); err != nil {
		t.Fatalf("after the user enabled agents: %v", err)
	}
}

func TestSuggestRefusedWithoutCommittedGit(t *testing.T) {
	env := suggestFixture(t)
	lead := env.leadTurn(t)
	if err := os.WriteFile(filepath.Join(env.root, "scratch.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := env.suggest(t, lead)
	requireRefused(t, err, "committed Git")
	if n := env.count(t); n != 0 {
		t.Fatalf("stored %d suggestions", n)
	}
}

func TestSuggestRefusedWhileDelegationTaskActive(t *testing.T) {
	env := suggestFixture(t)
	activation, err := env.e.PrepareDelegation(t.Context(), env.th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken})
	if env.p.last() == "" {
		t.Fatal("task not admitted")
	}
	_, err = env.suggest(t, env.leadTurn(t))
	requireRefused(t, err, "active delegation task")
}

func TestSuggestNotListedWhenAdmitted(t *testing.T) {
	e, _, turn, _, admitted := runtimeV2Admitted(t)
	tool, ok := e.Tools.Get("agent.suggest")
	if !ok {
		t.Fatal("agent.suggest not registered")
	}
	if e.permittedToolFor(admitted, turnTool{tool: tool}, nil) {
		t.Fatal("agent.suggest offered in a delegation-admitted turn")
	}
	_, err := e.suggestAgents(context.WithValue(admitted, executionTurnKey{}, turn), "parallel work")
	requireRefused(t, err, "already admitted")
	// The admitted turn still gets the runtime agent tools, and only it.
	spawn, _ := e.Tools.Get("agent.spawn")
	if !e.permittedToolFor(admitted, turnTool{tool: spawn}, nil) {
		t.Fatal("agent.spawn not offered in the admitted turn")
	}
	if e.permittedToolFor(t.Context(), turnTool{tool: spawn}, nil) {
		t.Fatal("agent.spawn offered without admission")
	}
}

func TestSuggestionRespondEnableReturnsToken(t *testing.T) {
	env := suggestFixture(t)
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: suggestionID(t, out), Decision: protocol.SuggestionEnable})
	if err != nil || res.Suggestion.State != store.SuggestionEnabled || res.ActivationToken == "" || res.ExpiresAt == nil {
		t.Fatalf("enable: %+v %v", res, err)
	}
	delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "go", DelegationActivationToken: res.ActivationToken})
	id := env.p.last()
	if id == "" {
		t.Fatal("follow-up turn with the token not admitted")
	}
	task, err := env.e.Store.AgentTask(t.Context(), id)
	if err != nil || task.Activation != protocol.AgentActivationExplicitUser {
		t.Fatalf("task %+v %v", task, err)
	}
}

func TestSuggestionRespondIdempotent(t *testing.T) {
	env := suggestFixture(t)
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	id := suggestionID(t, out)
	first, err := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable})
	if err != nil || first.ActivationToken == "" {
		t.Fatal(err)
	}
	again, err := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable})
	if err != nil || again.Suggestion.State != store.SuggestionEnabled || again.ActivationToken != "" || !again.Suggestion.DecidedAt.Equal(*first.Suggestion.DecidedAt) {
		t.Fatalf("repeat enable: %+v %v", again, err)
	}
	if _, err = env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionDecline}); err == nil {
		t.Fatal("decline after enable accepted")
	}
	for _, bad := range []protocol.AgentSuggestionRespondParams{{SuggestionID: id, Decision: "approve"}, {SuggestionID: "missing", Decision: protocol.SuggestionEnable}, {Decision: protocol.SuggestionEnable}} {
		if _, err = env.e.RespondAgentSuggestion(t.Context(), bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	// Declines are idempotent too, and a declined suggestion cannot be enabled.
	out, err = env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	id2 := suggestionID(t, out)
	for range 2 {
		res, err := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id2, Decision: protocol.SuggestionDecline})
		if err != nil || res.Suggestion.State != store.SuggestionDeclined {
			t.Fatalf("decline: %+v %v", res, err)
		}
	}
	if _, err = env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id2, Decision: protocol.SuggestionEnable}); err == nil {
		t.Fatal("enable after decline accepted")
	}
	// An expired (restart) suggestion cannot be decided.
	out, err = env.suggest(t, env.leadTurn(t))
	if err == nil {
		t.Fatal("suggest after a decline accepted")
	}
	if _, err = env.e.PrepareDelegation(t.Context(), env.th.ID); err != nil {
		t.Fatal(err)
	}
	out, err = env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.e.Store.ExpirePendingAgentSuggestions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: suggestionID(t, out), Decision: protocol.SuggestionEnable}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired enable: %v", err)
	}
	// Children never respond.
	if _, err = env.e.RespondAgentSuggestion(WithDelegationChild(t.Context()), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable}); err == nil {
		t.Fatal("child responded")
	}
}

func TestSuggestionDoesNotEnableDelegationByItself(t *testing.T) {
	env := suggestFixture(t)
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	id := suggestionID(t, out)
	delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "go ahead"})
	if env.p.last() != "" {
		t.Fatal("a pending suggestion admitted a turn without a token")
	}
	if _, err = env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable}); err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, env.e, protocol.TurnStartParams{ThreadID: env.th.ID, Text: "go ahead"})
	if env.p.last() != "" {
		t.Fatal("an enabled suggestion admitted a turn without a token")
	}
	var tasks int
	if err = env.e.Store.DB.QueryRow(`SELECT count(*) FROM agent_tasks`).Scan(&tasks); err != nil || tasks != 0 {
		t.Fatalf("tasks = %d %v", tasks, err)
	}
}

func TestSuggestStartupExpiresPending(t *testing.T) {
	env := suggestFixture(t)
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	id := suggestionID(t, out)
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider = "claude"
	cfg.LLM.Model = "deterministic"
	reg := llm.NewRegistry()
	reg.Register(&delegationProvider{})
	restarted, err := New(t.Context(), Options{Config: cfg, Store: env.e.Store, Secrets: secrets.NewMemoryStore(), LLMs: reg, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Shutdown(context.Background()) })
	got, err := env.e.Store.GetAgentSuggestion(t.Context(), id)
	if err != nil || got.State != store.SuggestionExpired {
		t.Fatalf("after restart: %+v %v", got, err)
	}
}

// An enable and a decline answering the same card at once are serialized: a
// decline that lands while the enable is preparing delegation is never
// silently cleared. Whichever wins, a declined card keeps suppressing.
func TestSuggestionRespondEnableDeclineRace(t *testing.T) {
	env := suggestFixture(t)
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	id := suggestionID(t, out)
	declined := make(chan error, 1)
	env.e.suggestionRespondHook = func() {
		env.e.suggestionRespondHook = nil
		// The decline arrives after the enable read the card as pending.
		go func() {
			_, err := env.e.RespondAgentSuggestion(context.WithoutCancel(t.Context()), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionDecline})
			declined <- err
		}()
		time.Sleep(300 * time.Millisecond)
	}
	res, enableErr := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable})
	declineErr := <-declined
	got, err := env.e.Store.GetAgentSuggestion(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	suppressed, err := env.e.Store.ThreadDeclinedSuggestion(t.Context(), env.th.ID)
	if err != nil {
		t.Fatal(err)
	}
	switch got.State {
	case store.SuggestionEnabled:
		if enableErr != nil || res.ActivationToken == "" || declineErr == nil {
			t.Fatalf("enabled: enable=%v token=%q decline=%v", enableErr, res.ActivationToken, declineErr)
		}
	case store.SuggestionDeclined:
		if !suppressed {
			t.Fatal("a recorded decline was cleared by an enable that did not succeed")
		}
		if enableErr == nil || res.ActivationToken != "" {
			t.Fatalf("declined card but enable returned %+v %v", res, enableErr)
		}
	default:
		t.Fatalf("state %s", got.State)
	}
}

// Two enables at once: only one of them gets a token.
func TestSuggestionRespondConcurrentEnablesOneToken(t *testing.T) {
	env := suggestFixture(t)
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	id := suggestionID(t, out)
	second := make(chan protocol.AgentSuggestionRespondResult, 1)
	env.e.suggestionRespondHook = func() {
		env.e.suggestionRespondHook = nil
		go func() {
			r, err := env.e.RespondAgentSuggestion(context.WithoutCancel(t.Context()), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable})
			if err != nil {
				t.Errorf("second enable: %v", err)
			}
			second <- r
		}()
		time.Sleep(300 * time.Millisecond)
	}
	first, err := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable})
	if err != nil {
		t.Fatal(err)
	}
	r2 := <-second
	tokens := 0
	for _, r := range []protocol.AgentSuggestionRespondResult{first, r2} {
		if r.Suggestion.State != store.SuggestionEnabled {
			t.Fatalf("state %s", r.Suggestion.State)
		}
		if r.ActivationToken != "" {
			tokens++
		}
	}
	if tokens != 1 {
		t.Fatalf("%d tokens issued for one card, want 1", tokens)
	}
}

// Two agent.suggest calls racing in one turn store one row; the loser is
// refused naming the one-per-message rule.
func TestSuggestConcurrentInOneTurnStoresOne(t *testing.T) {
	env := suggestFixture(t)
	lead := env.leadTurn(t)
	const n = 4
	errs := make(chan error, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start
			_, err := env.e.suggestAgents(lead, "parallel work")
			errs <- err
		}()
	}
	close(start)
	ok := 0
	for range n {
		if err := <-errs; err == nil {
			ok++
		} else if !strings.Contains(err.Error(), "one per user message") {
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
	if ok != 1 || env.count(t) != 1 {
		t.Fatalf("accepted %d, stored %d; want 1 and 1", ok, env.count(t))
	}
}

// Respond-enable when delegation cannot be prepared leaves the card pending,
// issues no token and clears no decline.
func TestSuggestionRespondEnableFailsWhenDelegationCannotStart(t *testing.T) {
	env := suggestFixture(t)
	// A declined card from an earlier message must stay effective.
	out, err := env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	old := suggestionID(t, out)
	out, err = env.suggest(t, env.leadTurn(t))
	if err != nil {
		t.Fatal(err)
	}
	id := suggestionID(t, out)
	if _, err = env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: old, Decision: protocol.SuggestionDecline}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(env.root, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := env.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: id, Decision: protocol.SuggestionEnable})
	if err == nil || res.ActivationToken != "" {
		t.Fatalf("enable on a dirty checkout: %+v %v", res, err)
	}
	got, err := env.e.Store.GetAgentSuggestion(t.Context(), id)
	if err != nil || got.State != store.SuggestionPending {
		t.Fatalf("card after failed enable: %+v %v", got, err)
	}
	if suppressed, err := env.e.Store.ThreadDeclinedSuggestion(t.Context(), env.th.ID); err != nil || !suppressed {
		t.Fatalf("failed enable cleared the decline: %v %v", suppressed, err)
	}
	env.e.mu.Lock()
	tokens := len(env.e.delegationTokens)
	env.e.mu.Unlock()
	if tokens != 0 {
		t.Fatalf("%d activation tokens left after a failed enable", tokens)
	}
}
