// Package router turns "how hard should this be" into "which model, on which
// key, right now". The agent loop asks for a plan and works down it; when a
// route fails it reports back, so a rate-limited key is left alone for a while
// instead of being hammered.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/credentials"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// Capabilities are what a request needs from a model.
type Capabilities struct {
	Tools     bool
	Images    bool
	Reasoning bool
	// MinContext is the estimated prompt size in tokens; a model with a smaller
	// window is skipped.
	MinContext int
}

// Route is one way to serve a request.
type Route struct {
	Provider string
	Model    string
	Cred     credentials.Resolved
	Meta     models.Meta
	Why      string
	Cost     float64
}

// Info is the protocol view of a route.
func (r Route) Info() protocol.RouteInfo {
	name := r.Meta.DisplayName
	if name == "" {
		name = r.Model
	}
	return protocol.RouteInfo{
		Provider: r.Provider, Model: r.Model, DisplayName: name,
		CredentialID: r.Cred.Record.ID, CredentialL: r.Cred.Record.Label,
		Why: r.Why, CostPerMTok: r.Cost,
	}
}

// Same reports whether two routes are the same model on the same key.
func (r Route) Same(o Route) bool {
	return r.Provider == o.Provider && r.Model == o.Model && r.Cred.Record.ID == o.Cred.Record.ID
}

// Plan is an ordered list of routes plus what was ruled out, so the UI can
// explain an empty or short list.
type Plan struct {
	Routes    []Route
	Blocked   []protocol.RouteInfo
	Level     protocol.Complexity
	Reason    string
	nextIndex int
}

// Next returns the next untried route.
func (p *Plan) Next() (Route, bool) {
	if p.nextIndex >= len(p.Routes) {
		return Route{}, false
	}
	r := p.Routes[p.nextIndex]
	p.nextIndex++
	return r, true
}

// Request describes what to route.
type Request struct {
	// Pinned is the selection after merging turn → thread → project; empty
	// fields mean "no preference".
	Pinned protocol.ModelSelection
	Level  protocol.Complexity
	Need   Capabilities
	// MaxRoutes caps how many fallbacks to line up (default 6).
	MaxRoutes int
	// Candidates, when set, is the only set of models the plan may use: the
	// registry the user has approved, or one pool from it. The catalog is not
	// consulted for more, because spending money on a model nobody chose is
	// exactly what the registry exists to prevent.
	Candidates []protocol.ConfiguredModel
	// Strategy orders the candidates; empty means the order they came in.
	Strategy string
}

// Router builds plans and remembers how routes have been behaving.
type Router struct {
	st    *store.Store
	creds *credentials.Service
	cat   *models.Catalog
	llms  *llm.Registry
	cfg   *config.Config
	log   *slog.Logger
}

func New(st *store.Store, creds *credentials.Service, cat *models.Catalog, llms *llm.Registry,
	cfg *config.Config, log *slog.Logger) *Router {
	return &Router{st: st, creds: creds, cat: cat, llms: llms, cfg: cfg, log: log}
}

// ErrNoRoute is returned when nothing can serve the request.
var ErrNoRoute = errors.New("no model is available")

const defaultMaxRoutes = 6

