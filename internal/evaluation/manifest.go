// Package evaluation measures isolated tasks without changing product settings.
package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"strings"
)

type Manifest struct {
	BaseDir        string            `json:"-"`
	Version        int               `json:"version"`
	ID             string            `json:"id"`
	SourceRevision string            `json:"source_revision"`
	FixtureHash    string            `json:"fixture_hash"`
	RubricVersion  string            `json:"rubric_version"`
	ScoringVersion string            `json:"scoring_version"`
	Population     string            `json:"population"`
	Sampling       string            `json:"sampling"`
	Seed           string            `json:"seed"`
	Repeats        int               `json:"repeats"`
	MaxCostUSD     float64           `json:"max_cost_usd"`
	Models         []Model           `json:"models"`
	Arms           []Arm             `json:"arms"`
	Tasks          []Task            `json:"tasks"`
	Comparisons    []Comparison      `json:"comparisons"`
	Unsupported    map[string]string `json:"unsupported,omitempty"`
}
type Model struct {
	ID             string   `json:"id"`
	Provider       string   `json:"provider"`
	Reasoning      string   `json:"reasoning"`
	CredentialRef  string   `json:"credential_ref,omitempty"`
	CredentialKind string   `json:"credential_kind,omitempty"`
	Window         int64    `json:"window"`
	MaxOutput      int64    `json:"max_output"`
	Price          Price    `json:"price"`
	Routes         []string `json:"routes,omitempty"`
}

