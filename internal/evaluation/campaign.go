package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type CampaignOptions struct {
	Out         string
	Live        bool
	MaxCostUSD  float64
	Resume      bool
	Credentials map[string]string
}
type Campaign struct {
	Version      int              `json:"version"`
	ID           string           `json:"id"`
	Manifest     Manifest         `json:"manifest"`
	ManifestHash string           `json:"manifest_hash"`
	Live         bool             `json:"live"`
	Phase        string           `json:"phase"`
	Units        []Unit           `json:"units"`
	Results      []TaskResult     `json:"results"`
	Budget       Budget           `json:"budget"`
	Reviews      []ReviewRevision `json:"reviews,omitempty"`
}

// ReviewRevision is extended with the blinded review contract in Task 6.
type ReviewRevision struct {
	ID string `json:"id"`
}

func saveCampaign(path string, c Campaign) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(path, ".campaign-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(path, "campaign.json"))
}
func LoadCampaign(path string) (Campaign, error) {
	var c Campaign
	if err := ordinaryRoot(path); err != nil {
		return c, err
	}
	f, err := os.Open(filepath.Join(path, "campaign.json"))
	if err != nil {
		return c, err
	}
	defer f.Close()
	err = DecodeStrict(f, &c, 256<<20)
	if err != nil {
		return c, err
	}
	if c.Version != 1 || Hash(c.Manifest) != c.ManifestHash {
		return c, errors.New("campaign integrity mismatch")
	}
	return c, nil
}
func PreflightLive(m Manifest, ceiling float64, credentials map[string]string) error {
	if ceiling <= 0 || !finite(ceiling) || m.MaxCostUSD <= 0 {
		return errors.New("finite live spending ceiling required")
	}
	for _, model := range m.Models {
		if model.CredentialRef == "" || model.CredentialKind != "api_key" || model.Price.ID == "" || model.Price.Input <= 0 || model.Price.Output <= 0 || credentials != nil && credentials[model.CredentialRef] == "" {
			return errors.New("live model credentials/pricing unavailable")
		}
		maximum := MaximumCost(model)
		if maximum > ceiling || maximum > m.MaxCostUSD {
			return fmt.Errorf("live preflight: request requires %.8f USD reservation", maximum)
		}
	}
	return nil
}
func RunCampaign(ctx context.Context, m Manifest, runner Runner, o CampaignOptions) (c Campaign, err error) {
	if err = Validate(m); err != nil {
		return
	}
	if o.Live {
		if err = PreflightLive(m, o.MaxCostUSD, o.Credentials); err != nil {
			return
		}
	}
	out, e := filepath.Abs(o.Out)
	if e != nil {
		return c, e
	}
	base, e := filepath.Abs(m.BaseDir)
	if e != nil {
		return c, e
	}
	if out == base || strings.HasPrefix(out, base+string(os.PathSeparator)) {
		return c, errors.New("campaign output must be outside fixture source tree")
	}
	if o.Resume {
		c, err = LoadCampaign(out)
		if err != nil {
			return
		}
		if c.ManifestHash != Hash(m) || c.Live != o.Live {
			return c, errors.New("resume configuration mismatch")
		}
		for i := range c.Results {
			if c.Results[i].Status == "running" {
				c.Results[i].Status = "incomplete"
				c.Results[i].IncompleteReasons = append(c.Results[i].IncompleteReasons, "interrupted dispatch; explicit new campaign required to retry")
			}
		}
	} else {
		if err = os.Mkdir(out, 0700); err != nil {
			return
		}
		ceiling := 0.
		if o.Live {
			ceiling = min(o.MaxCostUSD, m.MaxCostUSD)
		}
		units, e := Schedule(m)
		if e != nil {
			return c, e
		}
		c = Campaign{Version: 1, ID: m.ID, Manifest: m, ManifestHash: Hash(m), Live: o.Live, Phase: "staged", Units: units, Budget: Budget{Ceiling: ceiling}}
		b, _ := json.Marshal(owner{m.ID, filepath.Base(out), Hash(m)})
		if err = os.WriteFile(filepath.Join(out, ownerFile), b, 0600); err != nil {
			return
		}
	}
	c.Manifest.BaseDir = m.BaseDir
	if err = saveCampaign(out, c); err != nil {
		return
	}
	var mu sync.Mutex
	for _, unit := range c.Units {
		if err = ctx.Err(); err != nil {
			return
		}
		exists := false
		for _, r := range c.Results {
			if r.Unit.ID == unit.ID {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		workspace, e := Stage(out, m, unit)
		if e != nil {
			return c, e
		}
		idx := len(c.Results)
		c.Results = append(c.Results, TaskResult{Unit: unit, Status: "running", ConfigurationHash: c.ManifestHash, ArtifactDir: filepath.Join(workspace, "workspace")})
		c.Phase = "running"
		if err = saveCampaign(out, c); err != nil {
			return
		}
		model, _ := m.Model(unit.ModelID)
		input := RunInput{Manifest: m, Unit: unit, Workspace: workspace, Live: o.Live, Credential: o.Credentials[model.CredentialRef]}
		input.BeforeAttempt = func(ctx context.Context, id string, maximum float64) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			mu.Lock()
			defer mu.Unlock()
			if !o.Live {
				maximum = 0
			}
			if e := c.Budget.Reserve(id, maximum); e != nil {
				return e
			}
			return saveCampaign(out, c)
		}
		input.AttemptSink = func(a Attempt) error {
			mu.Lock()
			defer mu.Unlock()
			cost := a.ObservedUSD
			if !o.Live {
				zero := 0.
				cost = &zero
			}
			if e := c.Budget.Settle(a.ID, cost); e != nil {
				return e
			}
			c.Results[idx].Attempts = append(c.Results[idx].Attempts, a)
			return saveCampaign(out, c)
		}
		r, runErr := runner.Run(ctx, input)
		mu.Lock()
		durable := c.Results[idx].Attempts
		r.Unit = unit
		r.ConfigurationHash = c.ManifestHash
		r.ArtifactDir = filepath.Join(workspace, "workspace")
		r.Attempts = durable
		if runErr != nil {
			r.Status = "failed"
			r.IncompleteReasons = append(r.IncompleteReasons, "runner failed; inspect redacted task evidence")
		}
		c.Results[idx] = r
		mu.Unlock()
		if err = saveCampaign(out, c); err != nil {
			return
		}
		if err = ctx.Err(); err != nil {
			c.Results[idx].Status = "incomplete"
			_ = saveCampaign(out, c)
			return
		}
	}
	c.Phase = "review-pending"
	err = saveCampaign(out, c)
	return
}
