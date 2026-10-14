package evaluation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type runnerFunc func(context.Context, RunInput) (TaskResult, error)

func (f runnerFunc) Run(c context.Context, i RunInput) (TaskResult, error) { return f(c, i) }
func campaignFixture(t *testing.T) Manifest {
	t.Helper()
	m := fixtureManifest()
	m.BaseDir = t.TempDir()
	src := filepath.Join(m.BaseDir, "fixture")
	os.Mkdir(src, 0700)
	os.WriteFile(filepath.Join(src, "seed"), []byte("seed"), 0600)
	m.Tasks[0].FixtureHash, _ = TreeHash(src)
	return m
}
func TestCampaignBudget(t *testing.T) {
	b := Budget{Ceiling: 5}
	if e := b.Reserve("one", 4); e != nil {
		t.Fatal(e)
	}
	if e := b.Reserve("two", 2); e == nil {
		t.Fatal("overspent")
	}
	if e := b.Settle("one", nil); e != nil {
		t.Fatal(e)
	}
	if b.Spent() != 4 {
		t.Fatal("missing usage refunded")
	}
	if e := b.Settle("one", usd(0)); e == nil {
		t.Fatal("settled twice")
	}
	b = Budget{Ceiling: 5}
	b.Reserve("one", 4)
	b.Settle("one", usd(2))
	if b.Spent() != 2 {
		t.Fatal("settlement")
	}
	m := campaignFixture(t)
	m.MaxCostUSD = .001
	m.Models[0].CredentialRef = "TEST_KEY"
	m.Models[0].CredentialKind = "api_key"
	calls := 0
	_, err := RunCampaign(t.Context(), m, runnerFunc(func(context.Context, RunInput) (TaskResult, error) { calls++; return TaskResult{}, nil }), CampaignOptions{Out: filepath.Join(t.TempDir(), "run"), Live: true, MaxCostUSD: .001})
	if err == nil || calls != 0 {
		t.Fatal("insufficient preflight dispatched", err, calls)
	}
}
func TestCampaignCrashResume(t *testing.T) {
	m := campaignFixture(t)
	out := filepath.Join(t.TempDir(), "run")
	calls := 0
	runner := runnerFunc(func(ctx context.Context, in RunInput) (TaskResult, error) {
		calls++
		if err := in.BeforeAttempt(ctx, "dispatch-"+in.Unit.ID, 0); err != nil {
			return TaskResult{}, err
		}
		a := goodAttempt()
		a.ID = "dispatch-" + in.Unit.ID
		if err := in.AttemptSink(a); err != nil {
			return TaskResult{}, err
		}
		return TaskResult{Unit: in.Unit, Status: "completed", Attempts: []Attempt{a}}, nil
	})
	c, err := RunCampaign(t.Context(), m, runner, CampaignOptions{Out: out})
	if err != nil {
		t.Fatal(err)
	}
	n := calls
	c, err = RunCampaign(t.Context(), m, runner, CampaignOptions{Out: out, Resume: true})
	if err != nil || calls != n {
		t.Fatal("reran", err, calls, n)
	}
	c.Results[0].Status = "running"
	if err := saveCampaign(out, c); err != nil {
		t.Fatal(err)
	}
	c, err = RunCampaign(t.Context(), m, runner, CampaignOptions{Out: out, Resume: true})
	if err != nil || calls != n || c.Results[0].Status != "incomplete" {
		t.Fatal("uncertain dispatch reran", err, calls)
	}
	m.Seed = "changed"
	if _, err := RunCampaign(t.Context(), m, runner, CampaignOptions{Out: out, Resume: true}); err == nil {
		t.Fatal("changed resume accepted")
	}
}
func TestCampaignCancellationOwnership(t *testing.T) {
	m := campaignFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	out := filepath.Join(t.TempDir(), "run")
	calls := 0
	_, err := RunCampaign(ctx, m, runnerFunc(func(ctx context.Context, in RunInput) (TaskResult, error) {
		calls++
		cancel()
		return TaskResult{Unit: in.Unit, Status: "incomplete"}, ctx.Err()
	}), CampaignOptions{Out: out})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err, calls)
	}
	if _, err := LoadCampaign(out); err != nil {
		t.Fatal("state lost", err)
	}
	if err := CleanupOwned(out, "wrong"); err == nil {
		t.Fatal("deleted wrong ownership")
	}
}
