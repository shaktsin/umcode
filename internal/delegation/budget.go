package delegation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Budget serializes estimated reservations and actual provider settlements for
// an entire task. Estimates bound admission, not the provider's final bill.
// persist receives cumulative actual totals under the accounting lock; it must
// not call back into Budget. A failed write retains actuals and closes admission.
type Budget struct {
	owner            context.Context
	deadline         time.Time
	mu               sync.Mutex
	limits           protocol.ExecutionLimits
	actual, reserved protocol.UsageTotals
	attempts         map[string]bool
	persist          func(protocol.UsageTotals) error
	err              error
}

type BudgetOption func(*Budget)

// WithStartedAt preserves a task's original wall-clock duration on restoration.
func WithStartedAt(started time.Time) BudgetOption {
	return func(b *Budget) {
		if b.limits.MaxDurationMinutes > 0 {
			b.deadline = started.Add(time.Duration(b.limits.MaxDurationMinutes) * time.Minute)
		}
	}
}

func NewBudget(limits protocol.ExecutionLimits, restored protocol.UsageTotals, persist func(protocol.UsageTotals) error, options ...BudgetOption) *Budget {
	b := &Budget{limits: limits, actual: restored, attempts: make(map[string]bool), persist: persist}
	WithStartedAt(time.Now())(b)
	for _, option := range options {
		option(b)
	}
	return b
}

// Context returns the coordinator-owned task lifetime, or nil for standalone
// budgets. Internal child adapters must retain this lifetime when rebuilding
// their execution context from the engine base context.
func (b *Budget) Context() context.Context { b.mu.Lock(); defer b.mu.Unlock(); return b.owner }

func (b *Budget) reasonLocked() string {
	switch {
	case b.owner != nil && b.owner.Err() != nil:
		return fmt.Sprintf("shared task stopped: %v", b.owner.Err())
	case !b.deadline.IsZero() && !time.Now().Before(b.deadline):
		return "the shared task time budget"
	case b.err != nil:
		return fmt.Sprintf("shared task accounting unavailable: %v", b.err)
	case b.limits.MaxTokens > 0 && b.actual.InputTokens+b.actual.OutputTokens >= b.limits.MaxTokens:
		return "the shared task token budget"
	case b.limits.MaxCostUSD > 0 && b.actual.CostUSD >= b.limits.MaxCostUSD:
		return "the shared task cost budget"
	}
	return ""
}
func (b *Budget) StopReason() string          { b.mu.Lock(); defer b.mu.Unlock(); return b.reasonLocked() }
func (b *Budget) Err() error                  { b.mu.Lock(); defer b.mu.Unlock(); return b.err }
func (b *Budget) Usage() protocol.UsageTotals { b.mu.Lock(); defer b.mu.Unlock(); return b.actual }

// Remaining includes in-flight reservations. Only Reserve atomically admits a
// request; callers must handle a concurrent reservation consuming this snapshot.
func (b *Budget) Remaining() (tokens int64, cost float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tokens = math.MaxInt64
	cost = math.MaxFloat64
	if b.limits.MaxTokens > 0 {
		tokens = b.limits.MaxTokens - b.actual.InputTokens - b.actual.OutputTokens - b.reserved.InputTokens - b.reserved.OutputTokens
	}
	if b.limits.MaxCostUSD > 0 {
		cost = b.limits.MaxCostUSD - b.actual.CostUSD - b.reserved.CostUSD
	}
	return
}

func (b *Budget) Reserve(ctx context.Context, requestID string, allowance protocol.UsageTotals) (func(actual protocol.UsageTotals), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if requestID == "" || allowance.InputTokens < 0 || allowance.OutputTokens < 0 || allowance.CostUSD < 0 || math.IsNaN(allowance.CostUSD) || math.IsInf(allowance.CostUSD, 0) {
		return nil, errors.New("invalid shared budget reservation")
	}
	if reason := b.reasonLocked(); reason != "" {
		return nil, errors.New(reason)
	}
	if b.attempts[requestID] {
		return nil, errors.New("duplicate provider attempt")
	}
	tokens := b.actual.InputTokens + b.actual.OutputTokens + b.reserved.InputTokens + b.reserved.OutputTokens
	if b.limits.MaxTokens > 0 && (allowance.InputTokens > b.limits.MaxTokens-tokens || allowance.OutputTokens > b.limits.MaxTokens-tokens-allowance.InputTokens) {
		return nil, errors.New("insufficient shared task token allowance")
	}
	if b.limits.MaxCostUSD > 0 && allowance.CostUSD > b.limits.MaxCostUSD-b.actual.CostUSD-b.reserved.CostUSD {
		return nil, errors.New("insufficient shared task cost allowance")
	}
	b.attempts[requestID] = true
	b.reserved.Add(allowance)
	var once sync.Once
	return func(actual protocol.UsageTotals) {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.reserved.InputTokens -= allowance.InputTokens
			b.reserved.OutputTokens -= allowance.OutputTokens
			b.reserved.CostUSD -= allowance.CostUSD
			b.actual.Add(actual)
			if b.persist != nil {
				if err := b.persist(b.actual); err != nil {
					b.err = fmt.Errorf("persist task usage: %w", err)
				}
			}
		})
	}, nil
}

type budgetKey struct{}

func WithBudget(ctx context.Context, budget *Budget) context.Context {
	return context.WithValue(ctx, budgetKey{}, budget)
}
func BudgetFromContext(ctx context.Context) *Budget {
	b, _ := ctx.Value(budgetKey{}).(*Budget)
	return b
}
