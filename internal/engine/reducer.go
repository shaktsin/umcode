package engine

import (
	"context"
	"encoding/json"

	"github.com/shaktsin/umcode/internal/toolreduce"
)

// reduceToolResult returns a model-facing projection. The caller keeps output
// for storage, observation, hooks, and repeat detection.
func (e *Engine) reduceToolResult(name string, args json.RawMessage, output string, isError bool) (model string, report toolreduce.Report) {
	return e.reduceToolResultFor(context.Background(), name, args, output, isError)
}
func (e *Engine) reduceToolResultFor(ctx context.Context, name string, args json.RawMessage, output string, isError bool) (model string, report toolreduce.Report) {
	if !e.optimizationPolicy(ctx).ToolResultReducers || output == "" {
		return output, toolreduce.Report{}
	}
	model = output
	defer func() {
		if recover() != nil {
			e.toolReducerFailures.Add(1)
			if e.Log != nil {
				e.Log.Warn("tool result reducer panicked", "tool", name)
			}
			model, report = output, toolreduce.Report{}
		}
	}()
	if e.reduceHook != nil {
		e.reduceHook()
	}
	projected, reduction, applied := toolreduce.Reduce(toolreduce.Input{Name: name, Args: args, Output: output, IsError: isError})
	if !applied {
		if e.Log != nil && reduction.Declined != "" {
			e.Log.Debug("tool result reduction declined", "tool", name, "strategy", reduction.Strategy,
				"original_tokens", reduction.OriginalTokens, "sent_tokens", reduction.SentTokens,
				"decline_reason", reduction.Declined)
		}
		return output, reduction
	}
	if e.Log != nil {
		e.Log.Debug("tool result reduced", "tool", name, "strategy", reduction.Strategy,
			"original_tokens", reduction.OriginalTokens, "sent_tokens", reduction.SentTokens,
			"omitted", reduction.Omitted)
	}
	return projected, reduction
}
