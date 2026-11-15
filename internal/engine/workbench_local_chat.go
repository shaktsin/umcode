package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/workbench"
)

type workbenchLocalRuntime interface {
	WorkbenchLocalPromotion(context.Context, string) (workbench.Promotion, error)
	LocalWorkbenchModels(context.Context, string) ([]protocol.Model, error)
	GenerateWorkbenchLocal(context.Context, string, string, int, float64) (workbench.Prediction, error)
}

func workbenchLocalPrompt(items []protocol.Item, currentTurnID, text string, window, maxOutput int) (string, bool, error) {
	return workbenchLocalPromptFormat(items, currentTurnID, text, window, maxOutput, "chat-v1")
}

func workbenchLocalPromptFormat(items []protocol.Item, currentTurnID, text string, window, maxOutput int, format string) (string, bool, error) {
	if strings.TrimSpace(text) == "" || len(text) > 8192 {
		return "", false, errors.New("enter a plain text message of at most 8,192 UTF-8 bytes")
	}
	if format == "imported-chat-v1" {
		messages := []workbench.ChatMessage{}
		var pending *workbench.ChatMessage
		pendingTurn := ""
		pairedTurn := ""
		for _, it := range items {
			if it.TurnID == currentTurnID || it.Status != protocol.ItemCompleted || strings.TrimSpace(it.Text) == "" {
				continue
			}
			if it.Kind == protocol.ItemUserMessage {
				pending = &workbench.ChatMessage{Role: "user", Content: it.Text}
				pendingTurn = it.TurnID
				pairedTurn = ""
			}
			if it.Kind == protocol.ItemAgentMessage && pending != nil && it.TurnID == pendingTurn {
				messages = append(messages, *pending, workbench.ChatMessage{Role: "assistant", Content: it.Text})
				pending = nil
				pairedTurn = it.TurnID
			} else if it.Kind == protocol.ItemAgentMessage && pairedTurn == it.TurnID && len(messages) > 0 {
				messages[len(messages)-1].Content += "\n" + it.Text
			}
		}
		messages = append(messages, workbench.ChatMessage{Role: "user", Content: text})
		raw, err := json.Marshal(messages)
		if len(messages) > 64 || len(raw) > 1<<20 {
			return "", false, errors.New("conversation is too long; start a new chat")
		}
		// The imported tokenizer checks token capacity after applying its native
		// chat template. Byte-token accounting is only correct for scratch models.
		return string(raw), false, err
	}
	current := "User: " + text + "\nAssistant:"
	switch format {
	case "", "chat-v1":
	case "prompt-response-v1":
		current = text + "\n"
	case "text-v1":
		current = text
	default:
		return "", false, errors.New("checkpoint prompt format is unsupported")
	}
	if window < 1 || maxOutput < 1 || len(current)+1+maxOutput > window {
		return "", false, fmt.Errorf("message and output exceed this checkpoint's %d-byte context; shorten the prompt or output", window)
	}
	if format == "prompt-response-v1" {
		for _, item := range items {
			if item.TurnID != currentTurnID && item.Status == protocol.ItemCompleted && (item.Kind == protocol.ItemUserMessage || item.Kind == protocol.ItemAgentMessage) {
				return "", false, errors.New("this checkpoint was trained on individual question/answer pairs; start a new chat for each question")
			}
		}
	}
	type group struct{ id, text string }
	var groups []group
	for _, it := range items {
		if it.TurnID == currentTurnID || it.Status != protocol.ItemCompleted {
			continue
		}
		role := ""
		if it.Kind == protocol.ItemUserMessage {
			role = "User"
		} else if it.Kind == protocol.ItemAgentMessage {
			role = "Assistant"
		} else {
			continue
		}
		if len(groups) == 0 || groups[len(groups)-1].id != it.TurnID {
			groups = append(groups, group{id: it.TurnID})
		}
		if format == "prompt-response-v1" || format == "text-v1" {
			groups[len(groups)-1].text += it.Text + "\n"
		} else {
			groups[len(groups)-1].text += role + ": " + it.Text + "\n"
		}
	}
	prompt := current
	omitted := false
	for i := len(groups) - 1; i >= 0; i-- {
		if len(groups[i].text)+len(prompt)+1+maxOutput > window {
			omitted = true
			break
		}
		prompt = groups[i].text + prompt
	}
	return prompt, omitted, nil
}
func (e *Engine) startWorkbenchLocalTurn(ctx context.Context, th protocol.Thread, p turnRequest, runtime workbenchLocalRuntime) (protocol.Turn, error) {
	if len(p.Attachments) > 0 || p.TaskIntent != nil || p.DelegationActivationToken != "" || p.DelegationTaskID != "" || p.Workbench != nil {
		return protocol.Turn{}, errors.New("local Workbench models accept plain text chat only; tools, attachments and assistant tasks require a compatible assistant model")
	}
	id := selectedModel(p.Override, th.Settings)
	promotion, err := runtime.WorkbenchLocalPromotion(ctx, id)
	if err != nil {
		return protocol.Turn{}, err
	}
	models, err := runtime.LocalWorkbenchModels(ctx, "workbench_local")
	if err != nil {
		return protocol.Turn{}, err
	}
	window := 0
	for _, m := range models {
		if m.ID == id {
			window = m.ContextWindow
			break
		}
	}
	maxOutput := 64
	if window/4 < maxOutput {
		maxOutput = window / 4
	}
	items, err := e.Store.ListItems(ctx, th.ID, 0)
	if err != nil {
		return protocol.Turn{}, err
	}
	prompt, omitted, err := workbenchLocalPromptFormat(items, "", p.Text, window, maxOutput, promotion.PromptFormat)
	if err != nil {
		return protocol.Turn{}, err
	}
	if omitted {
		return protocol.Turn{}, errors.New("conversation exceeds this local checkpoint’s context; start a new chat or use a checkpoint with a longer context")
	}
	e.mu.Lock()
	if e.shuttingDown {
		e.mu.Unlock()
		return protocol.Turn{}, errors.New("engine is shutting down")
	}
	if running, ok := e.threadTurns[th.ID]; ok {
		e.mu.Unlock()
		return protocol.Turn{}, fmt.Errorf("turn %s is still running in this thread", running)
	}
	turn := protocol.Turn{ID: store.NewID("trn"), ThreadID: th.ID, Status: protocol.TurnRunning, Selection: p.Override, Resolved: protocol.ModelSelection{Provider: "workbench_local", Model: id}, StartedAt: time.Now().UTC()}
	runCtx, cancel := context.WithCancel(e.baseCtx)
	e.wg.Add(1)
	e.threadTurns[th.ID] = turn.ID
	e.activeTurns[turn.ID] = &activeTurn{cancel: cancel, turn: turn}
	if p.wait != nil {
		e.turnWaiters[turn.ID] = p.wait
	}
	e.mu.Unlock()
	release := func() {
		cancel()
		e.mu.Lock()
		delete(e.threadTurns, th.ID)
		delete(e.activeTurns, turn.ID)
		e.mu.Unlock()
	}
	if err = e.Store.CreateTurn(ctx, turn); err != nil {
		release()
		e.mu.Lock()
		delete(e.turnWaiters, turn.ID)
		e.mu.Unlock()
		e.wg.Done()
		return protocol.Turn{}, err
	}
	e.publishTurnEvent(ctx, th.ID, protocol.NotifyTurnStarted, protocol.TurnEvent{Turn: turn})
	user, err := e.newItem(ctx, turn, protocol.ItemUserMessage)
	if err == nil {
		user.Status = protocol.ItemCompleted
		user.Text = p.Text
		err = e.saveAndPublish(ctx, user, protocol.NotifyItemCompleted)
	}
	if err != nil {
		release()
		now := time.Now().UTC()
		turn.Status = protocol.TurnFailed
		turn.Error = err.Error()
		turn.FinishedAt = &now
		_ = e.Store.FinishTurn(context.WithoutCancel(ctx), turn)
		e.mu.Lock()
		delete(e.turnWaiters, turn.ID)
		e.mu.Unlock()
		e.wg.Done()
		return protocol.Turn{}, err
	}
	go func(turn protocol.Turn) {
		defer e.wg.Done()
		sctx := context.WithoutCancel(runCtx)
		agent, runErr := e.newItem(sctx, turn, protocol.ItemAgentMessage)
		if runErr == nil {
			runErr = e.saveAndPublish(sctx, agent, protocol.NotifyItemStarted)
		}
		var result workbench.Prediction
		if runErr == nil {
			result, runErr = runtime.GenerateWorkbenchLocal(runCtx, id, prompt, maxOutput, 0)
		}
		if runErr == nil && result.CheckpointID != promotion.CheckpointID {
			runErr = errors.New("local runtime returned a different checkpoint")
		}
		if runErr == nil && strings.TrimSpace(result.Text) == "" {
			runErr = errors.New("local checkpoint generated no text; test a different prompt in Inference")
		}
		if runCtx.Err() != nil {
			runErr = runCtx.Err()
		}
		if runErr == nil {
			agent.Text = result.Text
			e.publishTurnEvent(sctx, th.ID, protocol.NotifyItemDelta, protocol.ItemDelta{ThreadID: th.ID, TurnID: turn.ID, ItemID: agent.ID, Text: result.Text})
			agent.Status = protocol.ItemCompleted
			runErr = e.saveAndPublish(sctx, agent, protocol.NotifyItemCompleted)
		}
		now := time.Now().UTC()
		turn.FinishedAt = &now
		switch {
		case runErr == nil:
			turn.Status = protocol.TurnCompleted
		case errors.Is(runErr, context.Canceled):
			turn.Status = protocol.TurnInterrupted
			turn.Error = "interrupted"
		default:
			turn.Status = protocol.TurnFailed
			turn.Error = runErr.Error()
		}
		if runErr != nil {
			if agent.ID != "" {
				agent.Status = protocol.ItemCompleted
				agent.Text = ""
				_ = e.Store.SaveItem(sctx, agent)
			}
			if it, err := e.newItem(sctx, turn, protocol.ItemError); err == nil {
				it.Status = protocol.ItemCompleted
				it.Text = turn.Error
				_ = e.saveAndPublish(sctx, it, protocol.NotifyItemCompleted)
			}
		}
		if err := e.Store.FinishTurn(sctx, turn); err != nil {
			e.Log.Error("finish local inference turn", "err", err)
		}
		_ = e.Store.TouchThread(sctx, th.ID)
		if th.Title == "" && runErr == nil {
			title := []rune(strings.TrimSpace(p.Text))
			if len(title) > 60 {
				title = title[:60]
			}
			_ = e.Store.UpdateThread(sctx, th.ID, map[string]any{"title": string(title)})
		}
		release()
		e.publishTurnEvent(sctx, th.ID, protocol.NotifyTurnCompleted, protocol.TurnEvent{Turn: turn})
		e.publishThread(sctx, th.ID)
		e.mu.Lock()
		if ch, ok := e.turnWaiters[turn.ID]; ok {
			ch <- turn
			delete(e.turnWaiters, turn.ID)
		}
		e.mu.Unlock()
	}(turn)
	return turn, nil
}
