package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
)

type budgetProvider struct {
	eventsFor func(int) []llm.Event
	requests  []llm.Request
	budgets   []*delegation.Budget
	events    []llm.Event
	err       error
}

func (*budgetProvider) ID() string                                                   { return "claude" }
func (*budgetProvider) ListModels(context.Context, llm.Credential) ([]string, error) { return nil, nil }
func (p *budgetProvider) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.requests = append(p.requests, req)
	p.budgets = append(p.budgets, delegation.BudgetFromContext(ctx))
	if p.err != nil {
		return nil, p.err
	}
	events := p.events
	if p.eventsFor != nil {
		events = p.eventsFor(len(p.requests))
	}
	ch := make(chan llm.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestDelegationBudgetProviderAttempts(t *testing.T) {
	for _, tt := range []struct {
		name                                      string
		immediate, partial, unknown, subscription bool
	}{
		{name: "reported partial stream failure", partial: true},
		{name: "unreported partial stream failure"},
		{name: "rejected request releases reservation", immediate: true},
		{name: "unknown price retains token accounting", unknown: true},
		{name: "subscription retains token accounting", subscription: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			tr := protocol.Turn{ID: "budget-turn", ThreadID: th.ID, Status: protocol.TurnRunning}
			if err := e.Store.CreateTurn(t.Context(), tr); err != nil {
				t.Fatal(err)
			}
			res, err := e.resolveSelection(t.Context(), th, protocol.ModelSelection{}, "hello", 0)
			if err != nil {
				t.Fatal(err)
			}
			res.meta = models.Meta{In: 1, Out: 1, PriceUnknown: tt.unknown}
			if tt.subscription {
				res.cred.Material.Kind = llm.KindChatGPT
			}
			p := &budgetProvider{events: []llm.Event{{Type: llm.EventTextDelta, Text: "four"}, {Type: llm.EventError, Err: errors.New("stream failed")}}}
			if tt.partial || tt.unknown || tt.subscription {
				p.events = append(p.events, llm.Event{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, Reported: true}})
			}
			if tt.immediate {
				p.err = errors.New("request refused")
			}
			shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 100, MaxCostUSD: 1}, protocol.UsageTotals{}, nil)
			budget := newTurnBudget(protocol.ExecutionLimits{MaxTokens: 1000, MaxCostUSD: 1, MaxDurationMinutes: 5})
			budget.shared = shared
			_, _ = e.streamOnce(t.Context(), t.Context(), tr, res, res.cred, p, llm.Request{MaxTokens: 500}, "chat", budget)
			if len(p.requests) != 1 || p.requests[0].MaxTokens != 100 {
				t.Fatalf("response not capped to remaining task tokens: %+v", p.requests)
			}
			got := shared.Usage()
			if got.Requests != 1 {
				t.Fatalf("attempt not settled: %+v", got)
			}
			if tt.immediate {
				if got.InputTokens != 0 || got.OutputTokens != 0 {
					t.Fatalf("rejected request charged tokens: %+v", got)
				}
			} else if tt.partial || tt.unknown || tt.subscription {
				if got.InputTokens != 10 || got.OutputTokens != 20 {
					t.Fatalf("partial actual lost: %+v", got)
				}
			} else if got.OutputTokens != 1 || !got.Estimated {
				t.Fatalf("partial estimate lost: %+v", got)
			}
			if (tt.unknown || tt.subscription) && (!got.Estimated || got.CostUSD != 0) {
				t.Fatalf("unknown/subscription cost presented as known: %+v", got)
			}
			if _, err := shared.Reserve(t.Context(), "next", protocol.UsageTotals{OutputTokens: 1}); err != nil {
				t.Fatalf("unused reservation leaked: %v", err)
			}
		})
	}
}

