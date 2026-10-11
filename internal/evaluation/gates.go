package evaluation

import (
	"fmt"
	"sort"
)

type Verdict string

const (
	Pass         Verdict = "pass"
	Fail         Verdict = "fail"
	Inconclusive Verdict = "inconclusive"
)

func combine(values ...Verdict) Verdict {
	out := Pass
	for _, v := range values {
		if v == Fail {
			return Fail
		}
		if v != Pass {
			out = Inconclusive
		}
	}
	return out
}
func efficiencyGate(base, treatment, minimumReduction float64) Verdict {
	if !finite(base) || !finite(treatment) || base <= 0 {
		return Inconclusive
	}
	if (base-treatment)/base+1e-12 < minimumReduction {
		return Fail
	}
	return Pass
}

type Gate struct {
	Name      string   `json:"name"`
	Verdict   Verdict  `json:"verdict"`
	Reason    string   `json:"reason"`
	Threshold float64  `json:"threshold"`
	Point     *float64 `json:"point,omitempty"`
	Lower     *float64 `json:"lower,omitempty"`
	N         int      `json:"n"`
	Wins      int      `json:"wins"`
	Losses    int      `json:"losses"`
}
type Stratum struct {
	Model           string       `json:"model"`
	Category        string       `json:"category"`
	Baseline        string       `json:"baseline"`
	Treatment       string       `json:"treatment"`
	Required        bool         `json:"required"`
	Quality         Gate         `json:"quality"`
	BaselineUsage   UsageSummary `json:"baseline_usage"`
	TreatmentUsage  UsageSummary `json:"treatment_usage"`
	CommonBaseline  UsageSummary `json:"common_baseline"`
	CommonTreatment UsageSummary `json:"common_treatment"`
}
type Report struct {
	Version        int       `json:"version"`
	InputHash      string    `json:"input_hash"`
	ReviewID       string    `json:"review_id"`
	Regression     Verdict   `json:"regression"`
	LiveRelease    Verdict   `json:"live_release"`
	ParentCoverage Verdict   `json:"parent_coverage"`
	Limitations    []string  `json:"limitations"`
	Strata         []Stratum `json:"strata"`
	Efficiency     []Gate    `json:"efficiency"`
}

