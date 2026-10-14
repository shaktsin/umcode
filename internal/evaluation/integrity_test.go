package evaluation

import "testing"

func TestEvaluationMutationSensitivity(t *testing.T) {
	t.Run("missing-attempt-is-not-zero", func(t *testing.T) {
		s, err := Aggregate([]TaskResult{{Status: "success"}})
		if err == nil && s.CostUSD != nil {
			t.Fatal("missing attempts fabricated a free success")
		}
	})
	t.Run("negative-cost", func(t *testing.T) {
		a := goodAttempt()
		a.ObservedUSD = usd(-1)
		if _, err := Aggregate([]TaskResult{{Attempts: []Attempt{a}}}); err == nil {
			t.Fatal("negative cost accepted")
		}
	})
	t.Run("fabricated-check", func(t *testing.T) {
		r := TaskResult{Status: "completed", Checks: []CheckResult{{ID: "invented", Status: "passed"}}}
		passed, known := mandatoryChecks(r, fixtureManifest().Tasks[0])
		if passed || known {
			t.Fatal("model assertion replaced trusted check")
		}
	})
}
