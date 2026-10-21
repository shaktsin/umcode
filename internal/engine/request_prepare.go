package engine

import (
	"context"
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
	return budgetCalibratedRequest(req, capacity, capacity.ResponseReserve, e.requestEstimatorFactor(*res), measured)
}
