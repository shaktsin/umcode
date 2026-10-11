package evaluation

import (
	"errors"
	"fmt"
)

type UsageSummary struct {
	Requests       int      `json:"requests"`
	Successes      int      `json:"successes"`
	Input          int64    `json:"input"`
	UncachedInput  int64    `json:"uncached_input"`
	CachedInput    int64    `json:"cached_input"`
	Output         int64    `json:"output"`
	Reasoning      int64    `json:"reasoning"`
	ToolRounds     int64    `json:"tool_rounds"`
	MissingUsage   int      `json:"missing_usage"`
	MissingCost    int      `json:"missing_cost"`
	CostUSD        *float64 `json:"cost_usd"`
	CostPerSuccess *float64 `json:"cost_per_success"`
	Reasons        []string `json:"reasons,omitempty"`
}

func ValidateUsage(a Attempt) error {
	if !a.Reported {
		return nil
	}
	if a.Input == nil || a.Cached == nil || a.Output == nil || a.Reasoning == nil {
		return errors.New("partial reported usage")
	}
	if *a.Input < 0 || *a.Cached < 0 || *a.Cached > *a.Input || *a.Output < 0 || *a.Reasoning < 0 || *a.Reasoning > *a.Output {
		return errors.New("invalid token subsets")
	}
	return nil
}
func AttemptCost(a Attempt, p Price) (*float64, string, error) {
	if !a.Reported {
		return nil, "usage not reported", nil
	}
	if e := ValidateUsage(a); e != nil {
		return nil, "invalid usage", e
	}
	if a.Subscription {
		return nil, "subscription billing", nil
	}
	if p.ID == "" || !finite(p.Input) || !finite(p.Cached) || !finite(p.Output) {
		return nil, "pricing unavailable", nil
	}
	v := (float64(*a.Input-*a.Cached)*p.Input + float64(*a.Cached)*p.Cached + float64(*a.Output)*p.Output) / 1e6
	return &v, "", nil
}
func Aggregate(results []TaskResult) (UsageSummary, error) {
	s := UsageSummary{}
	cost := 0.
	seen := map[string]bool{}
	for _, r := range results {
		if len(r.Attempts) == 0 {
			s.MissingUsage++
			s.MissingCost++
			s.Reasons = append(s.Reasons, r.Unit.ID+": no attempt accounting")
		}
		if r.Status == "success" {
			s.Successes++
		}
		for _, a := range r.Attempts {
			if a.ID == "" || seen[a.ID] {
				return s, fmt.Errorf("duplicate/missing dispatch identity %q", a.ID)
			}
			seen[a.ID] = true
			s.Requests++
			if a.ToolRounds < 0 {
				return s, errors.New("negative tool rounds")
			}
			s.ToolRounds += a.ToolRounds
			if a.Reported {
				if e := ValidateUsage(a); e != nil {
					return s, e
				}
				s.Input += *a.Input
				s.CachedInput += *a.Cached
				s.UncachedInput += *a.Input - *a.Cached
				s.Output += *a.Output
				s.Reasoning += *a.Reasoning
			} else {
				s.MissingUsage++
			}
			if a.ObservedUSD == nil || a.Subscription || !a.Reported {
				s.MissingCost++
				s.Reasons = append(s.Reasons, a.ID+": "+a.MissingReason)
			} else {
				if !finite(*a.ObservedUSD) {
					return s, errors.New("invalid cost")
				}
				cost += *a.ObservedUSD
			}
		}
	}
	if s.MissingCost == 0 {
		s.CostUSD = &cost
		if s.Successes > 0 {
			v := cost / float64(s.Successes)
			s.CostPerSuccess = &v
		}
	}
	return s, nil
}
