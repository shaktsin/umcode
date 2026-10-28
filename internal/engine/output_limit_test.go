package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/teststore"
)

type outputLimitProvider struct {
	reason string
	tool   bool
	calls  int
	output int64
}

func (*outputLimitProvider) ID() string { return "claude" }
func (*outputLimitProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *outputLimitProvider) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.calls++
	ch := make(chan llm.Event, 3)
	ch <- llm.Event{Type: llm.EventTextDelta, Text: "Now I will write the implementation"}
	if p.tool {
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "partial", Name: "file__write", Args: json.RawMessage(`{"path":"never.txt","content":"unfinished`)}}
	}
	output := p.output
	if output == 0 {
		output = 4096
	}
	ch <- llm.Event{Type: llm.EventDone, StopReason: p.reason, Usage: llm.Usage{Reported: true, InputTokens: 100, OutputTokens: output}}
	close(ch)
	return ch, nil
}

func TestOutputLimitDoesNotCompleteOrExecutePartialTools(t *testing.T) {
	for _, tc := range []struct {
		reason string
		tool   bool
		failed bool
	}{
		{"max_tokens", false, true}, {"length", false, true}, {"MAX_TOKENS", true, true}, {"max_output_tokens", true, true}, {"end_turn", false, false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			cfg := config.Default(t.TempDir())
			cfg.LLM.Provider, cfg.LLM.Model = "claude", "deterministic"
			st, err := teststore.Open(t.Context(), filepath.Join(cfg.Home, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			provider := &outputLimitProvider{reason: tc.reason, tool: tc.tool}
			reg := llm.NewRegistry()
			reg.Register(provider)
			e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.Shutdown(context.Background()) })
			if _, err = e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "claude", Label: "test", Secret: "test", IsDefault: true}); err != nil {
				t.Fatal(err)
			}
			th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "output limit regression"})
			if err != nil {
				t.Fatal(err)
			}
			wait := make(chan protocol.Turn, 1)
			started, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "Implement the reader", Override: protocol.ModelSelection{Provider: "claude", Model: "deterministic", Complexity: protocol.ComplexityStandard}}, wait: wait})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case finished := <-wait:
				if tc.failed {
					if finished.Status != protocol.TurnFailed || !strings.Contains(finished.Error, "work is incomplete") {
						t.Fatalf("truncated turn incorrectly completed: %+v", finished)
					}
				} else if finished.Status != protocol.TurnCompleted {
					t.Fatalf("normal turn failed: %+v", finished)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("turn did not finish")
			}
			// A confirmed output cut-off is continued twice, then fails.
			wantCalls := 1
			if tc.failed && strings.ToLower(tc.reason) != "length" {
				wantCalls = 1 + maxOutputContinuations
			}
			if provider.calls != wantCalls {
				t.Fatalf("unbounded or unexpected retries: %d, want %d", provider.calls, wantCalls)
			}
			items, err := st.ListItems(t.Context(), th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Kind == protocol.ItemToolCall {
					t.Fatalf("truncated tool was executed: %+v", item)
				}
			}
			d, err := st.ReadTurnDetails(t.Context(), protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: started.ID})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(d.Diagnostics)
			var decoded struct {
				Requests []struct {
					StopReason string `json:"stopReason"`
					Fallback   string `json:"fallback"`
				}
			}
			if err = json.Unmarshal(b, &decoded); err != nil {
				t.Fatal(err)
			}
			last := len(decoded.Requests) - 1
			if len(decoded.Requests) != wantCalls || decoded.Requests[last].StopReason != tc.reason {
				t.Fatalf("stop reason lost: %s", b)
			}
			if tc.failed && decoded.Requests[last].Fallback != "output_limit" {
				t.Fatalf("truncation diagnostic missing: %s", b)
			}
		})
	}
}

// A capped delegated request can still report an actual overrun. Its output
// limit must remain a failed/incomplete turn, rather than a completed budget
// pause, while the same attempt settles shared actuals and suppresses tools.
func TestOutputLimitDelegationOverrunPreservesFailureAndAccounting(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	p := &outputLimitProvider{reason: "max_output_tokens", tool: true, output: 30000}
	e.LLMs.Register(p)
	e.mu.Lock()
	e.turnLimits.MaxTokens = 20000
	e.mu.Unlock()
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	wait := make(chan protocol.Turn, 1)
	started, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "Implement the reader", DelegationActivationToken: token.ActivationToken}, wait: wait})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case finished := <-wait:
		if finished.Status != protocol.TurnFailed || !strings.Contains(finished.Error, "work is incomplete") {
			t.Fatalf("delegated truncation incorrectly completed/paused: %+v", finished)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("delegated output-limited turn did not finish")
	}
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, shared, err := e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := shared.Usage(); got.InputTokens != 100 || got.OutputTokens != 30000 || got.Requests != 1 {
		t.Fatalf("output-limited attempt lost shared actuals: %+v", got)
	}
	if shared.StopReason() == "" {
		t.Fatal("reported overrun did not exhaust shared allowance")
	}
	if p.calls != 1 {
		t.Fatalf("unexpected output-limit retry: %d", p.calls)
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == protocol.ItemToolCall {
			t.Fatalf("truncated delegated tool executed: %+v", item)
		}
	}
	details, err := e.Store.ReadTurnDetails(t.Context(), protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: started.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Diagnostics.Requests) != 1 || details.Diagnostics.Requests[0].StopReason != "max_output_tokens" || details.Diagnostics.Requests[0].Fallback != "output_limit" {
		t.Fatalf("delegated output-limit diagnostics lost: %+v", details.Diagnostics)
	}
}

func (*outputLimitProvider) RuntimeCapacity(context.Context, llm.Credential, string) (llm.RuntimeCapacity, error) {
	return llm.RuntimeCapacity{ContextWindow: 200000}, nil
}

type maxTokensRecorder struct {
	outputLimitProvider
	maxTokens []int
}

func (p *maxTokensRecorder) Stream(ctx context.Context, cred llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if _, ok := testIntentReply(req, "explanation", "answer", ""); !ok {
		p.maxTokens = append(p.maxTokens, req.MaxTokens)
	}
	return p.outputLimitProvider.Stream(ctx, cred, req)
}

func TestPresetNoLongerSetsMaxTokens(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider, cfg.LLM.Model = "claude", "deterministic"
	st, err := teststore.Open(t.Context(), filepath.Join(cfg.Home, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	provider := &maxTokensRecorder{outputLimitProvider: outputLimitProvider{reason: "end_turn"}}
	reg := llm.NewRegistry()
	reg.Register(provider)
	e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Shutdown(context.Background()) })
	if _, err = e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "claude", Label: "test", Secret: "test", IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "preset max tokens"})
	if err != nil {
		t.Fatal(err)
	}
	wait := make(chan protocol.Turn, 1)
	if _, err = e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "Implement the reader", Override: protocol.ModelSelection{Provider: "claude", Model: "deterministic", Complexity: protocol.ComplexityLow}}, wait: wait}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wait:
	case <-time.After(15 * time.Second):
		t.Fatal("turn did not finish")
	}
	if len(provider.maxTokens) == 0 {
		t.Fatal("no request recorded")
	}
	// The Low preset used to cap output at 4096; the budgeter result for the
	// 256000-token default window with the 32768 default output is 32768.
	if got := provider.maxTokens[0]; got != 32768 {
		t.Fatalf("first request MaxTokens = %d, want the budgeter result 32768", got)
	}
}
