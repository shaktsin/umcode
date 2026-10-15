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
	"github.com/shaktsin/umcode/internal/store"
)

type outputLimitProvider struct {
	reason string
	tool   bool
	calls  int
}

func (*outputLimitProvider) ID() string { return "claude" }
func (*outputLimitProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *outputLimitProvider) Stream(context.Context, llm.Credential, llm.Request) (<-chan llm.Event, error) {
	p.calls++
	ch := make(chan llm.Event, 3)
	ch <- llm.Event{Type: llm.EventTextDelta, Text: "Now I will write the implementation"}
	if p.tool {
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "partial", Name: "file__write", Args: json.RawMessage(`{"path":"never.txt","content":"unfinished`)}}
	}
	ch <- llm.Event{Type: llm.EventDone, StopReason: p.reason, Usage: llm.Usage{Reported: true, InputTokens: 100, OutputTokens: 4096}}
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
			st, err := store.Open(t.Context(), filepath.Join(cfg.Home, "test.db"))
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
					if finished.Status != protocol.TurnFailed || !strings.Contains(finished.Error, "output limit") {
						t.Fatalf("truncated turn incorrectly completed: %+v", finished)
					}
				} else if finished.Status != protocol.TurnCompleted {
					t.Fatalf("normal turn failed: %+v", finished)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("turn did not finish")
			}
			if provider.calls != 1 {
				t.Fatalf("unbounded or unexpected retries: %d", provider.calls)
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
			if len(decoded.Requests) != 1 || decoded.Requests[0].StopReason != tc.reason {
				t.Fatalf("stop reason lost: %s", b)
			}
			if tc.failed && decoded.Requests[0].Fallback != "output_limit" {
				t.Fatalf("truncation diagnostic missing: %s", b)
			}
		})
	}
}
