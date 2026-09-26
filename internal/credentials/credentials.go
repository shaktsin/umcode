// Package credentials manages API keys: records in SQLite, secrets in the
// Keychain, key testing, default/fallback selection and monthly budgets.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
)

// ErrNoCredential means no usable key exists for a provider.
var ErrNoCredential = errors.New("no API key configured for this provider")

// ErrBudgetExceeded is returned when a hard-stop budget is used up.
var ErrBudgetExceeded = errors.New("monthly budget reached for this API key")

// Service manages credentials.
type Service struct {
	st      *store.Store
	secrets secrets.Store
	llms    *llm.Registry
	now     func() time.Time
}

// New returns a Service.
func New(st *store.Store, sec secrets.Store, llms *llm.Registry) *Service {
	return &Service{st: st, secrets: sec, llms: llms, now: time.Now}
}

func secretKey(id string) string { return "credential/" + id }

func last4(secret string) string {
	s := strings.TrimSpace(secret)
	if len(s) <= 4 {
		return strings.Repeat("•", len(s))
	}
	return s[len(s)-4:]
}

// Add stores a new key. The secret goes to the secrets store; only its last 4
// characters are kept in SQLite.
func (s *Service) Add(ctx context.Context, p protocol.CredentialAddParams) (protocol.Credential, error) {
	p.Provider = config.NormalizeProvider(p.Provider)
	if _, ok := s.llms.Get(p.Provider); !ok {
		return protocol.Credential{}, fmt.Errorf("unknown provider %q", p.Provider)
	}
	p.Secret = strings.TrimSpace(p.Secret)
	if p.Secret == "" && p.Provider != "openai_compatible" {
		return protocol.Credential{}, errors.New("API key is empty")
	}
	if p.Provider == "openai_compatible" && p.BaseURL == "" {
		return protocol.Credential{}, errors.New("a base URL is required for OpenAI-compatible servers")
	}
	if strings.TrimSpace(p.Label) == "" {
		p.Label = p.Provider
	}
	c := protocol.Credential{ID: store.NewID("key"), Provider: p.Provider, Label: p.Label, BaseURL: p.BaseURL,
		Last4: last4(p.Secret), IsDefault: p.IsDefault}
	if err := s.secrets.Set(secretKey(c.ID), p.Secret); err != nil {
		return c, fmt.Errorf("save secret: %w", err)
	}
	c, err := s.st.CreateCredential(ctx, c)
	if err != nil {
		_ = s.secrets.Delete(secretKey(c.ID))
		return c, err
	}
	_ = s.st.Audit(ctx, "credential.add", map[string]any{"id": c.ID, "provider": c.Provider, "label": c.Label})
	return c, nil
}

// List returns all credentials with this month's usage.
func (s *Service) List(ctx context.Context) ([]protocol.Credential, error) {
	creds, err := s.st.ListCredentials(ctx, "")
	if err != nil {
		return nil, err
	}
	from := monthStart(s.now())
	for i := range creds {
		u, err := s.st.UsageTotalsWhere(ctx, "credential_id = ? AND created_at >= ?", creds[i].ID, store.FormatTime(from))
		if err != nil {
			return nil, err
		}
		creds[i].MonthUsage = &u
	}
	return creds, nil
}

// Update applies a partial update.
func (s *Service) Update(ctx context.Context, p protocol.CredentialUpdateParams) (protocol.Credential, error) {
	if err := s.st.UpdateCredential(ctx, p); err != nil {
		return protocol.Credential{}, err
	}
	_ = s.st.Audit(ctx, "credential.update", p)
	return s.st.GetCredential(ctx, p.CredentialID)
}

// Rotate replaces a key's secret, keeping its id and usage history.
func (s *Service) Rotate(ctx context.Context, id, secret string) (protocol.Credential, error) {
	if _, err := s.st.GetCredential(ctx, id); err != nil {
		return protocol.Credential{}, err
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return protocol.Credential{}, errors.New("API key is empty")
	}
	if err := s.secrets.Set(secretKey(id), secret); err != nil {
		return protocol.Credential{}, err
	}
	if err := s.st.SetCredentialLast4(ctx, id, last4(secret)); err != nil {
		return protocol.Credential{}, err
	}
	_ = s.st.Audit(ctx, "credential.rotate", map[string]any{"id": id})
	return s.st.GetCredential(ctx, id)
}

// Delete removes a key and its secret. Usage history is kept.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.st.DeleteCredential(ctx, id); err != nil {
		return err
	}
	_ = s.secrets.Delete(secretKey(id))
	_ = s.st.Audit(ctx, "credential.delete", map[string]any{"id": id})
	return nil
}

// SetBudget sets a monthly budget (0 disables it).
func (s *Service) SetBudget(ctx context.Context, p protocol.UsageSetBudgetParams) error {
	if p.MonthlyBudgetUSD < 0 {
		return errors.New("budget must be >= 0")
	}
	if err := s.st.SetCredentialBudget(ctx, p.CredentialID, p.MonthlyBudgetUSD, p.HardStop); err != nil {
		return err
	}
	_ = s.st.Audit(ctx, "credential.budget", p)
	return nil
}

