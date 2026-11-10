package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/teststore"
	"github.com/shaktsin/umcode/internal/workbench"
)

// A local model selection must reach local validation before the credential and
// tool-capability router. This catches accidental return to the cloud path.
func TestWorkbenchLocalStartTurnMissingPromotionDoesNotRouteToCloud(t *testing.T) {
	e, th, cloudCalls, _ := workbenchLocalChatFixture(t)
	_, err := e.StartTurn(t.Context(), protocol.TurnStartParams{
		ThreadID: th.ID, Text: "hello", Override: protocol.ModelSelection{Provider: "workbench_local", Model: "missing-promotion"},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "promotion") {
		t.Fatalf("want actionable missing promotion error, got %v", err)
	}
	if got := cloudCalls.Load(); got != 0 {
		t.Fatalf("local selection contacted cloud %d times", got)
	}
	turns, err := e.Store.ListTurns(t.Context(), th.ID)
	if err != nil || len(turns) != 0 {
		t.Fatalf("invalid promotion admitted a turn: %+v, %v", turns, err)
	}
}

func TestWorkbenchLocalPromptKeepsConversationWithoutAgentContext(t *testing.T) {
	items := []protocol.Item{
		{TurnID: "old", Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "hello"},
		{TurnID: "old", Kind: protocol.ItemToolCall, Status: protocol.ItemCompleted, Text: "private tool output"},
		{TurnID: "old", Kind: protocol.ItemAgentMessage, Status: protocol.ItemCompleted, Text: "hi"},
		{TurnID: "old", Kind: protocol.ItemContextCompaction, Status: protocol.ItemCompleted, Text: "coding agent system summary"},
		{TurnID: "current", Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "again"},
	}
	prompt, omitted, err := workbenchLocalPrompt(items, "current", "again", 256, 32)
	if err != nil || omitted || prompt != "User: hello\nAssistant: hi\nUser: again\nAssistant:" {
		t.Fatalf("prompt=%q omitted=%v err=%v", prompt, omitted, err)
	}
}

func TestWorkbenchLocalPromptCountsUTF8BytesAndReportsHistoryOmission(t *testing.T) {
	items := []protocol.Item{
		{TurnID: "old", Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: strings.Repeat("x", 100)},
		{TurnID: "old", Kind: protocol.ItemAgentMessage, Status: protocol.ItemCompleted, Text: "old reply"},
		{TurnID: "recent", Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "hi"},
		{TurnID: "recent", Kind: protocol.ItemAgentMessage, Status: protocol.ItemCompleted, Text: "ok"},
	}
	prompt, omitted, err := workbenchLocalPrompt(items, "current", "é", 64, 8)
	if err != nil || !omitted || prompt != "User: hi\nAssistant: ok\nUser: é\nAssistant:" {
		t.Fatalf("prompt=%q omitted=%v err=%v", prompt, omitted, err)
	}
	// The framing is 17 bytes, é is two bytes, BOS is one, output is 8.
	if _, _, err := workbenchLocalPrompt(nil, "current", "é", 27, 8); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("byte-token overflow was not explicit: %v", err)
	}
}

