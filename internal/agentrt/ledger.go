package agentrt

import (
	"sync"

	"github.com/shaktsin/umcode/internal/protocol"
)

// shareEpsilon is the tolerance for float budget-share comparisons.
const shareEpsilon = 1e-9

type shareEntry struct {
	parent string
	share  float64
}

// ShareLedger tracks budget shares reserved by live agents.
type ShareLedger struct {
	mu      sync.Mutex
	entries map[string]shareEntry
}

// NewShareLedger returns an empty ledger.
func NewShareLedger() *ShareLedger {
	return &ShareLedger{entries: map[string]shareEntry{}}
}

// Free returns parentShare minus the shares held by parentID's live children.
// parentID "" is the lead (share 1.0). Results within epsilon of zero clamp to 0.
func (l *ShareLedger) Free(parentID string, parentShare float64) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	free := parentShare
	for _, e := range l.entries {
		if e.parent == parentID {
			free -= e.share
		}
	}
	if free < 0 && free > -shareEpsilon {
		return 0
	}
	return free
}

// Reserve records agentID's share under parentID, replacing any prior entry.
func (l *ShareLedger) Reserve(agentID, parentID string, share float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries[agentID] = shareEntry{parent: parentID, share: share}
}

// Release frees agentID's share; unknown IDs are a no-op.
func (l *ShareLedger) Release(agentID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, agentID)
}

// RebuildLedger reconstructs a ledger from persisted agents, counting only
// queued, running and waiting_approval agents.
func RebuildLedger(agents []protocol.RuntimeAgent) *ShareLedger {
	l := NewShareLedger()
	for _, a := range agents {
		switch a.State {
		case protocol.AgentStateQueued, protocol.AgentStateRunning, protocol.AgentStateWaitingApproval:
			l.entries[a.ID] = shareEntry{parent: a.ParentAgentID, share: a.BudgetShare}
		}
	}
	return l
}
