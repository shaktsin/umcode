package delegation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestSharedBudgetReservationAndCancellation(t *testing.T) {
	b := NewBudget(protocol.ExecutionLimits{MaxTokens: 100, MaxCostUSD: 1}, protocol.UsageTotals{InputTokens: 10}, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var settlements []func(protocol.UsageTotals)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			settle, err := b.Reserve(context.Background(), id, protocol.UsageTotals{OutputTokens: 60, CostUSD: 0.6})
			if err == nil {
				mu.Lock()
				settlements = append(settlements, settle)
				mu.Unlock()
			}
		}(string(rune('a' + i)))
	}
	close(start)
	wg.Wait()
	if len(settlements) != 1 {
		t.Fatalf("admitted %d competing reservations", len(settlements))
	}
	settlements[0](protocol.UsageTotals{}) // cancelled before usage
	settle, err := b.Reserve(t.Context(), "child", protocol.UsageTotals{OutputTokens: 80})
	if err != nil {
		t.Fatal(err)
	}
	settle(protocol.UsageTotals{OutputTokens: 100, Requests: 1})
	settle(protocol.UsageTotals{OutputTokens: 100, Requests: 1})
	if got := b.Usage(); got.InputTokens != 10 || got.OutputTokens != 100 || got.Requests != 1 {
		t.Fatalf("usage reset/doubled: %+v", got)
	}
	if b.StopReason() == "" {
		t.Fatal("actual overrun did not stop next attempt")
	}
	if _, err = b.Reserve(t.Context(), "next", protocol.UsageTotals{}); err == nil {
		t.Fatal("admitted after overrun")
	}
}

func TestSharedBudgetPersistenceFailureAndCancellation(t *testing.T) {
	var saved protocol.UsageTotals
	b := NewBudget(protocol.ExecutionLimits{MaxTokens: 100, MaxCostUSD: 1}, protocol.UsageTotals{}, func(u protocol.UsageTotals) error { saved = u; return errors.New("disk failed") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := b.Reserve(ctx, "cancelled", protocol.UsageTotals{OutputTokens: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reservation: %v", err)
	}
	settle, err := b.Reserve(t.Context(), "request", protocol.UsageTotals{OutputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Reserve(t.Context(), "request", protocol.UsageTotals{}); err == nil {
		t.Fatal("duplicate attempt ID admitted")
	}
	settle(protocol.UsageTotals{OutputTokens: 5, Estimated: true, Requests: 1})
	if saved.OutputTokens != 5 || !b.Usage().Estimated {
		t.Fatal("actual/estimated usage lost")
	}
	if b.StopReason() == "" || b.Err() == nil {
		t.Fatal("persistence failure not surfaced")
	}
	if _, err = b.Reserve(t.Context(), "next", protocol.UsageTotals{}); err == nil {
		t.Fatal("admitted despite failed persistence")
	}
}

func TestSharedBudgetTaskDurationDoesNotReset(t *testing.T) {
	b := NewBudget(protocol.ExecutionLimits{MaxTokens: 100, MaxDurationMinutes: 1}, protocol.UsageTotals{}, nil, WithStartedAt(time.Now().Add(-2*time.Minute)))
	if b.StopReason() == "" {
		t.Fatal("task duration reset on restoration")
	}
	if _, err := b.Reserve(t.Context(), "expired", protocol.UsageTotals{OutputTokens: 1}); err == nil {
		t.Fatal("admitted expired task")
	}
}