func TestWorkbenchLocalTurnPersistsPredictionAndNotificationsWithoutCloudKey(t *testing.T) {
	e, th, cloudCalls, _ := workbenchLocalChatFixture(t)
	runtime := newWorkbenchLocalRuntimeFixture()
	runtime.generate = func(_ context.Context, id, prompt string, maxTokens int, temperature float64) (workbench.Prediction, error) {
		if id != "promotion-1" || prompt != "User: hello\nAssistant:" || maxTokens < 1 || maxTokens > 128 || temperature != 0 {
			return workbench.Prediction{}, errors.New("wrong pinned generation request")
		}
		return workbench.Prediction{CheckpointID: "checkpoint-1", Text: "local response", Device: "cpu"}, nil
	}
	var methods []string
	var deltas string
	e.Bus.Add(&memorySubscriber{thread: th.ID, notify: func(method string, params any) {
		methods = append(methods, method)
		if p, ok := params.(protocol.ItemDelta); ok {
			deltas += p.Text
		}
	}})
	completed := runWorkbenchLocalTestTurn(t, e, th, runtime, "hello")
	if completed.Status != protocol.TurnCompleted || completed.Resolved.Provider != "workbench_local" || completed.Resolved.Model != "promotion-1" || completed.Resolved.CredentialID != "" {
		t.Fatalf("completed turn=%+v", completed)
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil || len(items) != 2 || items[0].Kind != protocol.ItemUserMessage || items[1].Kind != protocol.ItemAgentMessage || items[1].Text != "local response" || items[1].Status != protocol.ItemCompleted {
		t.Fatalf("transcript=%+v err=%v", items, err)
	}
	if deltas != "local response" || strings.Join(methods, ",") != "turn/started,item/completed,item/started,item/delta,item/completed,turn/completed,thread/updated" {
		t.Fatalf("notifications=%v deltas=%q", methods, deltas)
	}
	if cloudCalls.Load() != 0 {
		t.Fatal("local chat made a cloud request")
	}
	// An untitled chat receives a deterministic local title, with no second LLM call.
	saved, err := e.Store.GetThread(t.Context(), th.ID)
	if err != nil || saved.Title != "hello" {
		t.Fatalf("local title=%q err=%v", saved.Title, err)
	}
}

func TestWorkbenchLocalTurnCancellationAndConflict(t *testing.T) {
	e, th, cloudCalls, _ := workbenchLocalChatFixture(t)
	runtime := newWorkbenchLocalRuntimeFixture()
	entered := make(chan struct{})
	runtime.generate = func(ctx context.Context, _ string, _ string, _ int, _ float64) (workbench.Prediction, error) {
		close(entered)
		<-ctx.Done()
		return workbench.Prediction{}, ctx.Err()
	}
	wait := make(chan protocol.Turn, 1)
	p := turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "hello", Override: protocol.ModelSelection{Provider: "workbench_local", Model: "promotion-1"}}, wait: wait}
	started, err := e.startWorkbenchLocalTurn(t.Context(), th, p, runtime)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("local generation did not start")
	}
	if _, err := e.startWorkbenchLocalTurn(t.Context(), th, p, runtime); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("concurrent local turn admitted: %v", err)
	}
	if err := e.InterruptTurn(started.ID); err != nil {
		t.Fatal(err)
	}
	completed := awaitWorkbenchLocalTestTurn(t, wait)
	if completed.Status != protocol.TurnInterrupted || completed.Error != "interrupted" || cloudCalls.Load() != 0 {
		t.Fatalf("cancelled turn=%+v cloud=%d", completed, cloudCalls.Load())
	}
	turns, err := e.Store.ListTurns(t.Context(), th.ID)
	if err != nil || len(turns) != 1 || turns[0].Status != protocol.TurnInterrupted {
		t.Fatalf("cancellation was not persisted: %+v err=%v", turns, err)
	}
}

func TestWorkbenchLocalTurnRejectsUnsupportedRequestsBeforeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*protocol.TurnStartParams)
	}{
		{"attachments", func(p *protocol.TurnStartParams) {
			p.Attachments = []protocol.Attachment{{MimeType: "image/png", DataB64: "AAAA"}}
		}},
		{"tools", func(p *protocol.TurnStartParams) { p.TaskIntent = &protocol.TaskIntentProposal{} }},
		{"delegation", func(p *protocol.TurnStartParams) { p.DelegationActivationToken = "token" }},
		{"delegated task", func(p *protocol.TurnStartParams) { p.DelegationTaskID = "task" }},
		{"workbench assistant", func(p *protocol.TurnStartParams) { p.Workbench = &protocol.WorkbenchTurnContext{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, th, _, _ := workbenchLocalChatFixture(t)
			p := protocol.TurnStartParams{ThreadID: th.ID, Text: "hello", Override: protocol.ModelSelection{Provider: "workbench_local", Model: "promotion-1"}}
			tc.modify(&p)
			_, err := e.startWorkbenchLocalTurn(t.Context(), th, turnRequest{TurnStartParams: p}, newWorkbenchLocalRuntimeFixture())
			if err == nil || !strings.Contains(err.Error(), "plain text") {
				t.Fatalf("unsupported request admitted: %v", err)
			}
			turns, err := e.Store.ListTurns(t.Context(), th.ID)
			if err != nil || len(turns) != 0 {
				t.Fatalf("unsupported request persisted: %+v %v", turns, err)
			}
		})
	}
}

func TestWorkbenchLocalGenerationFailureNeverFallsBackToConfiguredCloud(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prediction workbench.Prediction
		err        error
		want       string
	}{
		{name: "worker failure", err: errors.New("checkpoint missing; import it again"), want: "checkpoint missing"},
		{name: "wrong checkpoint", prediction: workbench.Prediction{CheckpointID: "replacement", Text: "wrong reply"}, want: "checkpoint"},
		{name: "empty output", prediction: workbench.Prediction{CheckpointID: "checkpoint-1"}, want: "no text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, th, cloudCalls, cloudURL := workbenchLocalChatFixture(t)
			if _, err := e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "openai", Label: "cloud sentinel", Secret: "test-secret", BaseURL: cloudURL, IsDefault: true}); err != nil {
				t.Fatal(err)
			}
			runtime := newWorkbenchLocalRuntimeFixture()
			runtime.generate = func(context.Context, string, string, int, float64) (workbench.Prediction, error) {
				return tc.prediction, tc.err
			}
			completed := runWorkbenchLocalTestTurn(t, e, th, runtime, "hello")
			if completed.Status != protocol.TurnFailed || !strings.Contains(completed.Error, tc.want) || cloudCalls.Load() != 0 {
				t.Fatalf("local failure=%+v cloud=%d", completed, cloudCalls.Load())
			}
			items, err := e.Store.ListItems(t.Context(), th.ID, 0)
			if err != nil || len(items) == 0 || items[len(items)-1].Kind != protocol.ItemError || !strings.Contains(items[len(items)-1].Text, tc.want) {
				t.Fatalf("failure missing from transcript: %+v err=%v", items, err)
			}
		})
	}
}