// Test calls the provider's list-models endpoint with the key and caches the result.
func (s *Service) Test(ctx context.Context, id string) (protocol.CredentialTestResult, error) {
	c, err := s.st.GetCredential(ctx, id)
	if err != nil {
		return protocol.CredentialTestResult{}, err
	}
	cred, err := s.material(c)
	if err != nil {
		return protocol.CredentialTestResult{}, err
	}
	p, _ := s.llms.Get(c.Provider)
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := s.now()
	ids, err := p.ListModels(tctx, cred)
	res := protocol.CredentialTestResult{LatencyMs: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
	} else {
		res.OK, res.Models = true, ids
		_ = s.st.ReplaceModelCache(ctx, c.Provider, ids)
	}
	_ = s.st.RecordCredentialTest(ctx, id, res.OK)
	return res, nil
}

func (s *Service) material(c protocol.Credential) (llm.Credential, error) {
	secret, err := s.secrets.Get(secretKey(c.ID))
	if err != nil && !(errors.Is(err, secrets.ErrNotFound) && c.Provider == "openai_compatible") {
		return llm.Credential{}, fmt.Errorf("read secret for %s: %w", c.Label, err)
	}
	return llm.Credential{APIKey: secret, BaseURL: c.BaseURL}, nil
}

// Resolved is a credential ready to use.
type Resolved struct {
	Record   protocol.Credential
	Material llm.Credential
}

// Resolve picks the key for a provider: the requested id, else the provider's
// default, else its oldest enabled key.
func (s *Service) Resolve(ctx context.Context, provider, id string) (Resolved, error) {
	if id != "" {
		c, err := s.st.GetCredential(ctx, id)
		if err != nil {
			return Resolved{}, fmt.Errorf("API key %s: %w", id, err)
		}
		if c.Provider != provider {
			return Resolved{}, fmt.Errorf("API key %q is for %s, not %s", c.Label, c.Provider, provider)
		}
		if !c.Enabled {
			return Resolved{}, fmt.Errorf("API key %q is disabled", c.Label)
		}
		m, err := s.material(c)
		return Resolved{c, m}, err
	}
	creds, err := s.st.ListCredentials(ctx, provider)
	if err != nil {
		return Resolved{}, err
	}
	for _, c := range creds { // ordered default first
		if c.Enabled {
			m, err := s.material(c)
			return Resolved{c, m}, err
		}
	}
	return Resolved{}, fmt.Errorf("%w (%s)", ErrNoCredential, provider)
}

// Fallbacks returns other enabled keys for the provider marked for fallback.
func (s *Service) Fallbacks(ctx context.Context, provider, excludeID string) ([]Resolved, error) {
	creds, err := s.st.ListCredentials(ctx, provider)
	if err != nil {
		return nil, err
	}
	var out []Resolved
	for _, c := range creds {
		if c.ID == excludeID || !c.Enabled || !c.Fallback {
			continue
		}
		m, err := s.material(c)
		if err != nil {
			continue
		}
		out = append(out, Resolved{c, m})
	}
	return out, nil
}

// BudgetState describes spend against a key's monthly budget.
type BudgetState struct {
	SpentUSD  float64
	BudgetUSD float64
	Percent   float64
	Blocked   bool
}

// Budget returns the current month's spend for a key.
func (s *Service) Budget(ctx context.Context, c protocol.Credential) (BudgetState, error) {
	if c.MonthlyBudgetUSD <= 0 {
		return BudgetState{}, nil
	}
	u, err := s.st.UsageTotalsWhere(ctx, "credential_id = ? AND created_at >= ?", c.ID, store.FormatTime(monthStart(s.now())))
	if err != nil {
		return BudgetState{}, err
	}
	st := BudgetState{SpentUSD: u.CostUSD, BudgetUSD: c.MonthlyBudgetUSD, Percent: 100 * u.CostUSD / c.MonthlyBudgetUSD}
	st.Blocked = c.HardStop && u.CostUSD >= c.MonthlyBudgetUSD
	return st, nil
}

// ImportConfigKeys moves API keys found in config.yaml (or the UMCODE_
// environment) into secure storage, once per provider, when that provider
// has no key yet.
func (s *Service) ImportConfigKeys(ctx context.Context, cfg *config.Config) ([]protocol.Credential, error) {
	type pending struct{ key, baseURL string }
	found := map[string]pending{}
	if cfg.LLM.APIKey != "" && cfg.LLM.Provider != "" {
		found[cfg.LLM.Provider] = pending{key: cfg.LLM.APIKey}
	}
	for name, p := range cfg.LLM.Providers {
		if _, ok := found[name]; ok {
			continue
		}
		if p.APIKey != "" || (name == "openai_compatible" && p.BaseURL != "") {
			found[name] = pending{p.APIKey, p.BaseURL}
		}
	}
	var added []protocol.Credential
	for prov, t := range found {
		existing, err := s.st.ListCredentials(ctx, prov)
		if err != nil {
			return added, err
		}
		if len(existing) > 0 {
			continue
		}
		c, err := s.Add(ctx, protocol.CredentialAddParams{Provider: prov, Label: "Imported", Secret: t.key, BaseURL: t.baseURL})
		if err != nil {
			return added, fmt.Errorf("import %s key: %w", prov, err)
		}
		added = append(added, c)
	}
	return added, nil
}

func monthStart(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

// Usable returns a provider's enabled keys with their secrets, the default
// first — the set the router may choose between.
func (s *Service) Usable(ctx context.Context, provider string) ([]Resolved, error) {
	creds, err := s.st.ListCredentials(ctx, provider)
	if err != nil {
		return nil, err
	}
	var out []Resolved
	for _, c := range creds { // ordered default first
		if !c.Enabled {
			continue
		}
		m, err := s.material(c)
		if err != nil {
			continue // a key whose secret has gone missing is simply not offered
		}
		out = append(out, Resolved{c, m})
	}
	return out, nil
}