// Plan builds the ordered list of routes for a request.
func (r *Router) Plan(ctx context.Context, req Request) (*Plan, error) {
	max := req.MaxRoutes
	if max <= 0 {
		max = defaultMaxRoutes
	}
	level := req.Level
	if level == "" || level == protocol.ComplexityAuto {
		level = protocol.ComplexityStandard
	}
	plan := &Plan{Level: level}
	now := time.Now()

	// Which providers can pay for anything at all.
	usable, err := r.usableProviders(ctx)
	if err != nil {
		return nil, err
	}
	if len(usable) == 0 {
		plan.Reason = "no API key is set up yet; add one in Settings"
		return plan, nil
	}

	pinnedProvider := config.NormalizeProvider(req.Pinned.Provider)
	seen := map[string]bool{}

	add := func(provider, model, why string, cred credentials.Resolved) {
		key := provider + "/" + model + "/" + cred.Record.ID
		if seen[key] || len(plan.Routes) >= max {
			return
		}
		meta := r.cat.Lookup(ctx, provider, model)
		if reason := r.unusable(ctx, provider, model, cred, meta, req.Need, now); reason != "" {
			info := Route{Provider: provider, Model: model, Cred: cred, Meta: meta, Why: why}.Info()
			info.Unavailable = reason
			if h, err := r.st.GetHealth(ctx, provider, model, cred.Record.ID); err == nil && h.CoolingDown(now) {
				end := h.CooldownEnd
				info.CooldownEnd = &end
			}
			plan.Blocked = append(plan.Blocked, info)
			seen[key] = true
			return
		}
		seen[key] = true
		plan.Routes = append(plan.Routes, Route{
			Provider: provider, Model: model, Cred: cred, Meta: meta, Why: why,
			Cost: meta.In + 3*meta.Out,
		})
	}

	// A configured set short-circuits everything below: these are the models the
	// user approved, in the order the pool asks for, and nothing else.
	if len(req.Candidates) > 0 {
		for i, cm := range r.order(ctx, req.Candidates, req.Strategy, level) {
			why := protocol.RouteFallback
			switch {
			case cm.Provider == pinnedProvider && cm.Model == req.Pinned.Model:
				why = protocol.RoutePinned
			case i == 0:
				why = protocol.RouteDefault
			}
			creds := r.credsFor(ctx, usable, cm.Provider, req.Pinned.CredentialID)
			if len(creds) == 0 {
				plan.Blocked = append(plan.Blocked, protocol.RouteInfo{
					Provider: cm.Provider, Model: cm.Model, DisplayName: displayName(cm),
					Why: why, Unavailable: "no usable key for " + cm.Provider,
				})
				continue
			}
			for _, c := range creds {
				add(cm.Provider, cm.Model, why, c)
			}
		}
		if len(plan.Routes) == 0 && plan.Reason == "" {
			plan.Reason = r.explainEmpty(plan)
		}
		return plan, nil
	}

	// 1. A pinned model comes first, on the pinned key when there is one.
	if pinnedProvider != "" && req.Pinned.Model != "" {
		if creds := r.credsFor(ctx, usable, pinnedProvider, req.Pinned.CredentialID); len(creds) > 0 {
			for _, c := range creds {
				add(pinnedProvider, req.Pinned.Model, protocol.RoutePinned, c)
			}
		}
	}

	// 2. The tier, on the pinned provider first, then the others.
	order := providerOrder(usable, pinnedProvider, r.cfg.LLM.Provider)
	for _, provider := range order {
		creds := r.credsFor(ctx, usable, provider, req.Pinned.CredentialID)
		if len(creds) == 0 {
			continue
		}
		why := protocol.RouteTier
		if provider == pinnedProvider && req.Pinned.Model == "" {
			why = protocol.RouteDefault
		}
		for _, m := range r.cat.Tier(ctx, provider, level) {
			for _, c := range creds {
				add(provider, m.ID, why, c)
			}
		}
		// A provider with no catalog entries still has its configured default.
		if def := r.defaultModel(provider); def != "" {
			for _, c := range creds {
				add(provider, def, why, c)
			}
		}
	}

	// 3. Nothing in this tier: drop a level rather than failing the turn.
	if len(plan.Routes) == 0 && level == protocol.ComplexityDeep {
		lower := *plan
		req.Level = protocol.ComplexityStandard
		if p, err := r.Plan(ctx, req); err == nil && len(p.Routes) > 0 {
			for i := range p.Routes {
				p.Routes[i].Why = protocol.RouteLastGasp
			}
			p.Blocked = append(lower.Blocked, p.Blocked...)
			return p, nil
		}
	}
	if len(plan.Routes) == 0 && plan.Reason == "" {
		plan.Reason = r.explainEmpty(plan)
	}
	return plan, nil
}

// order sorts configured models for a pool strategy. "priority" is the order
// the user wrote down; the rest borrow the complexity tiers' ranking, so a
// pool and a tier agree about what "fast" or "strongest" means.
func (r *Router) order(ctx context.Context, in []protocol.ConfiguredModel, strategy string, level protocol.Complexity) []protocol.ConfiguredModel {
	out := make([]protocol.ConfiguredModel, len(in))
	copy(out, in)
	switch strategy {
	case protocol.PoolQuality:
		level = protocol.ComplexityDeep
	case protocol.PoolFast, protocol.PoolCheap:
		level = protocol.ComplexityQuick
	case protocol.PoolBalanced:
		level = protocol.ComplexityStandard
	default: // priority, or nothing set
		return out
	}
	meta := make(map[string]models.Meta, len(out))
	for _, m := range out {
		meta[m.Provider+"/"+m.Model] = r.cat.Lookup(ctx, m.Provider, m.Model)
	}
	price := func(m protocol.ConfiguredModel) float64 {
		x := meta[m.Provider+"/"+m.Model]
		return x.In + 3*x.Out
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := meta[out[i].Provider+"/"+out[i].Model], meta[out[j].Provider+"/"+out[j].Model]
		if level == protocol.ComplexityDeep {
			if a.Reasoning != b.Reasoning {
				return a.Reasoning
			}
			return price(out[i]) > price(out[j])
		}
		if a.Tools != b.Tools {
			return a.Tools
		}
		return price(out[i]) < price(out[j])
	})
	return out
}

