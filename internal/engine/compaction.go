package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

	return e.compactLocked(ctx, threadID, "")
}

// compactLocked summarizes the context of every turn except currentTurn and
// stores the summary on the newest of those turns. The caller must hold the
// thread's turn lock (a manual compaction takes it; a running turn already has
// it). currentTurn is empty for a manual compaction.
func (e *Engine) compactLocked(ctx context.Context, threadID, currentTurn string) error {
	thread, err := e.Store.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
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
	maxOutput := selection.preset.MaxOutputTokens
	if maxOutput <= 0 || maxOutput > 2400 {
		maxOutput = 1800
	}
	request := llm.Request{
		Model:     selection.sel.Model,
		System:    "Summarize this coding conversation for continuation in the same chat. Preserve the user's goal and constraints, decisions, relevant project/file facts, changes already made, checks and their outcomes, unresolved issues, and the next concrete steps. Do not invent facts or claim unverified checks passed. Be concise, but retain details needed to continue the work. Return only the summary.",
		Messages:  []llm.Message{llm.Text(llm.RoleUser, transcript)},
		MaxTokens: maxOutput,
		Reasoning: selection.preset.Reasoning,
	}
	compactCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	started := time.Now()
	events, err := provider.Stream(compactCtx, selection.cred.Material, request)
	if err != nil {
		return fmt.Errorf("compact chat context: %w", err)
	}
	var summary strings.Builder
	var usage llm.Usage
	for event := range events {
		switch event.Type {
		case llm.EventTextDelta:
			summary.WriteString(event.Text)
		case llm.EventDone:
			usage = event.Usage
		case llm.EventError:
			if event.Err != nil {
				return fmt.Errorf("compact chat context: %w", event.Err)
			}
		}
	}
	if err := compactCtx.Err(); err != nil {
		return fmt.Errorf("compact chat context: %w", err)
	}
	text := strings.TrimSpace(summary.String())
	if text == "" {
		return errors.New("model returned an empty chat summary")
	}

	turn := turns[len(turns)-1]
	item, err := e.newItem(ctx, turn, protocol.ItemContextCompaction)
	if err != nil {
		return err
	}
	item.Status = protocol.ItemCompleted
	item.Text = text
	if err := e.Store.SaveItem(ctx, item); err != nil {
		return err
	}
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
	if err := e.Store.InsertUsage(ctx, store.UsageRecord{
		CredentialID: selection.cred.Record.ID, Provider: selection.sel.Provider, Model: selection.sel.Model,
		ThreadID: threadID, TurnID: turn.ID, Role: "compaction", Usage: totals,
		LatencyMs: time.Since(started).Milliseconds(), Status: "ok",
	}); err != nil {
		e.Log.Warn("record compaction usage", "err", err)
	}
	e.Bus.Publish(threadID, protocol.NotifyItemCompleted, protocol.ItemEvent{Item: item})
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
