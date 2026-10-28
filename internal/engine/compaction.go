package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

const (
	compactionTurnLock = "__umcode_context_compaction__"
	maxCompactionInput = 100_000
)

// CompactThread summarizes the active conversation context for future turns.
// The original items remain in storage and visible in the transcript.
func (e *Engine) CompactThread(ctx context.Context, threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return protocol.Errorf(protocol.CodeInvalidParams, "thread id is required")
	}
	e.mu.Lock()
	if running, ok := e.threadTurns[threadID]; ok {
		e.mu.Unlock()
		return protocol.Errorf(protocol.CodeConflict, "cannot compact while %s is running", running)
	}
	e.threadTurns[threadID] = compactionTurnLock
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.threadTurns[threadID] == compactionTurnLock {
			delete(e.threadTurns, threadID)
		}
		e.mu.Unlock()
	}()

	thread, err := e.Store.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	tasks, err := e.Store.AgentTasksForThread(ctx, threadID)
	if err != nil {
		return err
	}
	var admitted bool
	for _, task := range tasks {
		if !unfinishedDelegation(task.State) {
			continue
		}
		if admitted {
			return errors.New("manual compaction has multiple unfinished task owners")
		}
		if err = e.validateDelegationParent(ctx, task); err != nil {
			return fmt.Errorf("manual compaction task ownership: %w", err)
		}
		// Manual compaction may only reuse a real admitted ledger. Never create
		// an unlimited zero-default budget for an incomplete stored task.
		limits, _, readErr := e.Store.ReadAgentTaskBudget(ctx, task.ID)
		if readErr != nil {
			return fmt.Errorf("manual compaction shared task budget unavailable: %w", readErr)
		}
		owned, shared, ownerErr := e.DelegationCoordinator.Task(ctx, task, limits)
		if ownerErr != nil {
			return fmt.Errorf("manual compaction task owner: %w", ownerErr)
		}
		if reason := shared.StopReason(); reason != "" {
			return errors.New(reason)
		}
		var linked context.Context
		var cancel context.CancelFunc
		if deadline, ok := owned.Deadline(); ok {
			linked, cancel = context.WithDeadline(ctx, deadline)
		} else {
			linked, cancel = context.WithCancel(ctx)
		}
		stop := context.AfterFunc(owned, cancel)
		defer stop()
		defer cancel()
		if owned.Err() != nil {
			cancel()
		}
		ctx = delegation.WithBudget(withDelegationTask(linked, task), shared)
		if err = ctx.Err(); err != nil {
			return err
		}
		admitted = true
	}
	var snapshot pluginSnapshot
	if e.Plugins != nil {
		acquired, err := e.Plugins.Acquire(ctx, thread.ProjectID)
		if err != nil {
			return fmt.Errorf("load project plugins: %w", err)
		}
		snapshot = acquired
		defer snapshot.Release()
	}
	return e.compactLocked(ctx, ctx, threadID, "", snapshot)
}

// compactLocked summarizes the context of every turn except currentTurn and
// stores the summary on the newest of those turns. The caller must hold the
// thread's turn lock (a manual compaction takes it; a running turn already has
// it). currentTurn is empty for a manual compaction.
func (e *Engine) compactLocked(ctx, sctx context.Context, threadID, currentTurn string, snapshot pluginSnapshot) error {
	thread, err := e.Store.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	return e.withCompactionHooks(ctx, snapshot, thread, currentTurn, func() error {
		return e.compactConversation(ctx, sctx, thread, currentTurn)
	})
}

func (e *Engine) withCompactionHooks(ctx context.Context, snapshot pluginSnapshot, thread protocol.Thread, currentTurn string, compact func() error) error {
	invocation := hooks.Invocation{Event: hooks.BeforeCompaction, ProjectID: thread.ProjectID, ThreadID: thread.ID, TurnID: currentTurn}
	e.runPluginHooks(ctx, snapshot, invocation)
	err := compact()
	invocation.Event = hooks.AfterCompaction
	if err != nil {
		invocation.ToolError = err.Error()
	}
	e.runPluginHooks(ctx, snapshot, invocation)
	return err
}

