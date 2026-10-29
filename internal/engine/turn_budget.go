package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

type turnBudget struct {
	shared       *delegation.Budget
	limits       protocol.ExecutionLimits
	started      time.Time
	tokens       int64
	costUSD      float64
	toolRounds   int
	lastRound    string
	repeatRounds int
}

func newTurnBudget(limits protocol.ExecutionLimits) *turnBudget {
	return &turnBudget{limits: limits, started: time.Now()}
}

func (b *turnBudget) addUsage(usage protocol.UsageTotals) {
	b.tokens += usage.InputTokens + usage.OutputTokens
	b.costUSD += usage.CostUSD
}

func (b *turnBudget) stopReason() string {
	// A non-positive duration or token limit (protocol.NoLimit) is unlimited.
	timeExpired := b.limits.MaxDurationMinutes > 0 && time.Since(b.started) >= time.Duration(b.limits.MaxDurationMinutes)*time.Minute
	if b.shared != nil && !timeExpired {
		return b.shared.StopReason()
	}
	switch {
	case timeExpired:
		return fmt.Sprintf("the %d-minute time budget", b.limits.MaxDurationMinutes)
	case b.limits.MaxTokens > 0 && b.tokens >= b.limits.MaxTokens:
		return fmt.Sprintf("the %s-token budget", formatBudgetTokens(b.limits.MaxTokens))
	case b.costUSD >= b.limits.MaxCostUSD:
		return fmt.Sprintf("the $%.2f per-turn cost budget", b.limits.MaxCostUSD)
	default:
		return ""
	}
}

func (b *turnBudget) observeToolRound(calls []llm.ToolCall, results []string) int {
	h := sha256.New()
	for i, call := range calls {
		parts := [][]byte{[]byte(call.Name), call.Args}
		if i < len(results) {
			parts = append(parts, []byte(results[i]))
		}
		for _, part := range parts {
			_, _ = fmt.Fprintf(h, "%d:", len(part))
			_, _ = h.Write(part)
		}
	}
	current := hex.EncodeToString(h.Sum(nil))
	if current == b.lastRound {
		b.repeatRounds++
	} else {
		b.lastRound = current
		b.repeatRounds = 1
	}
	b.toolRounds++
	return b.repeatRounds
}

func formatBudgetTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.0fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
