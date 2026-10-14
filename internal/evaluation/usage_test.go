package evaluation

import (
	"math"
	"testing"
)

func i64(n int64) *int64     { return &n }
func usd(n float64) *float64 { return &n }
func goodAttempt() Attempt {
	return Attempt{ID: "one", Reported: true, Input: i64(100), Cached: i64(40), Output: i64(20), Reasoning: i64(8), ObservedUSD: usd(.001), Outcome: "ok"}
}
func TestEvaluationTokenSubsets(t *testing.T) {
	a := goodAttempt()
	summary, err := Aggregate([]TaskResult{{Status: "success", Attempts: []Attempt{a}}})
	if err != nil || summary.UncachedInput != 60 || summary.Output != 20 {
		t.Fatal(summary, err)
	}
	price := Price{ID: "p", Input: 10, Cached: 1, Output: 20}
	got, reason, err := AttemptCost(a, price)
	if err != nil || reason != "" || math.Abs(*got-.00104) > 1e-10 {
		t.Fatal(got, reason, err)
	}
	a.Cached = i64(101)
	if _, _, err := AttemptCost(a, price); err == nil {
		t.Fatal("cached subset")
	}
	a = goodAttempt()
	a.Reasoning = i64(21)
	if _, err := Aggregate([]TaskResult{{Attempts: []Attempt{a}}}); err == nil {
		t.Fatal("reasoning subset")
	}
	a = goodAttempt()
	a.Subscription = true
	if cost, why, _ := AttemptCost(a, price); cost != nil || why == "" {
		t.Fatal("subscription priced")
	}
	a = goodAttempt()
	a.Reported = false
	if cost, why, _ := AttemptCost(a, price); cost != nil || why == "" {
		t.Fatal("missing usage priced")
	}
}
func TestEvaluationFailureDenominators(t *testing.T) {
	a := goodAttempt()
	a.ObservedUSD = usd(2)
	b := a
	b.ID = "two"
	b.Outcome = "error"
	b.ObservedUSD = usd(3)
	s, err := Aggregate([]TaskResult{{Status: "success", Attempts: []Attempt{a}}, {Status: "failed", Attempts: []Attempt{b}}})
	if err != nil || s.Successes != 1 || s.Requests != 2 || s.CostPerSuccess == nil || *s.CostPerSuccess != 5 {
		t.Fatal(s, err)
	}
	s, _ = Aggregate([]TaskResult{{Status: "failed", Attempts: []Attempt{b}}})
	if s.CostPerSuccess != nil {
		t.Fatal("zero success is free")
	}
	b.Reported = false
	b.ObservedUSD = nil
	s, _ = Aggregate([]TaskResult{{Status: "success", Attempts: []Attempt{a, b}}})
	if s.CostUSD != nil || s.MissingUsage != 1 {
		t.Fatal("missing hidden", s)
	}
}