func displayName(m protocol.ConfiguredModel) string {
	if m.Name != "" {
		return m.Name
	}
	return m.Model
}

// unusable says why a route cannot be used right now, or "" when it can.
func (r *Router) unusable(ctx context.Context, provider, model string, cred credentials.Resolved,
	meta models.Meta, need Capabilities, now time.Time) string {
	if need.Tools && !meta.Tools {
		return "cannot use tools"
	}
	if need.Images && !meta.Images {
		return "cannot read images"
	}
	if need.MinContext > 0 && meta.ContextWindow > 0 && need.MinContext > meta.ContextWindow {
		return fmt.Sprintf("context window is %d tokens, the request needs about %d", meta.ContextWindow, need.MinContext)
	}
	if st, err := r.creds.Budget(ctx, cred.Record); err == nil && st.Blocked {
		return fmt.Sprintf("monthly budget spent ($%.2f of $%.2f)", st.SpentUSD, st.BudgetUSD)
	}
	if h, err := r.st.GetHealth(ctx, provider, model, cred.Record.ID); err == nil && h.CoolingDown(now) {
		return "cooling down until " + h.CooldownEnd.Local().Format("15:04") + " after " + statusWord(h.LastStatus)
	}
	return ""
}

func statusWord(status string) string {
	switch status {
	case "rate_limited":
		return "a rate limit"
	case "unauthorized":
		return "an authentication error"
	case "server_error":
		return "a provider outage"
	case "insufficient_credit":
		return "insufficient provider credit"
	case "":
		return "an error"
	}
	return "an " + status
}

func (r *Router) explainEmpty(plan *Plan) string {
	if len(plan.Blocked) == 0 {
		return "no model in this tier can serve the request"
	}
	var parts []string
	for _, b := range plan.Blocked {
		if len(parts) == 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", b.DisplayName, b.Unavailable))
	}
	return "every candidate is unavailable: " + strings.Join(parts, ", ")
}

// usableProviders returns the enabled keys per provider, default first.
func (r *Router) usableProviders(ctx context.Context) (map[string][]credentials.Resolved, error) {
	out := map[string][]credentials.Resolved{}
	for _, id := range r.llms.IDs() {
		creds, err := r.creds.Usable(ctx, id)
		if err != nil || len(creds) == 0 {
			continue
		}
		out[id] = creds
	}
	return out, nil
}

// credsFor returns the keys to try for a provider: the pinned one alone, or all
// of them with the default first.
// credsFor lists the keys that may pay for a provider. Pinning a key means
// that key: choosing one and then being billed on another would make a budget
// or a per-key limit meaningless, so a pin narrows the list rather than just
// reordering it, and a provider the pinned key cannot pay for is skipped.
func (r *Router) credsFor(ctx context.Context, usable map[string][]credentials.Resolved, provider, pinned string) []credentials.Resolved {
	creds := usable[provider]
	if pinned == "" {
		return creds
	}
	for _, c := range creds {
		if c.Record.ID == pinned {
			return []credentials.Resolved{c}
		}
	}
	return nil
}