func (e *Engine) compactConversation(ctx, sctx context.Context, thread protocol.Thread, currentTurn string) error {
	// Only durable settlement and summary writes detach from execution. Allow
	// the two-minute provider request plus a bounded recovery-write interval.
	sctx, cancelStorage := context.WithTimeout(context.WithoutCancel(sctx), 2*time.Minute+30*time.Second)
	defer cancelStorage()
	threadID := thread.ID
	allTurns, err := e.Store.ListTurns(ctx, threadID)
	if err != nil {
		return err
	}
	var turns []protocol.Turn
	for _, t := range allTurns {
		if t.ID != currentTurn {
			turns = append(turns, t)
		}
	}
	if len(turns) == 0 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "send a message before compacting this chat")
	}
	items, err := e.Store.ListItems(ctx, threadID, 0)
	if err != nil {
		return err
	}
	messages := historyMessages(items, currentTurn, 0)
	if len(messages) == 0 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "there is no conversation context to compact")
	}
	transcript := compactionTranscript(messages)

	selection, err := e.resolveSelection(ctx, thread, protocol.ModelSelection{}, "Compact the current conversation context", 0)
	if err != nil {
		return fmt.Errorf("choose a model for chat compaction: %w", err)
	}
	provider, ok := e.LLMs.Get(selection.sel.Provider)
	if !ok {
		return fmt.Errorf("unknown provider %q for chat compaction", selection.sel.Provider)
	}
	maxOutput := compactionMaxOutput
	request := llm.Request{
		Model:     selection.sel.Model,
		System:    "Summarize this coding conversation for continuation in the same chat. Preserve the user's goal and constraints, decisions, relevant project/file facts, changes already made, checks and their outcomes, unresolved issues, and the next concrete steps. Do not invent facts or claim unverified checks passed. Be concise, but retain details needed to continue the work. Return only the summary.",
		Messages:  []llm.Message{llm.Text(llm.RoleUser, transcript)},
		MaxTokens: maxOutput,
	}
	e.applyAuxEffort(ctx, &selection, &request)
	compactCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	started := time.Now()
	var summary strings.Builder
	var usage llm.Usage
	shared := delegation.BudgetFromContext(ctx)
	base := request
	attempt := func() error {
		summary.Reset()
		request = base
		if shared != nil {
			budget := newTurnBudget(selection.limits)
			budget.shared = shared
			out, err := e.streamOnce(compactCtx, sctx, turns[len(turns)-1], selection, selection.cred, provider, request, "compaction", budget)
			if err != nil {
				return err
			}
			summary.WriteString(out.text)
			return nil
		}
		prepared, report, err := e.prepareModelRequest(compactCtx, sctx, turns[len(turns)-1], &selection, request, "compaction", newTurnBudget(selection.limits))
		if err != nil {
			return err
		}
		request = prepared
		events, err := provider.Stream(compactCtx, selection.cred.Material, request)
		if err != nil {
			return err
		}
		for event := range events {
			switch event.Type {
			case llm.EventTextDelta:
				summary.WriteString(event.Text)
			case llm.EventDone:
				usage = event.Usage
			case llm.EventError:
				if event.Err != nil {
					return event.Err
				}
			}
		}
		e.observeRequestUsage(selection, report, usage.InputTokens, usage.Reported)
		return nil
	}
	err = attempt()
	if err != nil && templateKwargsRejected(err, base.TemplateKwargs) {
		// The server does not accept the thinking controls: remember that and
		// retry once without them.
		e.disableTemplateKwargs(&selection)
		e.applyAuxEffort(ctx, &selection, &base)
		err = attempt()
	}
	if err != nil {
		return fmt.Errorf("compact chat context: %w", err)
	}
	if err := compactCtx.Err(); err != nil {
		return fmt.Errorf("compact chat context: %w", err)
	}
	text := strings.TrimSpace(summary.String())
	if text == "" {
		return errors.New("model returned an empty chat summary")
	}

	turn := turns[len(turns)-1]
	item, err := e.newItem(sctx, turn, protocol.ItemContextCompaction)
	if err != nil {
		return err
	}
	item.Status = protocol.ItemCompleted
	item.Text = text
	if err := e.Store.SaveItem(sctx, item); err != nil {
		return err
	}
	if shared == nil {
		if !usage.Reported {
			usage.InputTokens = llm.EstimateTokens(request.System + transcript)
			usage.OutputTokens = llm.EstimateTokens(text)
			usage.Reported = false
		}
		totals := protocol.UsageTotals{
			InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens,
			OutputTokens: usage.OutputTokens, ReasoningTokens: usage.ReasoningTokens,
			Requests: 1, Estimated: !usage.Reported,
		}
		totals.CostUSD = models.Cost(selection.meta, usage)
		if err := e.Store.InsertUsage(sctx, store.UsageRecord{
			CredentialID: selection.cred.Record.ID, Provider: selection.sel.Provider, Model: selection.sel.Model,
			ThreadID: threadID, TurnID: turn.ID, Role: "compaction", Usage: totals,
			LatencyMs: time.Since(started).Milliseconds(), Status: "ok",
		}); err != nil {
			e.Log.Warn("record compaction usage", "err", err)
		}
	}
	e.publishTurnEvent(ctx, threadID, protocol.NotifyItemCompleted, protocol.ItemEvent{Item: item})
	return nil
}

func compactionTranscript(messages []llm.Message) string {
	var b strings.Builder
	for _, message := range messages {
		role := strings.ToUpper(string(message.Role))
		text := strings.TrimSpace(message.JoinedText())
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%s:\n%s\n\n", role, text)
	}
	transcript := b.String()
	runes := []rune(transcript)
	if len(runes) <= maxCompactionInput {
		return transcript
	}
	const head = 6000
	return string(runes[:head]) + "\n[Middle of conversation omitted for the bounded compaction input.]\n" +
		string(runes[len(runes)-(maxCompactionInput-head):])
}
