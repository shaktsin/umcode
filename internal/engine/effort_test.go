package engine

import "testing"

func TestRoundEffort(t *testing.T) {
	cases := []struct {
		name         string
		chosen       string
		selfAdapting bool
		round        int
		afterFailure bool
		want         string
	}{
		{"high first round", "high", false, 0, false, "high"},
		{"high round 2 after success", "high", false, 1, false, "medium"},
		{"high round 2 after failure", "high", false, 1, true, "high"},
		{"medium round 3 after success", "medium", false, 2, false, "low"},
		{"low round 3 after success", "low", false, 2, false, "off"},
		{"low first round", "low", false, 0, false, "low"},
		{"self adapting later round", "high", true, 3, false, "high"},
		{"self adapting low later round", "low", true, 3, false, "low"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := roundEffort(c.chosen, c.selfAdapting, c.round, c.afterFailure); got != c.want {
				t.Fatalf("roundEffort(%q, %v, %d, %v) = %q, want %q", c.chosen, c.selfAdapting, c.round, c.afterFailure, got, c.want)
			}
		})
	}
}
