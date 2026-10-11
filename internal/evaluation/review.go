package evaluation

import (
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/vault"
	"io"
	"os"
	"path/filepath"
)

type ReviewScore struct {
	OpaqueID          string `json:"opaque_id"`
	ArtifactHash      string `json:"artifact_hash"`
	Score             *int   `json:"score"`
	SafetyViolations  *int   `json:"safety_violations"`
	UnsupportedClaims *int   `json:"unsupported_claims"`
	ContextOmissions  *int   `json:"context_omissions"`
	StaleAuthority    *int   `json:"stale_authority"`
}
type ReviewRevision struct {
	ID            string        `json:"id"`
	Campaign      string        `json:"campaign"`
	RubricVersion string        `json:"rubric_version"`
	Reviewer      string        `json:"reviewer"`
	PriorID       string        `json:"prior_id,omitempty"`
	Adjudication  string        `json:"adjudication,omitempty"`
	Scores        []ReviewScore `json:"scores"`
	Conflicted    bool          `json:"conflicted,omitempty"`
}

func OpaqueID(c Campaign, unitID string) string {
	return Hash([]string{c.ManifestHash, unitID, "blind-v1"})[:24]
}
func ExportReview(c Campaign, destination string) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	for _, r := range c.Results {
		task, ok := c.Manifest.Task(r.Unit.TaskID)
		if !ok {
			return errors.New("unknown review task")
		}
		current, err := TreeHash(r.ArtifactDir)
		if err != nil || current != r.ArtifactHash {
			return errors.New("review artifact integrity mismatch")
		}
		artifacts := map[string]string{}
		for _, rel := range task.Artifacts {
			path, err := SafeJoin(r.ArtifactDir, rel)
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			b, err := io.ReadAll(io.LimitReader(f, 64<<10))
			f.Close()
			if err != nil {
				return err
			}
			b, _ = vault.Redact(b)
			artifacts[rel] = string(b)
		}
		response, _ := vault.Redact([]byte(r.Response))
		checks := append([]CheckResult(nil), r.Checks...)
		for i := range checks {
			body, _ := vault.Redact([]byte(checks[i].Output))
			checks[i].Output = string(body)
		}
		bundle := struct {
			OpaqueID     string            `json:"opaque_id"`
			ArtifactHash string            `json:"artifact_hash"`
			Rubric       string            `json:"rubric_version"`
			Response     string            `json:"response"`
			Checks       []CheckResult     `json:"checks"`
			Artifacts    map[string]string `json:"artifacts"`
			Limitation   string            `json:"blinding_limitation"`
		}{OpaqueID(c, r.Unit.ID), r.ArtifactHash, c.Manifest.RubricVersion, string(response), checks, artifacts, "Natural output differences may reveal treatment; arm mapping and accounting are withheld."}
		b, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destination, bundle.OpaqueID+".json"), b, 0600); err != nil {
			return err
		}
	}
	return nil
}
func ImportReview(c Campaign, input io.Reader) (ReviewRevision, error) {
	var rev ReviewRevision
	if err := DecodeStrict(input, &rev, 16<<20); err != nil {
		return rev, err
	}
	if !safeID.MatchString(rev.ID) || rev.Campaign != c.ID || rev.RubricVersion != c.Manifest.RubricVersion || rev.Reviewer == "" || len(rev.Scores) == 0 || rev.Conflicted {
		return rev, errors.New("invalid review identity")
	}
	previous := map[string]ReviewRevision{}
	for _, old := range c.Reviews {
		previous[old.ID] = old
		if old.ID == rev.ID {
			return rev, errors.New("duplicate review revision")
		}
	}
	if rev.PriorID != "" {
		if _, ok := previous[rev.PriorID]; !ok {
			return rev, errors.New("unknown prior review revision")
		}
	}
	byID := map[string]TaskResult{}
	for _, r := range c.Results {
		byID[OpaqueID(c, r.Unit.ID)] = r
	}
	seen := map[string]bool{}
	for _, score := range rev.Scores {
		r, ok := byID[score.OpaqueID]
		if !ok || seen[score.OpaqueID] || score.ArtifactHash != r.ArtifactHash || score.Score == nil || *score.Score < 0 || *score.Score > 4 {
			return rev, errors.New("invalid review score/artifact")
		}
		seen[score.OpaqueID] = true
		for _, count := range []*int{score.SafetyViolations, score.UnsupportedClaims, score.ContextOmissions, score.StaleAuthority} {
			if count == nil || *count < 0 {
				return rev, errors.New("missing/invalid review count")
			}
		}
		current, err := TreeHash(r.ArtifactDir)
		if err != nil || current != score.ArtifactHash {
			return rev, errors.New("reviewed artifacts have changed")
		}
		for _, old := range c.Reviews {
			for _, prior := range old.Scores {
				if prior.OpaqueID == score.OpaqueID && Hash(prior) != Hash(score) {
					if rev.PriorID != old.ID || rev.Reviewer != old.Reviewer {
						if rev.Adjudication == "" || rev.PriorID == "" {
							rev.Conflicted = true
						}
					}
				}
			}
		}
	}
	return rev, nil
}
