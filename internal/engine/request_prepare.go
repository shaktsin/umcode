package engine

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"time"
)

// prepareModelRequest is the final capacity admission for the selected route.
// It never changes durable history or increases the execution budget.
func (e *Engine) prepareModelRequest(ctx, sctx context.Context, turn protocol.Turn, res *resolved, req llm.Request, phase string, budget *turnBudget) (llm.Request, requestBudgetReport, error) {
	capacity, err := e.resolveCapacity(ctx, *res)
	if err != nil {
		return llm.Request{}, requestBudgetReport{}, err
	}
	res.meta.ContextWindow = capacity.Window
	for pass := 0; ; pass++ {
		var measured *int
		if e.LLMs != nil {
			if provider, ok := e.LLMs.Get(res.sel.Provider); ok {
				if counter, ok := provider.(llm.RequestTokenCounter); ok {
					countCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
					n, countErr := counter.CountRequestTokens(countCtx, res.cred.Material, req)
					cancel()
					if countErr == nil && n > 0 {
						measured = &n
					}
				}
			}
		}
		prepared, report, fitErr := budgetCalibratedRequest(req, capacity, capacity.ResponseReserve, e.requestEstimatorFactor(*res), measured)
		if fitErr == nil {
			report.Reductions = pass
			if pass > 0 {
				if turn.ID != "" && turn.ThreadID != "" {
					item, err := e.newItem(sctx, turn, protocol.ItemContextCompaction)
					if err != nil {
						return llm.Request{}, report, err
					}
					item.Status = protocol.ItemCompleted
					item.Text = "Automatic working-context checkpoint. Original messages and tool outputs remain saved and retrievable."
					item.Data, _ = json.Marshal(map[string]any{"workingMessages": prepared.Messages})
					if err = e.Store.SaveItem(sctx, item); err != nil {
						return llm.Request{}, report, err
					}
				}
				res.workingMessages = prepared.Messages
				res.contextReduced = true
			}
			return prepared, report, nil
		}
		if !errors.Is(fitErr, errRequestOverCapacity) || phase != "chat" || pass >= 2 {
			return llm.Request{}, report, fitErr
		}
		reduced, err := e.compactLiveMessages(ctx, sctx, turn, res, req.Messages, budget)
		if err != nil {
			return llm.Request{}, report, err
		}
		before, _ := countRequestInput(req)
		next := cloneModelRequest(req)
		next.Messages = reduced
		after, _ := countRequestInput(next)
		if after.EstimatedInput >= before.EstimatedInput {
			return llm.Request{}, report, errCompactionNoReduction
		}
		req = next
	}
}
