package memory

import (
	"reflect"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

// These cases catch selecting a leaf for cross-cutting knowledge, creating a
// missing nested file, and comparing path prefixes without a directory boundary.
func TestResolvePlacement(t *testing.T) {
	for _, tc := range []struct {
		name             string
		scopes, existing []string
		want             Placement
	}{
		{"empty scopes", nil, []string{"web/UMCODE.md", "UMCODE.md"}, Placement{"UMCODE.md", false}},
		{"explicit root scope", []string{"."}, []string{"web/UMCODE.md"}, Placement{"UMCODE.md", true}},
		{"deepest existing ancestor", []string{"web/components/Button.go"}, []string{"web/UMCODE.md", "UMCODE.md", "web/components/UMCODE.md"}, Placement{"web/components/UMCODE.md", false}},
		{"directory scope", []string{"web/components"}, []string{"web/UMCODE.md", "web/components/UMCODE.md"}, Placement{"web/components/UMCODE.md", false}},
		{"common existing ancestor", []string{"web/components/Button.go", "web/routes/index.go"}, []string{"web/routes/UMCODE.md", "UMCODE.md", "web/components/UMCODE.md", "web/UMCODE.md"}, Placement{"web/UMCODE.md", false}},
		{"missing common ancestor", []string{"web/a/a.go", "web/b/b.go"}, []string{"web/a/UMCODE.md", "web/b/UMCODE.md", "UMCODE.md"}, Placement{"UMCODE.md", false}},
		{"disjoint scopes", []string{"web/a.go", "api/b.go"}, []string{"web/UMCODE.md", "api/UMCODE.md", "UMCODE.md"}, Placement{"UMCODE.md", false}},
		{"missing nested file", []string{"web/components/a.go"}, []string{"web/UMCODE.md"}, Placement{"web/UMCODE.md", false}},
		{"missing root", []string{"web/a.go"}, nil, Placement{"UMCODE.md", true}},
		{"directory boundary", []string{"website/a.go"}, []string{"web/UMCODE.md"}, Placement{"UMCODE.md", true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := PlacementInput{ProjectRoot: "/project", ScopePaths: tc.scopes, Existing: tc.existing}
			scopes, existing := append([]string(nil), tc.scopes...), append([]string(nil), tc.existing...)
			got, outcome := ResolvePlacement(in)
			if got != tc.want || outcome != (Outcome{}) {
				t.Fatalf("placement = %+v, %+v; want %+v", got, outcome, tc.want)
			}
			if !reflect.DeepEqual(in.ScopePaths, scopes) || !reflect.DeepEqual(in.Existing, existing) {
				t.Fatal("input slices were mutated")
			}
			for i, j := 0, len(scopes)-1; i < j; i, j = i+1, j-1 {
				scopes[i], scopes[j] = scopes[j], scopes[i]
			}
			for i, j := 0, len(existing)-1; i < j; i, j = i+1, j-1 {
				existing[i], existing[j] = existing[j], existing[i]
			}
			if reversed, out := ResolvePlacement(PlacementInput{ProjectRoot: "/project", ScopePaths: scopes, Existing: existing}); reversed != got || out != outcome {
				t.Fatalf("enumeration affected placement: %+v, %+v", reversed, out)
			}
		})
	}
}

func TestResolvePlacementRejectsUnsafeInputs(t *testing.T) {
	for _, tc := range []struct {
		name, root       string
		scopes, existing []string
	}{
		{"missing project", "", nil, nil},
		{"relative project", "project", nil, nil},
		{"absolute scope", "/project", []string{"/project/web"}, nil},
		{"escaping scope", "/project", []string{"../outside"}, nil},
		{"uncanonical scope", "/project", []string{"web/../api"}, nil},
		{"backslash scope", "/project", []string{`web\file.go`}, nil},
		{"windows scope", "/project", []string{"C:/project/web"}, nil},
		{"empty scope", "/project", []string{""}, nil},
		{"foreign scope", "/project", []string{"web/CLAUDE.md"}, nil},
		{"foreign inventory", "/project", nil, []string{"AGENTS.md"}},
		{"global foreign inventory", "/project", nil, []string{"AGENT.md"}},
		{"absolute inventory", "/project", nil, []string{"/project/UMCODE.md"}},
		{"escaping inventory", "/project", nil, []string{"../UMCODE.md"}},
		{"uncanonical inventory", "/project", nil, []string{"web/../UMCODE.md"}},
		{"foreign directory inventory", "/project", nil, []string{"CLAUDE.md/UMCODE.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, out := ResolvePlacement(PlacementInput{ProjectRoot: tc.root, ScopePaths: tc.scopes, Existing: tc.existing})
			if got != (Placement{}) || out.Status != protocol.MemoryOutcomeRejected || out.Reason == "" {
				t.Fatalf("unsafe placement = %+v, %+v", got, out)
			}
		})
	}
}
