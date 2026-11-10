package agentrt

import (
	"context"

	"github.com/shaktsin/umcode/internal/protocol"
)

// AgentStore is the persistence the runtime needs. *store.Store satisfies it.
type AgentStore interface {
	CreateRuntimeAgent(ctx context.Context, a protocol.RuntimeAgent, callID string) (protocol.RuntimeAgent, error)
	RuntimeAgentForCall(ctx context.Context, taskID, callID string) (protocol.RuntimeAgent, error)
	RuntimeAgent(ctx context.Context, taskID, id string) (protocol.RuntimeAgent, error)
	RuntimeAgents(ctx context.Context, taskID string) ([]protocol.RuntimeAgent, error)
	UpdateRuntimeAgent(ctx context.Context, a protocol.RuntimeAgent, expectedVersion int64) error
	ReconcileRuntimeAgents(ctx context.Context) error
	ApprovedAgentPlanForTask(ctx context.Context, taskID string) (protocol.AgentPlan, error)
	EnqueueAgentMessage(ctx context.Context, m protocol.AgentMessage) (protocol.AgentMessage, error)
	PendingAgentMessages(ctx context.Context, taskID, toAgentID string) ([]protocol.AgentMessage, error)
	AcknowledgeAgentMessage(ctx context.Context, taskID, id string) error
}
