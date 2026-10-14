package evaluation

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestQualityIntervalReferences(t *testing.T) {
	if got := upperExactBinomial(0, 30, .05); math.Abs(got-.0950338529) > 1e-9 {
		t.Fatal(got)
	}
	if lowerExactBinomial(0, 30, .05) != 0 || upperExactBinomial(30, 30, .05) != 1 {
		t.Fatal("endpoints")
	}
	lower, err := QualityInterval(0, 0, 30, 1)
	if err != nil || lower >= -.02 {
		t.Fatal("small ties passed", lower, err)
	}
	if _, e := QualityInterval(10, 21, 30, 1); e == nil {
		t.Fatal("invalid paired outcomes")
	}
}
func TestGatesNoPooling(t *testing.T) {
	if combine(Pass, Fail, Inconclusive) != Fail {
		t.Fatal("failure hidden")
	}
	if combine(Pass, Inconclusive) != Inconclusive {
		t.Fatal("missing hidden")
	}
	c := Campaign{Manifest: fixtureManifest(), Phase: "review-pending"}
	r := Evaluate(c, ReviewRevision{})
	if r.LiveRelease == Pass || r.ParentCoverage == Pass {
		t.Fatal("missing release passed", r)
	}
	c.Results = []TaskResult{{Status: "failed", Unit: Unit{ID: "bad", TaskID: "edit", ArmID: "on", ModelID: "synthetic", Primary: true}, Checks: []CheckResult{{Status: "failed", Safety: true}}}}
	r = Evaluate(c, ReviewRevision{})
	if r.Regression != Fail {
		t.Fatal("known safety failure hidden", r)
	}
}
func TestEfficiencyThresholds(t *testing.T) {
	if efficiencyGate(100, 70, .30) != Pass || efficiencyGate(100, 75, .25) != Pass || efficiencyGate(100, 85, .15) != Pass {
		t.Fatal("exact thresholds")
	}
	if efficiencyGate(100, 76, .25) != Fail || efficiencyGate(0, 0, .25) != Inconclusive {
		t.Fatal("invalid efficiency pass")
	}
}
func TestReportIntegrity(t *testing.T) {
	c := Campaign{Manifest: fixtureManifest(), Phase: "running"}
	r := Evaluate(c, ReviewRevision{})
	var j, m bytes.Buffer
	if err := WriteReport(&j, r, "json"); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(&m, r, "markdown"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(j.String(), `"live_release": "inconclusive"`) || !strings.Contains(m.String(), "inconclusive") {
		t.Fatal("report mismatch")
	}
	if r.ParentCoverage != Inconclusive {
		t.Fatal("unsupported comparison hidden")
	}
}

func TestQualityIncompletePairDoesNotCount(t *testing.T) {
	m := fixtureManifest()
	units, err := Schedule(m)
	if err != nil {
		t.Fatal(err)
	}
	c := Campaign{Manifest: m, Units: units, Phase: "review-pending", Results: []TaskResult{{Unit: units[0], Status: "completed", Checks: []CheckResult{{ID: "notes", Status: "passed"}}}}}
	r := Evaluate(c, ReviewRevision{})
	if len(r.Strata) != 1 || r.Strata[0].Quality.N != 0 || r.LiveRelease == Pass {
		t.Fatal(r)
	}
}