type workbenchLocalRuntimeFixture struct {
	promotion    workbench.Promotion
	window       int
	promotionErr error
	generate     func(context.Context, string, string, int, float64) (workbench.Prediction, error)
}

func newWorkbenchLocalRuntimeFixture() *workbenchLocalRuntimeFixture {
	return &workbenchLocalRuntimeFixture{promotion: workbench.Promotion{ID: "promotion-1", ProjectID: "project-1", ModelID: "model-1", ModelCreatedAt: "2026-11-01T00:00:00Z", CheckpointID: "checkpoint-1", Runtime: "pytorch", Name: "Local model"}, window: 256,
		generate: func(context.Context, string, string, int, float64) (workbench.Prediction, error) {
			return workbench.Prediction{CheckpointID: "checkpoint-1", Text: "local response", Device: "cpu"}, nil
		}}
}

func (r *workbenchLocalRuntimeFixture) WorkbenchLocalPromotion(context.Context, string) (workbench.Promotion, error) {
	return r.promotion, r.promotionErr
}

func (r *workbenchLocalRuntimeFixture) LocalWorkbenchModels(context.Context, string) ([]protocol.Model, error) {
	return []protocol.Model{{Provider: "workbench_local", ID: r.promotion.ID, ContextWindow: r.window}}, nil
}

func (r *workbenchLocalRuntimeFixture) GenerateWorkbenchLocal(ctx context.Context, id, prompt string, maxTokens int, temperature float64) (workbench.Prediction, error) {
	return r.generate(ctx, id, prompt, maxTokens, temperature)
}

func runWorkbenchLocalTestTurn(t *testing.T, e *Engine, th protocol.Thread, runtime *workbenchLocalRuntimeFixture, text string) protocol.Turn {
	t.Helper()
	wait := make(chan protocol.Turn, 1)
	_, err := e.startWorkbenchLocalTurn(t.Context(), th, turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: text, Override: protocol.ModelSelection{Provider: "workbench_local", Model: "promotion-1"}}, wait: wait}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return awaitWorkbenchLocalTestTurn(t, wait)
}

func awaitWorkbenchLocalTestTurn(t *testing.T, wait <-chan protocol.Turn) protocol.Turn {
	t.Helper()
	select {
	case completed := <-wait:
		return completed
	case <-time.After(5 * time.Second):
		t.Fatal("local turn did not complete")
		return protocol.Turn{}
	}
}

func workbenchLocalChatFixture(t *testing.T) (*Engine, protocol.Thread, *atomic.Int64, string) {
	t.Helper()
	calls := new(atomic.Int64)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected cloud request", http.StatusInternalServerError)
	}))
	t.Cleanup(cloud.Close)
	home := t.TempDir()
	cfg := config.Default(home)
	cfg.LLM.Provider, cfg.LLM.Model = "openai", "cloud-model"
	st, err := teststore.Open(t.Context(), filepath.Join(home, "local-chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: llm.NewRegistry(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Shutdown(context.Background()) })
	th, err := st.CreateThread(t.Context(), protocol.Thread{})
	if err != nil {
		t.Fatal(err)
	}
	return e, th, calls, cloud.URL
}

func TestLocalPromptMatchesCheckpointTrainingFormat(t *testing.T) {
	for _, tc := range []struct{ format, want string }{
		{"chat-v1", "User: Which color?\nAssistant:"},
		{"prompt-response-v1", "Which color?\n"},
		{"text-v1", "Which color?"},
	} {
		got, _, err := workbenchLocalPromptFormat(nil, "", "Which color?", 128, 32, tc.format)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got=%q want=%q err=%v", tc.format, got, tc.want, err)
		}
	}
	if _, _, err := workbenchLocalPromptFormat(nil, "", "question", 128, 32, "invented"); err == nil {
		t.Fatal("unknown prompt format accepted")
	}
}

func TestRawQuestionPromptPreservesTrainingWhitespaceAndRejectsUntrainedHistory(t *testing.T) {
	got, _, err := workbenchLocalPromptFormat(nil, "", " question \n", 128, 32, "prompt-response-v1")
	if err != nil || got != " question \n\n" {
		t.Fatalf("raw bytes changed %q %v", got, err)
	}
	items := []protocol.Item{{TurnID: "old", Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "old question"}, {TurnID: "old", Kind: protocol.ItemAgentMessage, Status: protocol.ItemCompleted, Text: "old answer"}}
	if _, _, err = workbenchLocalPromptFormat(items, "", "next question", 256, 32, "prompt-response-v1"); err == nil || !strings.Contains(err.Error(), "new chat") {
		t.Fatalf("unsupported history: %v", err)
	}
}
