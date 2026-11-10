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

func TestBudgetSliceForwardsAndCaps(t *testing.T) {
	parent := NewBudget(protocol.ExecutionLimits{MaxTokens: 100, MaxCostUSD: 1}, protocol.UsageTotals{}, nil)
	child := parent.Slice(0.25)
	if tok, cost := child.Remaining(); tok != 25 || cost != 0.25 {
		t.Fatalf("slice remaining = %d, %v", tok, cost)
	}
	if _, err := child.Reserve(t.Context(), "big", protocol.UsageTotals{OutputTokens: 26}); err == nil {
		t.Fatal("slice admitted more than its share")
	}
	if got := parent.Usage(); got.OutputTokens != 0 {
		t.Fatalf("rejected reservation leaked to parent: %+v", got)
	}
	settle, err := child.Reserve(t.Context(), "r1", protocol.UsageTotals{OutputTokens: 25, CostUSD: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	if tok, _ := parent.Remaining(); tok != 75 {
		t.Fatalf("parent did not hold the reservation: %d", tok)
	}
	settle(protocol.UsageTotals{OutputTokens: 25, CostUSD: 0.1, Requests: 1})
	if child.StopReason() == "" {
		t.Fatal("slice did not stop at its share")
	}
	if _, err = child.Reserve(t.Context(), "r2", protocol.UsageTotals{}); err == nil {
		t.Fatal("exhausted slice admitted")
	}
	if got := parent.Usage(); got.OutputTokens != 25 || got.Requests != 1 || got.CostUSD != 0.1 {
		t.Fatalf("parent totals: %+v", got)
	}
	if parent.StopReason() != "" {
		t.Fatal("parent stopped by child exhaustion")
	}
	// A second slice is cut from what remains.
	if tok, _ := parent.Slice(0.5).Remaining(); tok != 37 {
		t.Fatalf("second slice = %d, want 37", tok)
	}
}

func TestBudgetSliceClampAndUnlimited(t *testing.T) {
	parent := NewBudget(protocol.ExecutionLimits{MaxTokens: 100}, protocol.UsageTotals{}, nil)
	if tok, _ := parent.Slice(7).Remaining(); tok != 100 {
		t.Fatalf("share>1 not clamped: %d", tok)
	}
	if c := parent.Slice(-1); c.StopReason() == "" {
		if tok, _ := c.Remaining(); tok != 0 {
			t.Fatalf("share<=0 clamp: %d", tok)
		}
	}
	free := NewBudget(protocol.ExecutionLimits{}, protocol.UsageTotals{}, nil)
	c := free.Slice(0.5)
	if c.StopReason() != "" {
		t.Fatal("slice of unlimited budget is stopped")
	}
	if _, err := c.Reserve(t.Context(), "x", protocol.UsageTotals{OutputTokens: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	spent := NewBudget(protocol.ExecutionLimits{MaxTokens: 10}, protocol.UsageTotals{InputTokens: 10}, nil)
	if spent.Slice(1).StopReason() == "" {
		t.Fatal("slice of exhausted parent is not stopped")
	}
}

func TestBudgetSliceStopsWithParent(t *testing.T) {
	parent := NewBudget(protocol.ExecutionLimits{MaxTokens: 100}, protocol.UsageTotals{}, nil)
	owned, cancel := context.WithCancel(context.Background())
	parent.owner = owned
	child := parent.Slice(0.5)
	if child.StopReason() != "" {
		t.Fatal("fresh slice stopped")
	}
	cancel()
	if child.StopReason() == "" {
		t.Fatal("slice outlived cancelled parent")
	}
	if _, err := child.Reserve(context.Background(), "x", protocol.UsageTotals{}); err == nil {
		t.Fatal("slice admitted after parent cancel")
	}
	// Parent accounting failure propagates through Err.
	p2 := NewBudget(protocol.ExecutionLimits{MaxTokens: 100}, protocol.UsageTotals{}, func(protocol.UsageTotals) error { return errors.New("disk") })
	c2 := p2.Slice(1)
	settle, err := c2.Reserve(t.Context(), "a", protocol.UsageTotals{OutputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	settle(protocol.UsageTotals{OutputTokens: 1})
	if c2.Err() == nil || c2.StopReason() == "" {
		t.Fatalf("parent accounting failure not propagated: %v %q", c2.Err(), c2.StopReason())
	}
}