func TestDelegationBudgetCostCapAndAdmissionFailure(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	tr := protocol.Turn{ID: "budget-cost", ThreadID: th.ID, Status: protocol.TurnRunning}
	if err := e.Store.CreateTurn(t.Context(), tr); err != nil {
		t.Fatal(err)
	}
	res, err := e.resolveSelection(t.Context(), th, protocol.ModelSelection{}, "hello", 0)
	if err != nil {
		t.Fatal(err)
	}
	res.meta = models.Meta{In: 1_000_000, Out: 1_000_000}
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 100, MaxCostUSD: 5}, protocol.UsageTotals{}, nil)
	b := newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: 5})
	b.shared = shared
	p := &budgetProvider{err: errors.New("refused")}
	_, _ = e.streamOnce(t.Context(), t.Context(), tr, res, res.cred, p, llm.Request{MaxTokens: 100}, "chat", b)
	if len(p.requests) != 1 || p.requests[0].MaxTokens != 5 {
		t.Fatalf("response not capped to route cost: %+v", p.requests)
	}
	// Admission failure must never cross the provider boundary or count a paid attempt.
	before := shared.Usage()
	_, err = e.streamOnce(t.Context(), t.Context(), tr, res, res.cred, p, llm.Request{System: "this input is much too expensive", MaxTokens: 100}, "chat", b)
	if err == nil || len(p.requests) != 1 || shared.Usage() != before {
		t.Fatalf("admission error spent provider attempt: %v %+v", err, shared.Usage())
	}
}

func TestDelegationBudgetInheritedAcrossFollowups(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	p := &budgetProvider{events: []llm.Event{{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, Reported: true}}}}
	e.LLMs.Register(p)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: token.ActivationToken})
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "continue", DelegationTaskID: task.ID})
	if len(p.budgets) != 2 || p.budgets[0] == nil || p.budgets[0] != p.budgets[1] {
		t.Fatalf("follow-up lost shared owner: %+v", p.budgets)
	}
	if got := p.budgets[1].Usage(); got.InputTokens != 20 || got.OutputTokens != 40 || got.Requests != 2 {
		t.Fatalf("usage reset/doubled: %+v", got)
	}
	child := WithDelegationChild(delegation.WithBudget(t.Context(), p.budgets[0]))
	if delegation.BudgetFromContext(child) != p.budgets[0] {
		t.Fatal("child masked accounting along with authority")
	}
}

func TestDelegationBudgetInterruptCancelsPendingApproval(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: token.ActivationToken})
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, err := e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.activeTurns[root.ID] = &activeTurn{turn: root, taskID: task.ID, cancel: func() {}}
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.activeTurns, root.ID); e.mu.Unlock() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.persistAndWaitApproval(ctx, context.WithoutCancel(ctx), protocol.Approval{ID: "delegation-approval", ThreadID: th.ID, TurnID: root.ID, Tool: "shell.run", Status: "pending", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, time.Hour, "approval.request")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		pending := len(e.approvals)
		e.mu.Unlock()
		if pending == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval not pending")
		}
		time.Sleep(time.Millisecond)
	}
	if err = e.InterruptTurn(root.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("approval wait leaked")
	}
	e.mu.Lock()
	pending := len(e.approvals)
	e.mu.Unlock()
	if pending != 0 {
		t.Fatal("pending approval slot leaked")
	}
	persisted, err := e.Store.AgentTask(t.Context(), task.ID)
	if err != nil || persisted.State != protocol.AgentStateCancelled {
		t.Fatalf("interrupt did not cancel durable task: %+v %v", persisted, err)
	}
}

func TestDelegationBudgetIncludesAuxiliaryRequests(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	root := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "original request"})
	// A title cannot use a separate allowance after the task is exhausted.
	exhausted := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 1}, protocol.UsageTotals{OutputTokens: 1}, nil)
	p := &budgetProvider{events: []llm.Event{{Type: llm.EventTextDelta, Text: "Context summary"}, {Type: llm.EventDone, Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, Reported: true}}}}
	e.LLMs.Register(p)
	if got := e.llmTitle(delegation.WithBudget(t.Context(), exhausted), root, "request", "response"); got != "" || len(p.requests) != 0 {
		t.Fatalf("optional title escaped budget: %q %d", got, len(p.requests))
	}
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 100_000, MaxCostUSD: 10}, protocol.UsageTotals{}, nil)
	ctx := delegation.WithBudget(t.Context(), shared)
	if err := e.compactConversation(ctx, ctx, th, ""); err != nil {
		t.Fatal(err)
	}
	if got := shared.Usage(); got.Requests != 1 || got.InputTokens != 10 || got.OutputTokens != 20 {
		t.Fatalf("compaction not charged to task: %+v", got)
	}
	if title := e.llmTitle(ctx, root, "request", "response"); title != "Context summary" {
		t.Fatalf("title=%q", title)
	}
	if got := shared.Usage(); got.Requests != 2 || got.InputTokens != 20 || got.OutputTokens != 40 {
		t.Fatalf("title not charged to task: %+v", got)
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	messages := 0
	for _, item := range items {
		if item.Kind == protocol.ItemAgentMessage {
			messages++
		}
	}
	if messages != 1 {
		t.Fatalf("auxiliary output leaked into transcript: %d messages", messages)
	}
}