// providerOrder puts the pinned provider first, then the configured default,
// then the rest in a stable order.
func providerOrder(usable map[string][]credentials.Resolved, pinned, configured string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range []string{pinned, configured} {
		if p != "" && len(usable[p]) > 0 && !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	for _, p := range []string{"claude", "openai", "gemini", "openai_compatible"} {
		if len(usable[p]) > 0 && !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	for p := range usable {
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}

func (r *Router) defaultModel(provider string) string {
	if provider == r.cfg.LLM.Provider && r.cfg.LLM.Model != "" {
		return r.cfg.LLM.Model
	}
	if pc, ok := r.cfg.LLM.Providers[provider]; ok && pc.DefaultModel != "" {
		return pc.DefaultModel
	}
	return r.cat.DefaultModel(provider)
}

// Succeeded records a good request, clearing any cooldown.
func (r *Router) Succeeded(ctx context.Context, rt Route, latency time.Duration) {
	if err := r.st.RecordSuccess(ctx, rt.Provider, rt.Model, rt.Cred.Record.ID, latency.Milliseconds()); err != nil {
		r.log.Warn("record route success", "err", err)
	}
}

// Failed records a failure and sets a cooldown that matches what went wrong.
// It returns the status word used, for the turn's trail.
func (r *Router) Failed(ctx context.Context, rt Route, err error) string {
	status, cooldown := Classify(err)
	if rerr := r.st.RecordFailure(ctx, rt.Provider, rt.Model, rt.Cred.Record.ID, status, err.Error(), cooldown); rerr != nil {
		r.log.Warn("record route failure", "err", rerr)
	}
	return status
}

// Classify maps a provider error to a status word and how long to stay off the
// route. A rate limit honours Retry-After; an auth error is long, because it
// will not fix itself in a minute; a transient outage is short.
func Classify(err error) (status string, cooldown time.Duration) {
	var pe *llm.Error
	if !errors.As(err, &pe) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout", 30 * time.Second
		}
		return "error", 0
	}
	switch {
	case pe.Status == http.StatusTooManyRequests || pe.Status == 529:
		cd := pe.RetryAfter
		if cd <= 0 {
			cd = 60 * time.Second
		}
		if cd > 30*time.Minute {
			cd = 30 * time.Minute
		}
		return "rate_limited", cd
	case pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden:
		return "unauthorized", 15 * time.Minute
	case isAnthropicCreditError(pe):
		return "insufficient_credit", 15 * time.Minute
	case pe.Status == http.StatusRequestEntityTooLarge || isContextError(pe):
		return "context_too_long", 0
	case pe.Status >= 500:
		return "server_error", 2 * time.Minute
	case pe.Status == http.StatusNotFound:
		// The model is not there for this key; stop offering it for a while.
		return "unknown_model", 30 * time.Minute
	case pe.Status >= 400:
		// A malformed request will be just as malformed on another model, so
		// this is not a reason to move, and not the route's fault either.
		return "invalid_request", 0
	}
	return "error", 0
}

// Retryable says whether another route is worth trying after this error.
func Retryable(err error) bool {
	status, _ := Classify(err)
	switch status {
	case "rate_limited", "server_error", "unauthorized", "timeout", "context_too_long", "unknown_model", "insufficient_credit":
		return true
	}
	return false
}

func isAnthropicCreditError(pe *llm.Error) bool {
	if pe.Status != http.StatusBadRequest || config.NormalizeProvider(pe.Provider) != "claude" {
		return false
	}
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(pe.Body), &body); err != nil {
		return false
	}
	return body.Type == "error" && body.Error.Type == "invalid_request_error" &&
		strings.HasPrefix(strings.ToLower(body.Error.Message), "your credit balance is too low to access the anthropic api.")
}

func isContextError(pe *llm.Error) bool {
	b := strings.ToLower(pe.Body)
	return strings.Contains(b, "context length") || strings.Contains(b, "context_length") ||
		strings.Contains(b, "too many tokens") || strings.Contains(b, "maximum context")
}

// Health returns what the router knows, for model/health.
func (r *Router) Health(ctx context.Context) ([]protocol.ModelHealthRow, error) {
	rows, err := r.st.ListHealth(ctx)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	if creds, err := r.st.ListCredentials(ctx, ""); err == nil {
		for _, c := range creds {
			labels[c.ID] = c.Label
		}
	}
	out := make([]protocol.ModelHealthRow, 0, len(rows))
	for _, h := range rows {
		row := protocol.ModelHealthRow{
			Provider: h.Provider, Model: h.Model, CredentialID: h.CredentialID, Label: labels[h.CredentialID],
			LastStatus: h.LastStatus, LastError: h.LastError, OKCount: h.OKCount, ErrCount: h.ErrCount,
			LatencyMs: h.LatencyMs, UpdatedAt: h.UpdatedAt,
		}
		if !h.CooldownEnd.IsZero() {
			end := h.CooldownEnd
			row.CooldownEnd = &end
		}
		out = append(out, row)
	}
	return out, nil
}