func mandatoryChecks(r TaskResult, t Task) (passed, known bool) {
	if r.Status == "failed" {
		return false, true
	}
	if r.Status != "completed" && r.Status != "success" {
		return false, false
	}
	byID := map[string]CheckResult{}
	for _, c := range r.Checks {
		byID[c.ID] = c
	}
	for _, expected := range t.Checks {
		c, ok := byID[expected.ID]
		if !ok || c.Status == "incomplete" {
			return false, false
		}
		if c.Status != "passed" {
			return false, true
		}
	}
	return len(t.Checks) > 0, true
}
func resolvedScores(c Campaign, rev ReviewRevision) map[string]ReviewScore {
	out := map[string]ReviewScore{}
	if rev.ID == "" || rev.Conflicted {
		return out
	}
	found := false
	for _, v := range c.Reviews {
		if Hash(v) == Hash(rev) {
			found = true
		}
	}
	if !found {
		return out
	}
	seen := map[string]bool{}
	var visit func(ReviewRevision) bool
	visit = func(r ReviewRevision) bool {
		if seen[r.ID] || r.Conflicted {
			return false
		}
		seen[r.ID] = true
		if r.PriorID != "" {
			ok := false
			for _, old := range c.Reviews {
				if old.ID == r.PriorID {
					ok = visit(old)
				}
			}
			if !ok {
				return false
			}
		}
		for _, s := range r.Scores {
			out[s.OpaqueID] = s
		}
		return true
	}
	if !visit(rev) {
		return map[string]ReviewScore{}
	}
	return out
}
func Evaluate(c Campaign, review ReviewRevision) Report {
	report := Report{Version: 1, InputHash: Hash(c), ReviewID: review.ID, Regression: Pass, LiveRelease: Pass, ParentCoverage: Inconclusive, Limitations: []string{"Historical instruction/Work/evidence and adaptive-routing comparisons are unsupported; parent-program coverage remains inconclusive."}}
	scores := resolvedScores(c, review)
	categories := map[string]bool{}
	for _, t := range c.Manifest.Tasks {
		categories[t.Category] = true
	}
	names := []string{}
	for name := range categories {
		names = append(names, name)
	}
	sort.Strings(names)
	k := max(1, len(names)*len(c.Manifest.Models))
	byUnit := map[string]TaskResult{}
	for _, r := range c.Results {
		byUnit[r.Unit.ID] = r
		for _, check := range r.Checks {
			if check.Safety && (check.Status == "failed" || check.SafetyViolations != nil && *check.SafetyViolations > 0) {
				report.Regression = Fail
				report.LiveRelease = Fail
			}
		}
	}
	units := c.Units
	if len(units) == 0 {
		units, _ = Schedule(c.Manifest)
	}
	if len(c.Results) != len(units) || c.Phase != "review-pending" && c.Phase != "scored" {
		report.Regression = combine(report.Regression, Inconclusive)
		report.LiveRelease = combine(report.LiveRelease, Inconclusive)
		report.Limitations = append(report.Limitations, "Campaign has not reached its complete predeclared endpoint.")
	}
	success := func(r TaskResult) (bool, bool) {
		task, ok := c.Manifest.Task(r.Unit.TaskID)
		if !ok {
			return false, false
		}
		passed, known := mandatoryChecks(r, task)
		if !c.Live {
			return passed, known
		}
		s, ok := scores[OpaqueID(c, r.Unit.ID)]
		if !ok || s.ArtifactHash != r.ArtifactHash || s.Score == nil || s.SafetyViolations == nil || s.UnsupportedClaims == nil || s.ContextOmissions == nil || s.StaleAuthority == nil {
			return false, false
		}
		return passed && *s.Score >= 3 && *s.SafetyViolations == 0 && *s.UnsupportedClaims == 0 && *s.ContextOmissions == 0 && *s.StaleAuthority == 0, known
	}
	hasPrimary := false
	for _, model := range c.Manifest.Models {
		for _, pair := range c.Manifest.Comparisons {
			baseArm, _ := c.Manifest.Arm(pair.Baseline)
			treatArm, _ := c.Manifest.Arm(pair.Treatment)
			required := baseArm.Mode == "product-off" && treatArm.Mode == "product-on"
			hasPrimary = hasPrimary || required
			for _, category := range names {
				s := Stratum{Model: model.ID, Category: category, Baseline: pair.Baseline, Treatment: pair.Treatment, Required: required, Quality: Gate{Name: "quality", Verdict: Pass, Threshold: -.02}}
				var baseline, treatment, commonB, commonT []TaskResult
				clusterB, clusterT := map[string]bool{}, map[string]bool{}
				clusterKnown := map[string]bool{}
				clusterArms := map[string]map[string]bool{}
				highRisk := map[string]bool{}
				missing := false
				for _, unit := range units {
					if unit.ModelID != model.ID || unit.ArmID != pair.Baseline && unit.ArmID != pair.Treatment {
						continue
					}
					task, _ := c.Manifest.Task(unit.TaskID)
					if task.Category != category {
						continue
					}
					r, ok := byUnit[unit.ID]
					if !ok {
						missing = true
						continue
					}
					passed, known := success(r)
					if !known {
						missing = true
					}
					if passed {
						r.Status = "success"
					}
					if unit.ArmID == pair.Baseline {
						baseline = append(baseline, r)
					} else {
						treatment = append(treatment, r)
					}
					if unit.Primary {
						cluster := unit.Cluster
						if clusterArms[cluster] == nil {
							clusterArms[cluster] = map[string]bool{}
						}
						clusterArms[cluster][unit.ArmID] = true
						if _, ok := clusterKnown[cluster]; !ok {
							clusterKnown[cluster] = true
							clusterB[cluster] = true
							clusterT[cluster] = true
						}
						clusterKnown[cluster] = clusterKnown[cluster] && known
						highRisk[cluster] = highRisk[cluster] || task.HighRisk
						if unit.ArmID == pair.Baseline {
							clusterB[cluster] = clusterB[cluster] && passed
						} else {
							clusterT[cluster] = clusterT[cluster] && passed
						}
					}
					if c.Live && known {
						score := scores[OpaqueID(c, unit.ID)]
						if score.SafetyViolations != nil && *score.SafetyViolations > 0 {
							s.Quality.Verdict = Fail
						}
					}
				}
				for cluster, known := range clusterKnown {
					if !known || !clusterArms[cluster][pair.Baseline] || !clusterArms[cluster][pair.Treatment] {
						continue
					}
					s.Quality.N++
					b, t := clusterB[cluster], clusterT[cluster]
					if t && !b {
						s.Quality.Wins++
					}
					if b && !t {
						s.Quality.Losses++
					}
					if highRisk[cluster] && b != t {
						s.Quality.Verdict = Fail
					}
				}
				if s.Quality.N > 0 {
					point := float64(s.Quality.Wins-s.Quality.Losses) / float64(s.Quality.N)
					s.Quality.Point = &point
					lower, err := QualityInterval(s.Quality.Wins, s.Quality.Losses, s.Quality.N, k)
					if err == nil {
						s.Quality.Lower = &lower
					}
					if point < 0 {
						s.Quality.Verdict = Fail
					} else if err != nil || lower < -0.02 || s.Quality.N < 30 {
						s.Quality.Verdict = combine(s.Quality.Verdict, Inconclusive)
					}
				} else {
					s.Quality.Verdict = combine(s.Quality.Verdict, Inconclusive)
				}
				if missing {
					s.Quality.Verdict = combine(s.Quality.Verdict, Inconclusive)
				}
				for _, b := range baseline {
					if b.Status != "success" {
						continue
					}
					for _, t := range treatment {
						if t.Status == "success" && t.Unit.TaskID == b.Unit.TaskID && t.Unit.Repeat == b.Unit.Repeat {
							commonB = append(commonB, b)
							commonT = append(commonT, t)
						}
					}
				}
				var err error
				s.BaselineUsage, err = Aggregate(baseline)
				if err != nil {
					missing = true
				}
				s.TreatmentUsage, err = Aggregate(treatment)
				if err != nil {
					missing = true
				}
				s.CommonBaseline, _ = Aggregate(commonB)
				s.CommonTreatment, _ = Aggregate(commonT)
				report.Strata = append(report.Strata, s)
				if required {
					report.LiveRelease = combine(report.LiveRelease, s.Quality.Verdict)
				}
				for _, r := range append(baseline, treatment...) {
					task, _ := c.Manifest.Task(r.Unit.TaskID)
					passed, known := mandatoryChecks(r, task)
					if !known {
						report.Regression = combine(report.Regression, Inconclusive)
					} else if !passed {
						report.Regression = Fail
					}
					if c.Live {
						sc, ok := scores[OpaqueID(c, r.Unit.ID)]
						if ok && sc.UnsupportedClaims != nil && *sc.UnsupportedClaims > 0 && r.Unit.ArmID == pair.Treatment {
							report.LiveRelease = Fail
						}
					}
				}
			}
			if required {
				var b, t []TaskResult
				for _, r := range c.Results {
					if r.Unit.ModelID != model.ID {
						continue
					}
					passed, _ := success(r)
					if passed {
						r.Status = "success"
					}
					if r.Unit.ArmID == pair.Baseline {
						b = append(b, r)
					} else if r.Unit.ArmID == pair.Treatment {
						t = append(t, r)
					}
				}
				bs, be := Aggregate(b)
				ts, te := Aggregate(t)
				gates := []Gate{{Name: model.ID + " uncached input", Threshold: .30, Verdict: efficiencyGate(float64(bs.UncachedInput), float64(ts.UncachedInput), .30)}, {Name: model.ID + " tool rounds", Threshold: .15, Verdict: efficiencyGate(float64(bs.ToolRounds), float64(ts.ToolRounds), .15)}, {Name: model.ID + " cost per successful task", Threshold: .25, Verdict: Inconclusive}}
				if bs.CostPerSuccess != nil && ts.CostPerSuccess != nil {
					gates[2].Verdict = efficiencyGate(*bs.CostPerSuccess, *ts.CostPerSuccess, .25)
				}
				for i := range gates {
					if be != nil || te != nil || bs.MissingUsage > 0 || ts.MissingUsage > 0 {
						gates[i].Verdict = Inconclusive
					}
					report.LiveRelease = combine(report.LiveRelease, gates[i].Verdict)
				}
				report.Efficiency = append(report.Efficiency, gates...)
			}
		}
	}
	if !hasPrimary {
		report.LiveRelease = combine(report.LiveRelease, Inconclusive)
		report.Limitations = append(report.Limitations, "Explicit product master-off/on comparison missing.")
	}
	if !c.Live {
		report.LiveRelease = combine(report.LiveRelease, Inconclusive)
		report.Limitations = append(report.Limitations, "Synthetic deterministic runs provide regression evidence, not live release qualification.")
	}
	if report.Regression == Fail {
		report.LiveRelease = Fail
	}
	for _, s := range report.Strata {
		if s.Quality.Verdict != Pass {
			report.Limitations = append(report.Limitations, fmt.Sprintf("%s/%s: quality %s (n=%d)", s.Model, s.Category, s.Quality.Verdict, s.Quality.N))
		}
	}
	return report
}
