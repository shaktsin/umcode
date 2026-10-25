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
// model-facing tools in place of the legacy delegation agent tools. The lead
// keeps the default RootCapabilities authority (agentrt.WithLeadCapabilities).
func (e *Engine) registerAgentRuntime(reg *tools.Registry, base context.Context) {
	e.AgentRuntime = agentrt.NewRuntime(e.Store, unavailableRunner{}, base)
	for _, tool := range tools.NewRuntimeAgentTools(e.AgentRuntime, e.authorizeDelegationTool, runtimeCaller) {
		reg.Add(tool)
	}
}

// runtimeCaller resolves the calling agent for the runtime agent tools. It
// fails closed: only the lead's own turn (no child execution marker) resolves,
// as the lead (""). Child contexts do not resolve until runtime agents run
// their own turns and carry their agent ID (TODO(Plan 2): resolve the runtime
// agent's ID from its execution context here).
func runtimeCaller(ctx context.Context) (string, bool) {
	if delegationChild(ctx) || childExecutionFrom(ctx) != nil {
		return "", false
	}
	return "", true
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