func TestDelegationBudgetSettlesEveryFailoverAttempt(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tokens   int64
		attempts int
	}{
		{name: "retry reserves remaining allowance", tokens: 100, attempts: 2},
		{name: "actual overrun blocks retry", tokens: 20, attempts: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			p := &budgetProvider{eventsFor: func(attempt int) []llm.Event {
				if attempt == 1 {
					return []llm.Event{{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, Reported: true}}, {Type: llm.EventError, Err: &llm.Error{Provider: "claude", Status: 503, Body: "unavailable"}}}
				}
				return []llm.Event{{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 2, OutputTokens: 3, Reported: true}}}
			}}
			e.LLMs.Register(p)
			if _, err := e.SetRoutingConfig(t.Context(), protocol.RoutingConfig{Models: []protocol.ConfiguredModel{{ID: "one", Name: "One", Provider: "claude", Model: "first", Enabled: true}, {ID: "two", Name: "Two", Provider: "claude", Model: "second", Enabled: true}}, Pools: []protocol.ModelPool{{ID: "approved", Name: "Approved", Strategy: protocol.PoolPriority, Models: []string{"one", "two"}, Enabled: true}}}); err != nil {
				t.Fatal(err)
			}
			tr := protocol.Turn{ID: "failover", ThreadID: th.ID, Status: protocol.TurnRunning}
			if err := e.Store.CreateTurn(t.Context(), tr); err != nil {
				t.Fatal(err)
			}
			res, err := e.resolveSelection(t.Context(), th, protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "approved"}, "hello", 0)
			if err != nil {
				t.Fatal(err)
			}
			shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: tt.tokens, MaxCostUSD: 1}, protocol.UsageTotals{}, nil)
			b := newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: 5})
			b.shared = shared
			_, err = e.callModel(t.Context(), t.Context(), tr, &res, llm.Request{MaxTokens: 1000}, "chat", b)
			if (tt.attempts == 1) != (err != nil) {
				t.Fatalf("result=%v", err)
			}
			if tt.attempts == 1 && !strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("overrun hid original provider failure: %v", err)
			}
			if len(p.requests) != tt.attempts || shared.Usage().Requests != int64(tt.attempts) {
				t.Fatalf("attempt settlement: requests=%d usage=%+v err=%v", len(p.requests), shared.Usage(), err)
			}
			if tt.attempts == 2 && p.requests[1].MaxTokens != 70 {
				t.Fatalf("retry reused initial reservation: %+v", p.requests)
			}
			wantIn, wantOut := int64(10), int64(20)
			if tt.attempts == 2 {
				wantIn, wantOut = 12, 23
			}
			if got := shared.Usage(); got.InputTokens != wantIn || got.OutputTokens != wantOut {
				t.Fatalf("retry usage reset/lost partials: %+v", got)
			}
		})
	}
}

type waitingDelegationProvider struct{ started chan context.Context }

func (*waitingDelegationProvider) ID() string { return "claude" }
func (*waitingDelegationProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return nil, nil
}
func (p *waitingDelegationProvider) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.started <- ctx
	ch := make(chan llm.Event)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}

