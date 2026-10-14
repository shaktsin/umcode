package engine

import (
	"context"
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluationAdmissionPolicy(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	p := optimization.All(true)
	ctx := optimization.WithPolicy(t.Context(), p)
	actual, err := e.resolveOptimizationPolicy(ctx)
	if err != nil || actual.AutomaticWorkflow {
		t.Fatal("caller context overrode admission")
	}
	e.evaluationPolicy = &p
	actual, err = e.resolveOptimizationPolicy(context.Background())
	if err != nil || actual != p {
		t.Fatal("override lost", err)
	}
	saved, err := e.TokenOptimization(t.Context())
	if err != nil || saved.Enabled {
		t.Fatal("ablation changed setting")
	}
}
func TestEvaluationIsolation(t *testing.T) {
	m := evaluation.Manifest{Version: 1, ID: "suite", SourceRevision: "rev", FixtureHash: "hash", RubricVersion: "v1", ScoringVersion: "v1", Population: "fixtures", Sampling: "independent", Seed: "seed", Repeats: 1, Models: []evaluation.Model{{ID: "eval-model", Provider: "claude", Reasoning: "standard", Window: 100000, MaxOutput: 1000}}, Arms: []evaluation.Arm{{ID: "off", Mode: "product-off"}, {ID: "on", Mode: "product-on"}}, Comparisons: []evaluation.Comparison{{Baseline: "off", Treatment: "on"}}, Tasks: []evaluation.Task{{ID: "hello", Category: "coding", Fixture: "source", FixtureHash: "hash", Prompts: []string{"say hello", "say hello again"}, Artifacts: []string{"notes.txt"}, Limits: evaluation.Limits{Seconds: 30, Tokens: 100000, ToolRounds: 10}, Checks: []evaluation.Check{{ID: "notes", Argv: []string{"true"}, Seconds: 2}}, Script: []evaluation.ScriptStep{{Text: "Hello"}, {Restart: true, Text: "Again"}}}}}
	base := t.TempDir()
	m.BaseDir = base
	src := filepath.Join(base, "source")
	os.Mkdir(src, 0700)
	os.WriteFile(filepath.Join(src, "notes.txt"), []byte("seed"), 0600)
	m.Tasks[0].FixtureHash, _ = evaluation.TreeHash(src)
	units, err := evaluation.Schedule(m)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewEvaluationRunner()
	paths := []string{}
	for _, u := range units {
		p, err := evaluation.Stage(t.TempDir(), m, u)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		r, err := runner.Run(t.Context(), evaluation.RunInput{Manifest: m, Unit: u, Workspace: p})
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != "completed" || len(r.Policies) != 2 {
			t.Fatalf("result %+v", r)
		}
		arm, _ := m.Arm(u.ArmID)
		expected, _ := evaluation.EffectivePolicy(arm)
		for _, got := range r.Policies {
			if got != expected {
				t.Fatal("restart policy drift")
			}
		}
	}
	os.WriteFile(filepath.Join(paths[0], "workspace", "notes.txt"), []byte("private"), 0600)
	b, _ := os.ReadFile(filepath.Join(paths[1], "workspace", "notes.txt"))
	if string(b) != "seed" {
		t.Fatal("cross-arm mutation")
	}
}
