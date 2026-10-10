package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

const tokenOptimizationKey = "token_optimization.enabled"

func (e *Engine) legacyOptimizationPolicy() optimization.Policy {
	if e.Cfg == nil {
		return optimization.Policy{}
	}
	m := e.Cfg.Models
	return optimization.Policy{ContextCompiler: m.ContextCompiler, ContextRetrieval: m.ContextRetrieval, ProgressiveTools: m.ProgressiveTools, ToolResultReducers: m.ToolResultReducers, DesignedWorkflow: m.DesignedWorkflow, AutoPromote: e.Cfg.Memory.AutoPromote}
}
func (e *Engine) optimizationSetting(ctx context.Context) (optimization.Policy, protocol.TokenOptimizationResult, error) {
	var enabled *bool
	err := e.Store.GetSetting(ctx, tokenOptimizationKey, &enabled)
	if err == nil {
		if enabled == nil {
			return optimization.Policy{}, protocol.TokenOptimizationResult{}, fmt.Errorf("invalid token optimization setting")
		}
		return optimization.All(*enabled), protocol.TokenOptimizationResult{Enabled: *enabled, Source: "stored"}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return optimization.Policy{}, protocol.TokenOptimizationResult{}, fmt.Errorf("load token optimization: %w", err)
	}
	p := e.legacyOptimizationPolicy()
	count := 0
	for _, v := range []bool{p.ContextCompiler, p.ContextRetrieval, p.ProgressiveTools, p.ToolResultReducers, p.DesignedWorkflow, p.AutoPromote} {
		if v {
			count++
		}
	}
	source := "legacy"
	if count == 0 {
		source = "default"
	}
	return p, protocol.TokenOptimizationResult{Enabled: count == 6, Source: source, LegacyMixed: count > 0 && count < 6}, nil
}
func (e *Engine) TokenOptimization(ctx context.Context) (protocol.TokenOptimizationResult, error) {
	_, r, err := e.optimizationSetting(ctx)
	return r, err
}
func (e *Engine) SetTokenOptimization(ctx context.Context, p protocol.TokenOptimizationParams) (protocol.TokenOptimizationResult, error) {
	if err := e.Store.SetSetting(ctx, tokenOptimizationKey, p.Enabled); err != nil {
		return protocol.TokenOptimizationResult{}, err
	}
	return protocol.TokenOptimizationResult{Enabled: p.Enabled, Source: "stored"}, nil
}
func (e *Engine) resolveOptimizationPolicy(ctx context.Context) (optimization.Policy, error) {
	p, _, err := e.optimizationSetting(ctx)
	return p, err
}
func (e *Engine) optimizationPolicy(ctx context.Context) optimization.Policy {
	if p, ok := optimization.FromContext(ctx); ok {
		return p
	}
	return e.legacyOptimizationPolicy()
}
