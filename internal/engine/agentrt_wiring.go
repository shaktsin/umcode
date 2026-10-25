package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// errExecutionAdapterPending is returned for every runtime agent until the
// runtime_v2 execution adapter exists.
var errExecutionAdapterPending = errors.New("agent execution is not available yet (runtime_v2 execution adapter pending)")

// unavailableRunner is the agentrt.Runner used while runtime_v2 is dark: it
// fails every agent clearly instead of executing it.
type unavailableRunner struct{}

func (unavailableRunner) Run(context.Context, protocol.RuntimeAgent) (protocol.AgentResult, error) {
	return protocol.AgentResult{}, errExecutionAdapterPending
}

// registerAgentRuntime builds the agent runtime on base and registers its
// model-facing tools in place of the legacy delegation agent tools. Every
// caller is the lead ("") until runtime agents run their own turns.
func (e *Engine) registerAgentRuntime(reg *tools.Registry, base context.Context) {
	e.AgentRuntime = agentrt.NewRuntime(e.Store, unavailableRunner{}, base)
	lead := func(context.Context) string { return "" }
	for _, tool := range tools.NewRuntimeAgentTools(e.AgentRuntime, e.authorizeDelegationTool, lead) {
		reg.Add(tool)
	}
}

type toolOccurrenceKey struct{}

// withToolOccurrence records the per-turn occurrence of the provider call ID
// being executed (see nextToolReductionKey): providers without call IDs reuse
// synthetic IDs such as "call_0" in every model iteration of a turn.
func withToolOccurrence(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, toolOccurrenceKey{}, n)
}

func toolOccurrenceFrom(ctx context.Context) int {
	if n, ok := ctx.Value(toolOccurrenceKey{}).(int); ok && n > 0 {
		return n
	}
	return 1
}

// runtimeAgentCallID is the idempotency key the agent runtime stores for a
// tool call: the turn, the per-turn occurrence and the provider call ID. A
// genuine retry of one call (same turn, call ID and occurrence) maps to the
// same key; a reused synthetic call ID in a later iteration does not. Turn IDs
// and occurrences contain no ':', so the provider ID is the unambiguous tail.
func runtimeAgentCallID(turnID, callID string, occurrence int) string {
	if callID == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d:%s", turnID, occurrence, callID)
}
