package evaluation

import (
	"context"
	"fmt"
	"github.com/shaktsin/umcode/internal/optimization"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalFailureDominatesMissing(t *testing.T) {
	task := fixtureManifest().Tasks[0]
	task.Checks = append(task.Checks, Check{ID: "failed"})
	passed, known := mandatoryChecks(TaskResult{Status: "completed", Checks: []CheckResult{{ID: "notes", Status: "incomplete"}, {ID: "failed", Status: "failed"}}}, task)
	if passed || !known {
		t.Fatal("known failure hidden")
	}
	a := goodAttempt()
	a.ToolRounds = 10
	b := goodAttempt()
	b.ID = "other"
	b.Reported = false
	b.ToolRounds = 10
	gates := efficiencyGates("test", []TaskResult{{Attempts: []Attempt{a}}}, []TaskResult{{Attempts: []Attempt{b}}})
	if gates[1].Verdict != Fail {
		t.Fatal("round failure hidden by missing tokens")
	}
}
func TestFinalReviewConflictAdjudication(t *testing.T) {
	c := reviewCampaign(t)
	first := reviewFor(c)
	c.Reviews = append(c.Reviews, first)
	next := reviewFor(c)
	next.ID = "conflict"
	next.Reviewer = "second"
	v := 0
	next.Scores[0].Score = &v
	next, err := importReview(c, next)
	if err != nil {
		t.Fatal(err)
	}
	c.Reviews = append(c.Reviews, next)
	if len(resolvedScores(c, first)) != 0 {
		t.Fatal("older revision bypassed dispute")
	}
	adj := reviewFor(c)
	adj.ID = "resolved"
	adj.PriorID = next.ID
	adj.Adjudication = "independent adjudicator resolved disagreement"
	adj.Reviewer = "adjudicator"
	adj, err = importReview(c, adj)
	if err != nil {
		t.Fatal(err)
	}
	c.Reviews = append(c.Reviews, adj)
	if len(resolvedScores(c, adj)) != 1 {
		t.Fatal("adjudication did not resolve dispute")
	}
}
func TestFinalSyntheticEvidenceCannotQualify(t *testing.T) {
	m := fixtureManifest()
	template := m.Tasks[0]
	m.Tasks = nil
	for i := 0; i < 200; i++ {
		v := template
		v.ID = fmt.Sprintf("task-%d", i)
		m.Tasks = append(m.Tasks, v)
	}
	units, err := Schedule(m)
	if err != nil {
		t.Fatal(err)
	}
	c := Campaign{ID: m.ID, Manifest: m, ManifestHash: Hash(m), Live: true, Phase: "review-pending", Units: units}
	rev := ReviewRevision{ID: "review", Campaign: m.ID, RubricVersion: m.RubricVersion, Reviewer: "human"}
	for _, u := range units {
		a := goodAttempt()
		a.ID = u.ID
		a.Synthetic = true
		a.ToolRounds = 10
		a.Input = i64(100)
		a.Cached = i64(0)
		a.ObservedUSD = usd(.02)
		if u.ArmID == "on" {
			a.Input = i64(50)
			a.ToolRounds = 5
			a.ObservedUSD = usd(.01)
		}
		c.Results = append(c.Results, TaskResult{Unit: u, ArtifactHash: "same", Status: "completed", Attempts: []Attempt{a}, Checks: []CheckResult{{ID: "notes", Status: "passed"}}})
		v := 3
		rev.Scores = append(rev.Scores, ReviewScore{OpaqueID: OpaqueID(c, u.ID), ArtifactHash: "same", Score: &v, SafetyViolations: zero(), UnsupportedClaims: zero(), ContextOmissions: zero(), StaleAuthority: zero()})
	}
	c.Reviews = []ReviewRevision{rev}
	if Evaluate(c, rev).LiveRelease == Pass {
		t.Fatal("synthetic two-arm campaign qualified live")
	}
}
func TestFinalVerifierProtectsExecutedPrerequisites(t *testing.T) {
	task, artifacts := verifierTask(t)
	os.WriteFile(filepath.Join(task.TrustedRoot, "verify-second.sh"), []byte("exit 1\n"), 0600)
	task.TrustedHash, _ = TreeHash(task.TrustedRoot)
	task.Artifacts = []string{"submission.sh"}
	os.WriteFile(filepath.Join(artifacts, "submission.sh"), []byte("printf 'exit 0\\n' > verify-second.sh\n"), 0600)
	task.Checks = []Check{{ID: "attack", Argv: []string{"/bin/sh", "submission.sh"}, Seconds: 2}, {ID: "second", Argv: []string{"/bin/sh", "verify-second.sh"}, Seconds: 2}}
	checks, err := Verify(t.Context(), task, artifacts, filepath.Join(t.TempDir(), "verify"))
	if err == nil && len(checks) == 2 && checks[0].Status == "passed" && checks[1].Status == "passed" {
		t.Fatal("rewritten grader manufactured success")
	}
}
func TestFinalModelValidation(t *testing.T) {
	m := fixtureManifest()
	m.Models[0].ID = "gemini-2.5-pro"
	if err := Validate(m); err != nil {
		t.Fatal("ordinary provider ID rejected", err)
	}
	m.Models[0].Reasoning = "high"
	if Validate(m) == nil {
		t.Fatal("unsupported complexity accepted")
	}
	m = fixtureManifest()
	m.Models[0].Price.Cached = m.Models[0].Price.Input + 1
	if Validate(m) == nil {
		t.Fatal("reservation assumed wrong maximum price")
	}
}
func TestFinalResumeCeilingCannotExpandAuthorization(t *testing.T) {
	m := campaignFixture(t)
	m.MaxCostUSD = 100
	m.Models[0].CredentialRef = "KEY"
	m.Models[0].CredentialKind = "api_key"
	out := filepath.Join(t.TempDir(), "run")
	runner := runnerFunc(func(context.Context, RunInput) (TaskResult, error) { return TaskResult{Status: "incomplete"}, nil })
	options := CampaignOptions{Out: out, Live: true, MaxCostUSD: 100, Credentials: map[string]string{"KEY": "test"}}
	if _, err := RunCampaign(t.Context(), m, runner, options); err != nil {
		t.Fatal(err)
	}
	options.Resume = true
	options.MaxCostUSD = 1
	if _, err := RunCampaign(t.Context(), m, runner, options); err == nil {
		t.Fatal("resume silently retained old spending authorization")
	}
}
func TestFinalPartialUsageKeepsReservation(t *testing.T) {
	a := goodAttempt()
	a.Partial = true
	if cost, reason, err := AttemptCost(a, Price{ID: "p", Input: 1, Output: 2}); err != nil || cost != nil || reason == "" {
		t.Fatal(cost, reason, err)
	}
	s, err := Aggregate([]TaskResult{{Status: "success", Attempts: []Attempt{a}}})
	if err != nil || s.MissingUsage != 1 || s.CostUSD != nil {
		t.Fatal(s, err)
	}
}
func TestFinalKnownReviewSafetySurvivesMissingChecks(t *testing.T) {
	c := reviewCampaign(t)
	c.Live = true
	c.Results[0].Checks = nil
	rev := reviewFor(c)
	v := 1
	rev.Scores[0].SafetyViolations = &v
	c.Reviews = []ReviewRevision{rev}
	if Evaluate(c, rev).LiveRelease != Fail {
		t.Fatal("human safety failure hidden by missing verifier")
	}
}

func TestFinalCompleteEvidenceCanQualify(t *testing.T) {
	m := fixtureManifest()
	template := m.Tasks[0]
	m.Tasks = nil
	for i := 0; i < 200; i++ {
		task := template
		task.ID = fmt.Sprintf("task-%d", i)
		m.Tasks = append(m.Tasks, task)
	}
	full := optimization.All(true)
	policies := []optimization.Policy{{ContextCompiler: true}, {ContextCompiler: true, ContextRetrieval: true}, {ToolResultReducers: true}, {ProgressiveTools: true}, {DesignedWorkflow: true, AutomaticWorkflow: true}, {DesignedWorkflow: true, AutomaticWorkflow: true, AutoPromote: true}}
	for i := 0; i < 6; i++ {
		p := full
		switch i {
		case 0:
			p.ContextCompiler = false
			p.ContextRetrieval = false
		case 1:
			p.ContextRetrieval = false
		case 2:
			p.ToolResultReducers = false
		case 3:
			p.ProgressiveTools = false
		case 4:
			p.DesignedWorkflow = false
			p.AutomaticWorkflow = false
			p.AutoPromote = false
		case 5:
			p.AutoPromote = false
		}
		policies = append(policies, p)
	}
	for i, p := range policies {
		copy := p
		id := fmt.Sprintf("component-%d", i)
		m.Arms = append(m.Arms, Arm{ID: id, Mode: "override", Policy: &copy})
		base, treat := "off", id
		if i == 1 {
			base = "component-0"
		}
		if i == 5 {
			base = "component-4"
		}
		if i >= 6 {
			base, treat = id, "on"
		}
		m.Comparisons = append(m.Comparisons, Comparison{Baseline: base, Treatment: treat})
	}
	units, err := Schedule(m)
	if err != nil {
		t.Fatal(err)
	}
	c := Campaign{ID: m.ID, Manifest: m, ManifestHash: Hash(m), Live: true, Phase: "scored", Units: units}
	rev := ReviewRevision{ID: "human", Campaign: m.ID, RubricVersion: m.RubricVersion, Reviewer: "reviewer"}
	for _, unit := range units {
		a := goodAttempt()
		a.ID = unit.ID
		a.Synthetic = false
		a.Provider = m.Models[0].Provider
		a.Model = m.Models[0].ID
		a.PriceID = m.Models[0].Price.ID
		a.Input = i64(100)
		a.Cached = i64(0)
		a.ToolRounds = 10
		if unit.ArmID == "on" {
			a.Input = i64(50)
			a.ToolRounds = 5
		}
		a.ObservedUSD, _, err = AttemptCost(a, m.Models[0].Price)
		if err != nil {
			t.Fatal(err)
		}
		c.Results = append(c.Results, TaskResult{Unit: unit, ConfigurationHash: c.ManifestHash, ArtifactHash: "bound", Status: "completed", Attempts: []Attempt{a}, Checks: []CheckResult{{ID: "notes", Status: "passed"}}})
		score := 3
		rev.Scores = append(rev.Scores, ReviewScore{OpaqueID: OpaqueID(c, unit.ID), ArtifactHash: "bound", Score: &score, SafetyViolations: zero(), UnsupportedClaims: zero(), ContextOmissions: zero(), StaleAuthority: zero()})
	}
	c.Reviews = []ReviewRevision{rev}
	if reasons := qualificationEvidence(c); len(reasons) > 0 {
		t.Fatal(reasons)
	}
	if r := Evaluate(c, rev); r.LiveRelease != Pass {
		t.Fatal(r.LiveRelease, r.Limitations)
	}
}
