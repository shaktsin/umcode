package engine

import (
	"encoding/json"

	"github.com/shaktsin/umcode/internal/toolreduce"
)

// reduceHook is a test seam immediately before Reduce, used to prove panic fallback.
var reduceHook func()

// reduceToolResult returns a model-facing projection. The caller keeps output
// for storage, observation, hooks, and repeat detection.
func (e *Engine) reduceToolResult(name string, args json.RawMessage, output string, isError bool) (model string, report toolreduce.Report) {
	if e.Cfg == nil || !e.Cfg.Models.ToolResultReducers || output == "" {
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
	if reduceHook != nil {
		reduceHook()
	}
	projected, reduction, applied := toolreduce.Reduce(toolreduce.Input{Name: name, Args: args, Output: output, IsError: isError})
	if !applied {
		return output, reduction
	}
	if e.Log != nil {
		e.Log.Debug("tool result reduced", "tool", name, "strategy", reduction.Strategy,
			"original_tokens", reduction.OriginalTokens, "sent_tokens", reduction.SentTokens)
	}
	return projected, reduction
}
