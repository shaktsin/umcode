package engine

import (
	"context"
	"errors"

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