// Prices are frozen USD per million tokens; cached and reasoning are subsets.
type Price struct {
	ID     string  `json:"id"`
	Input  float64 `json:"input"`
	Cached float64 `json:"cached"`
	Output float64 `json:"output"`
}
type Comparison struct {
	Baseline  string `json:"baseline"`
	Treatment string `json:"treatment"`
}
type Limits struct {
	Seconds    int   `json:"seconds"`
	Tokens     int64 `json:"tokens"`
	ToolRounds int   `json:"tool_rounds"`
}
type Task struct {
	ID           string       `json:"id"`
	Category     string       `json:"category"`
	Cluster      string       `json:"cluster,omitempty"`
	Fixture      string       `json:"fixture"`
	FixtureHash  string       `json:"fixture_hash"`
	Prompts      []string     `json:"prompts"`
	Artifacts    []string     `json:"artifacts"`
	Checks       []Check      `json:"checks"`
	Limits       Limits       `json:"limits"`
	HighRisk     bool         `json:"high_risk"`
	Projectless  bool         `json:"projectless"`
	ApprovalMode string       `json:"approval_mode"`
	AllowActions bool         `json:"allow_actions"`
	Script       []ScriptStep `json:"script,omitempty"`
	TrustedRoot  string       `json:"trusted_root,omitempty"`
	TrustedHash  string       `json:"trusted_hash,omitempty"`
}
type Check struct {
	ID      string   `json:"id"`
	Argv    []string `json:"argv"`
	Seconds int      `json:"seconds"`
	Safety  bool     `json:"safety"`
}
type ScriptStep struct {
	When    string          `json:"when,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Text    string          `json:"text,omitempty"`
	Restart bool            `json:"restart,omitempty"`
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func SafePath(p string) bool {
	return p != "" && p != "." && !filepath.IsAbs(p) && !strings.Contains(p, "\\") && filepath.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

// DecodeStrict also rejects duplicate object keys, including nested objects.
func DecodeStrict(r io.Reader, out any, limit int64) error {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > limit {
		return errors.New("JSON size limit exceeded")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if err := uniqueValue(d, 0); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit exceeded")
	}
	v, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := v.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			v, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := v.(string)
			if !ok || seen[key] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[key] = true
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}
func DecodeManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	if err := DecodeStrict(r, &m, 1<<20); err != nil {
		return m, err
	}
	return m, Validate(m)
}
func Validate(m Manifest) error {
	fail := func(s string) error { return fmt.Errorf("invalid evaluation manifest: %s", s) }
	if m.Version != 1 || !safeID.MatchString(m.ID) || m.SourceRevision == "" || m.FixtureHash == "" || m.RubricVersion == "" || m.ScoringVersion == "" || m.Population == "" || m.Sampling == "" || m.Seed == "" {
		return fail("identity/version/sampling")
	}
	if m.Repeats < 1 || m.Repeats > 20 || len(m.Tasks) < 1 || len(m.Tasks) > 10000 || len(m.Arms) < 2 || len(m.Arms) > 32 || len(m.Models) < 1 || len(m.Models) > 32 || !finite(m.MaxCostUSD) {
		return fail("bounds")
	}
	models := map[string]bool{}
	for _, model := range m.Models {
		if !validModelID(model.ID) || models[model.ID] || !supportedProvider(model.Provider) || !validReasoning(model.Reasoning) || model.Window <= 0 || model.Window > 10000000 || model.MaxOutput <= 0 || model.MaxOutput > model.Window || !finite(model.Price.Input) || !finite(model.Price.Cached) || !finite(model.Price.Output) || model.Price.Cached > model.Price.Input || len(model.Routes) > 0 {
			return fail("model/prices")
		}
		models[model.ID] = true
	}
	arms := map[string]bool{}
	for _, a := range m.Arms {
		if !safeID.MatchString(a.ID) || arms[a.ID] {
			return fail("arm identity")
		}
		if _, e := EffectivePolicy(a); e != nil {
			return e
		}
		arms[a.ID] = true
	}
	if len(m.Comparisons) < 1 || len(m.Comparisons) > 1024 {
		return fail("comparisons")
	}
	seenPairs := map[Comparison]bool{}
	for _, c := range m.Comparisons {
		if !arms[c.Baseline] || !arms[c.Treatment] || c.Baseline == c.Treatment || seenPairs[c] {
			return fail("comparison")
		}
		seenPairs[c] = true
	}
	tasks := map[string]bool{}
	for _, t := range m.Tasks {
		if !safeID.MatchString(t.ID) || tasks[t.ID] || !safeID.MatchString(t.Category) || t.Cluster != "" && !safeID.MatchString(t.Cluster) || !SafePath(t.Fixture) || t.FixtureHash == "" || len(t.Prompts) == 0 || len(t.Prompts) > 128 || t.Limits.Seconds <= 0 || t.Limits.Seconds > 86400 || t.Limits.Tokens <= 0 || t.Limits.ToolRounds <= 0 || t.Limits.ToolRounds > 10000 {
			return fail("task/bounds")
		}
		tasks[t.ID] = true
		if t.ApprovalMode != "" && t.ApprovalMode != "normal" && t.ApprovalMode != "auto_read_only" && t.ApprovalMode != "auto_all" {
			return fail("approval mode")
		}
		for _, p := range t.Prompts {
			if len(p) == 0 || len(p) > 64<<10 {
				return fail("prompt bound")
			}
		}
		if len(t.Artifacts) > 1024 || len(t.Checks) == 0 || len(t.Checks) > 128 || len(t.Script) > 10000 {
			return fail("artifact/check/script bounds")
		}
		for _, p := range t.Artifacts {
			if !SafePath(p) {
				return fail("artifact path")
			}
		}
		if t.TrustedRoot != "" && (!SafePath(t.TrustedRoot) || t.TrustedHash == "") {
			return fail("trusted verifier")
		}
		checks := map[string]bool{}
		for _, c := range t.Checks {
			if !safeID.MatchString(c.ID) || checks[c.ID] || len(c.Argv) == 0 || len(c.Argv) > 128 || c.Argv[0] == "" || c.Seconds <= 0 || c.Seconds > 3600 {
				return fail("check")
			}
			checks[c.ID] = true
		}
	}
	for k, v := range m.Unsupported {
		if k == "" || v == "" {
			return fail("unsupported comparison reason")
		}
	}
	return nil
}

func validModelID(id string) bool {
	return len(id) > 0 && len(id) <= 512 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\n\r\t")
}
func supportedProvider(id string) bool {
	return id == "claude" || id == "openai" || id == "openai_compatible" || id == "gemini"
}

// validReasoning accepts the effort levels and the legacy names older
// manifests carry.
func validReasoning(s string) bool {
	switch s {
	case "low", "medium", "high", "quick", "standard", "deep":
		return true
	}
	return false
}
