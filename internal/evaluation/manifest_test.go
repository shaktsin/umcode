package evaluation

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/optimization"
	"math"
	"reflect"
	"strings"
	"testing"
)

func fixtureManifest() Manifest {
	return Manifest{Version: 1, ID: "suite", SourceRevision: "revision", FixtureHash: "sha", RubricVersion: "v1", ScoringVersion: "v1", Repeats: 1, Seed: "seed", Population: "fixture regression", Sampling: "independent fixtures", Models: []Model{{ID: "synthetic", Provider: "scripted", Reasoning: "standard", Window: 10000, MaxOutput: 1000, Price: Price{ID: "synthetic", Input: 1, Output: 2, Cached: 0.1}}}, Arms: []Arm{{ID: "off", Mode: "product-off"}, {ID: "on", Mode: "product-on"}}, Comparisons: []Comparison{{Baseline: "off", Treatment: "on"}}, Tasks: []Task{{ID: "edit", Category: "coding", Fixture: "fixture", FixtureHash: "hash", Prompts: []string{"write notes"}, Artifacts: []string{"notes.txt"}, Limits: Limits{Seconds: 30, Tokens: 10000, ToolRounds: 10}, Checks: []Check{{ID: "notes", Argv: []string{"go", "test", "./..."}, Seconds: 10}}}}}
}
func TestManifestBoundsAndStrictness(t *testing.T) {
	m := fixtureManifest()
	b, _ := json.Marshal(m)
	if _, err := DecodeManifest(strings.NewReader(string(b))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"version":1,"version":1}`, strings.Replace(string(b), `"version":1`, `"unknown":1,"version":1`, 1), string(b) + ` {}`, strings.Repeat(" ", 1<<20+1)} {
		if _, err := DecodeManifest(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted bad JSON %q", bad[:min(len(bad), 60)])
		}
	}
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.Tasks = append(m.Tasks, m.Tasks[0]) }, func(m *Manifest) { m.Repeats = 21 }, func(m *Manifest) { m.Repeats = 0 }, func(m *Manifest) { m.Tasks[0].Fixture = "../user" }, func(m *Manifest) { m.Tasks[0].Artifacts = []string{"/secret"} }, func(m *Manifest) { m.Tasks[0].Prompts = []string{strings.Repeat("a", 65537)} }, func(m *Manifest) { m.Models[0].Price.Input = math.Inf(1) }, func(m *Manifest) { m.Tasks[0].Limits.Seconds = 0 }, func(m *Manifest) { m.Tasks = make([]Task, 10001) }, func(m *Manifest) { m.Arms = make([]Arm, 33) }, func(m *Manifest) { m.Models[0].MaxOutput = 10001 },
	} {
		copy := fixtureManifest()
		mutate(&copy)
		if err := Validate(copy); err == nil {
			t.Fatalf("accepted invalid %+v", copy.ID)
		}
	}
	m.Tasks[0].Prompts = []string{strings.Repeat("a", 65536)}
	m.Repeats = 20
	if err := Validate(m); err != nil {
		t.Fatal("boundary rejected", err)
	}
}
func TestArmDependenciesAndProductModes(t *testing.T) {
	for _, on := range []bool{false, true} {
		mode := "product-off"
		if on {
			mode = "product-on"
		}
		p, e := EffectivePolicy(Arm{Mode: mode})
		if e != nil || p != optimization.All(on) {
			t.Fatalf("product arm %+v %v", p, e)
		}
	}
	for _, p := range []optimization.Policy{{ContextRetrieval: true}, {AutoPromote: true}, {AutomaticWorkflow: true}, {DesignedWorkflow: true}} {
		if _, err := EffectivePolicy(Arm{Mode: "override", Policy: &p}); err == nil {
			t.Fatal("invalid dependencies accepted", p)
		}
	}
	p := optimization.Policy{DesignedWorkflow: true, AutomaticWorkflow: true}
	if _, err := EffectivePolicy(Arm{Mode: "override", Policy: &p}); err != nil {
		t.Fatal(err)
	}
	if _, err := EffectivePolicy(Arm{Mode: "product-off", Policy: &p}); err == nil {
		t.Fatal("product override accepted")
	}
}
func TestPairedSchedule(t *testing.T) {
	m := fixtureManifest()
	m.Repeats = 2
	a, err := Schedule(m)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Schedule(m)
	if !reflect.DeepEqual(a, b) || len(a) != 4 {
		t.Fatalf("unstable schedule %+v", a)
	}
	for _, u := range a {
		if u.Primary != (u.Repeat == 0) {
			t.Fatal("repeat counted primary")
		}
	}
	m.Tasks = append(m.Tasks, m.Tasks[0])
	m.Tasks[1].ID = "related"
	m.Tasks[0].Cluster = "shared"
	m.Tasks[1].Cluster = "shared"
	a, err = Schedule(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range a {
		if u.Cluster != "shared" {
			t.Fatal("cluster identity lost")
		}
	}
}
