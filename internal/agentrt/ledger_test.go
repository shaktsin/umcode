package agentrt

import (
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func near(a, b float64) bool { return math.Abs(a-b) <= shareEpsilon }

func TestFourQuarterSharesFitInLead(t *testing.T) {
	l := NewShareLedger()
	for i := 0; i < 4; i++ {
		l.Reserve(fmt.Sprintf("c%d", i), "", 0.25)
	}
	if f := l.Free("", 1.0); !near(f, 0) || f < 0 {
		t.Fatalf("free=%v want ~0 and non-negative", f)
	}
	if f := l.Free("", 1.0); f >= 0.25-shareEpsilon {
		t.Fatalf("fifth quarter must not fit, free=%v", f)
	}
}

func TestReleaseReturnsShare(t *testing.T) {
	l := NewShareLedger()
	l.Reserve("a", "", 0.5)
	l.Release("a")
	l.Release("unknown")
	if f := l.Free("", 1.0); f != 1.0 {
		t.Fatalf("free=%v want 1.0", f)
	}
}

func TestReserveReplacesExisting(t *testing.T) {
	l := NewShareLedger()
	l.Reserve("a", "", 0.5)
	l.Reserve("a", "", 0.3)
	if f := l.Free("", 1.0); !near(f, 0.7) {
		t.Fatalf("free=%v want 0.7", f)
	}
}

func TestGrandchildSharesComeFromParentShare(t *testing.T) {
	l := NewShareLedger()
	l.Reserve("c", "", 0.4)
	if f := l.Free("c", 0.4); f != 0.4 {
		t.Fatalf("free=%v want 0.4", f)
	}
	l.Reserve("g", "c", 0.3)
	if f := l.Free("c", 0.4); !near(f, 0.1) {
		t.Fatalf("free c=%v want 0.1", f)
	}
	if f := l.Free("", 1.0); !near(f, 0.6) {
		t.Fatalf("free lead=%v want 0.6", f)
	}
}

func TestRebuildIgnoresTerminalAgents(t *testing.T) {
	mk := func(id string, s protocol.AgentState) protocol.RuntimeAgent {
		return protocol.RuntimeAgent{ID: id, ParentAgentID: "", BudgetShare: 0.2, State: s}
	}
	l := RebuildLedger([]protocol.RuntimeAgent{
		mk("q", protocol.AgentStateQueued), mk("r", protocol.AgentStateRunning),
		mk("w", protocol.AgentStateWaitingApproval), mk("d", protocol.AgentStateCompleted),
		mk("x", protocol.AgentStateCancelled), mk("f", protocol.AgentStateFailed),
		mk("i", protocol.AgentStateInterrupted),
	})
	if f := l.Free("", 1.0); !near(f, 0.4) {
		t.Fatalf("free=%v want 0.4", f)
	}
}

func TestLedgerConcurrentReserveRelease(t *testing.T) {
	l := NewShareLedger()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("a%d", i)
			l.Reserve(id, "", 0.01)
			_ = l.Free("", 1.0)
			l.Release(id)
		}(i)
	}
	wg.Wait()
	if f := l.Free("", 1.0); f != 1.0 {
		t.Fatalf("free=%v want 1.0", f)
	}
}
