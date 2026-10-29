package engine

import (
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestTurnBudgetStopsOnTokenAndCostUsage(t *testing.T) {
	limits := protocol.ExecutionLimits{MaxDurationMinutes: 5, MaxTokens: 20_000, MaxCostUSD: 1, MaxToolRounds: 200}
	b := newTurnBudget(limits)
	b.addUsage(protocol.UsageTotals{InputTokens: 12_000, OutputTokens: 8_000, CostUSD: 0.2})
	if got := b.stopReason(); got == "" {
		t.Fatal("expected token budget to stop the turn")
	}

	b = newTurnBudget(limits)
	b.addUsage(protocol.UsageTotals{InputTokens: 10, OutputTokens: 10, CostUSD: 1})
	if got := b.stopReason(); got == "" {
		t.Fatal("expected cost budget to stop the turn")
	}
}

func TestTurnBudgetDetectsIdenticalToolRounds(t *testing.T) {
	b := newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: 5, MaxTokens: 20_000, MaxCostUSD: 1, MaxToolRounds: 200})
	calls := []llm.ToolCall{{Name: "read_file", Args: []byte(`{"path":"main.go"}`)}}
	if got := b.observeToolRound(calls, []string{"same file contents"}); got != 1 {
		t.Fatalf("first round repeat count = %d, want 1", got)
	}
	if got := b.observeToolRound(calls, []string{"same file contents"}); got != 2 {
		t.Fatalf("second round repeat count = %d, want 2", got)
	}
	if got := b.observeToolRound(calls, []string{"changed file contents"}); got != 1 {
		t.Fatalf("changed result repeat count = %d, want 1", got)
	}
}

func TestValidExecutionLimitsNoLimit(t *testing.T) {
	ok := protocol.ExecutionLimits{MaxDurationMinutes: 120, MaxTokens: 1_000_000, MaxCostUSD: 10, MaxToolRounds: 200}
	if !validExecutionLimits(ok) {
		t.Fatal("baseline invalid")
	}
	l := ok
	l.MaxDurationMinutes, l.MaxTokens = protocol.NoLimit, protocol.NoLimit
	if !validExecutionLimits(l) {
		t.Fatal("-1 should be accepted for time and tokens")
	}
	for _, mut := range []func(*protocol.ExecutionLimits){
		func(x *protocol.ExecutionLimits) { x.MaxDurationMinutes = -2 },
		func(x *protocol.ExecutionLimits) { x.MaxDurationMinutes = 0 },
		func(x *protocol.ExecutionLimits) { x.MaxTokens = -2 },
		func(x *protocol.ExecutionLimits) { x.MaxTokens = 0 },
		func(x *protocol.ExecutionLimits) { x.MaxCostUSD = -1 },
		func(x *protocol.ExecutionLimits) { x.MaxToolRounds = -1 },
	} {
		x := ok
		mut(&x)
		if validExecutionLimits(x) {
			t.Fatalf("accepted %+v", x)
		}
	}
}

func TestTurnBudgetUnlimitedTimeAndTokensNeverStop(t *testing.T) {
	b := newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: protocol.NoLimit, MaxTokens: protocol.NoLimit, MaxCostUSD: 1})
	b.started = time.Now().Add(-1000 * time.Hour)
	b.addUsage(protocol.UsageTotals{InputTokens: 50_000_000, OutputTokens: 50_000_000, CostUSD: 0.5})
	if got := b.stopReason(); got != "" {
		t.Fatalf("unlimited budget stopped: %q", got)
	}
	b.addUsage(protocol.UsageTotals{CostUSD: 0.5})
	if got := b.stopReason(); got == "" {
		t.Fatal("cost limit must still stop")
	}
}

func TestTurnBudgetFiniteLimitsStillStopWhenOtherIsUnlimited(t *testing.T) {
	b := newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: 5, MaxTokens: protocol.NoLimit, MaxCostUSD: 10})
	b.started = time.Now().Add(-6 * time.Minute)
	if got := b.stopReason(); got == "" {
		t.Fatal("finite time limit did not stop")
	}
	b = newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: protocol.NoLimit, MaxTokens: 20_000, MaxCostUSD: 10})
	b.addUsage(protocol.UsageTotals{InputTokens: 20_000})
	if got := b.stopReason(); got == "" {
		t.Fatal("finite token limit did not stop")
	}
}

func TestTurnBudgetUnlimitedTimeConsultsSharedBudget(t *testing.T) {
	limits := protocol.ExecutionLimits{MaxDurationMinutes: protocol.NoLimit, MaxTokens: 100, MaxCostUSD: 10}
	b := newTurnBudget(limits)
	b.started = time.Now().Add(-1000 * time.Hour)
	b.shared = delegation.NewBudget(limits, protocol.UsageTotals{InputTokens: 100}, nil)
	if got := b.stopReason(); got != "the shared task token budget" {
		t.Fatalf("shared stop reason = %q", got)
	}
	b.shared = delegation.NewBudget(protocol.ExecutionLimits{MaxDurationMinutes: protocol.NoLimit, MaxTokens: protocol.NoLimit, MaxCostUSD: 10}, protocol.UsageTotals{InputTokens: 1 << 40}, nil)
	if got := b.stopReason(); got != "" {
		t.Fatalf("unlimited shared budget stopped: %q", got)
	}
}
