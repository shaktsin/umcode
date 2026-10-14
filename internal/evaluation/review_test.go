package evaluation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewCampaign(t *testing.T) Campaign {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("verified"), 0600)
	h, _ := TreeHash(root)
	m := fixtureManifest()
	u, _ := Schedule(m)
	return Campaign{Version: 1, ID: m.ID, Manifest: m, ManifestHash: Hash(m), Phase: "review-pending", Results: []TaskResult{{Unit: u[0], ArtifactDir: root, ArtifactHash: h, Response: "verified", Status: "completed", Checks: []CheckResult{{ID: "notes", Status: "passed"}}}}}
}
func reviewFor(c Campaign) ReviewRevision {
	score := 3
	return ReviewRevision{ID: "review-1", Campaign: c.ID, RubricVersion: c.Manifest.RubricVersion, Reviewer: "reviewer", Scores: []ReviewScore{{OpaqueID: OpaqueID(c, c.Results[0].Unit.ID), ArtifactHash: c.Results[0].ArtifactHash, Score: &score, SafetyViolations: zero(), UnsupportedClaims: zero(), ContextOmissions: zero(), StaleAuthority: zero()}}}
}
func importReview(c Campaign, r ReviewRevision) (ReviewRevision, error) {
	b, _ := json.Marshal(r)
	return ImportReview(c, bytes.NewReader(b))
}
func TestReviewBlindingAndRedaction(t *testing.T) {
	c := reviewCampaign(t)
	c.Results[0].Response = "api_key=secret-value-never-export"
	out := filepath.Join(t.TempDir(), "reviews")
	if err := ExportReview(c, out); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(out, e.Name()))
		for _, bad := range []string{"secret-value-never-export", `"arm_id"`, `"cost_usd"`, `"policies"`} {
			if strings.Contains(string(b), bad) {
				t.Fatal("blind export leaked", bad)
			}
		}
	}
}
func TestReviewImportIntegrity(t *testing.T) {
	c := reviewCampaign(t)
	r := reviewFor(c)
	if _, e := importReview(c, r); e != nil {
		t.Fatal(e)
	}
	for _, mut := range []func(*ReviewRevision){func(r *ReviewRevision) { r.Scores[0].ArtifactHash = "stale" }, func(r *ReviewRevision) { r.Scores[0].OpaqueID = "unknown" }, func(r *ReviewRevision) { r.RubricVersion = "v2" }, func(r *ReviewRevision) { r.Scores[0].Score = nil }, func(r *ReviewRevision) { v := 5; r.Scores[0].Score = &v }, func(r *ReviewRevision) { r.Scores[0].SafetyViolations = nil }, func(r *ReviewRevision) { r.Scores = append(r.Scores, r.Scores[0]) }} {
		r := reviewFor(c)
		mut(&r)
		if _, e := importReview(c, r); e == nil {
			t.Fatal("invalid review accepted")
		}
	}
	c.Reviews = []ReviewRevision{reviewFor(c)}
	if _, e := importReview(c, reviewFor(c)); e == nil {
		t.Fatal("duplicate revision accepted")
	}
	r = reviewFor(c)
	r.ID = "review-2"
	r.PriorID = "review-1"
	if _, e := importReview(c, r); e != nil {
		t.Fatal("amendment", e)
	}
	os.WriteFile(filepath.Join(c.Results[0].ArtifactDir, "notes.txt"), []byte("replaced"), 0600)
	r.ID = "review-3"
	if _, e := importReview(c, r); e == nil {
		t.Fatal("changed artifact review accepted")
	}
}