func TestDelegationBudgetChildContextCancellationReleasesTurn(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: token.ActivationToken})
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner, shared, err := e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	p := &waitingDelegationProvider{started: make(chan context.Context, 1)}
	e.LLMs.Register(p)
	done := make(chan protocol.Turn, 1)
	child, err := e.startTurn(WithDelegationChild(owner), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "child assignment"}, wait: done})
	if err != nil {
		t.Fatal(err)
	}
	var providerCtx context.Context
	select {
	case providerCtx = <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not reach provider")
	}
	if delegation.BudgetFromContext(providerCtx) != shared {
		t.Fatal("child startup discarded inherited accounting")
	}
	if _, ok := DelegationTaskFromContext(providerCtx); ok {
		t.Fatal("child inherited parent activation authority")
	}
	if err = e.DelegationCoordinator.CancelTask(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Status != protocol.TurnInterrupted {
			t.Fatalf("child=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child context did not inherit task cancellation")
	}
	e.mu.Lock()
	_, active := e.activeTurns[child.ID]
	_, threadActive := e.threadTurns[th.ID]
	e.mu.Unlock()
	if active || threadActive {
		t.Fatal("cancelled child left active turn/thread slots")
	}
}

func TestDelegationBudgetTaskGeneratedTitleRetainsContext(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	if err := e.Store.UpdateThread(t.Context(), th.ID, map[string]any{"title": ""}); err != nil {
		t.Fatal(err)
	}
	p := &budgetProvider{events: []llm.Event{{Type: llm.EventTextDelta, Text: "A task title"}, {Type: llm.EventDone, Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, Reported: true}}}}
	e.LLMs.Register(p)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: token.ActivationToken})
	task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, shared, err := e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for shared.Usage().Requests < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := shared.Usage(); got.Requests != 2 || got.InputTokens != 20 || got.OutputTokens != 40 {
		t.Fatalf("background title lost task context: %+v", got)
	}
}

func TestDelegationBudgetReportedErrorUsagePreservesIncompleteFailure(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		limit, input, output int64
		exhausted            bool
	}{
		{name: "within allowance", limit: 100_000, input: 10, output: 20},
		{name: "actual overrun", limit: 3000, input: 100, output: 4096, exhausted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			e.mu.Lock()
			e.turnLimits.MaxTokens = tc.limit
			e.mu.Unlock()
			p := &budgetProvider{events: []llm.Event{
				{Type: llm.EventTextDelta, Text: "Partial response"},
				{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "incomplete", Name: "file__write", Args: []byte(`{"path":"never.txt","content":"unfinished`)}},
				{Type: llm.EventError, Err: errors.New("response incomplete: content_filter"), Usage: llm.Usage{InputTokens: tc.input, OutputTokens: tc.output, Reported: true}},
			}}
			e.LLMs.Register(p)
			token, err := e.PrepareDelegation(t.Context(), th.ID)
			if err != nil {
				t.Fatal(err)
			}
			wait := make(chan protocol.Turn, 1)
			root, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: token.ActivationToken}, wait: wait})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case finished := <-wait:
				if finished.Status != protocol.TurnFailed || !strings.Contains(finished.Error, "content_filter") {
					t.Fatalf("incomplete error lost: %+v", finished)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("incomplete response did not finish")
			}
			task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, shared, err := e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if got := shared.Usage(); got.InputTokens != tc.input || got.OutputTokens != tc.output || got.Requests != 1 {
				t.Fatalf("reported error usage replaced by estimates: %+v", got)
			}
			if tc.exhausted && shared.StopReason() == "" {
				t.Fatal("reported overrun did not exhaust shared budget")
			}
			if len(p.requests) != 1 {
				t.Fatalf("incomplete response retried: %d", len(p.requests))
			}
			items, err := e.Store.ListItems(t.Context(), th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Kind == protocol.ItemToolCall {
					t.Fatalf("incomplete error tool executed: %+v", item)
				}
			}
			details, err := e.Store.ReadTurnDetails(t.Context(), protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: root.ID})
			if err != nil {
				t.Fatal(err)
			}
			if len(details.Diagnostics.Requests) != 1 || details.Diagnostics.Requests[0].Fallback != "error" || details.Diagnostics.Requests[0].MeasurementOrigin != "provider" {
				t.Fatalf("reported error diagnostics lost: %+v", details.Diagnostics)
			}
		})
	}
}
