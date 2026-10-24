package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"net/http"
	"strings"
	"time"
)

func defaultCapacityEndpoint(provider string) string {
	switch provider {
	case "claude":
		return "https://api.anthropic.com"
	case "openai":
		return "https://api.openai.com/v1"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta"
	case "openai_compatible":
		return "http://localhost:11434/v1"
	default:
		return ""
	}
}
func (e *Engine) capacityProfile(ctx context.Context, p protocol.ModelCapacityParams) (protocol.ModelCapacityProfile, error) {
	if p.CredentialID == "" || strings.TrimSpace(p.Model) == "" {
		return protocol.ModelCapacityProfile{}, protocol.Errorf(protocol.CodeInvalidParams, "credentialId and model are required")
	}
	cred, err := e.Store.GetCredential(ctx, p.CredentialID)
	if err != nil {
		return protocol.ModelCapacityProfile{}, err
	}
	if !cred.Enabled {
		return protocol.ModelCapacityProfile{}, protocol.Errorf(protocol.CodeInvalidParams, "credential is disabled")
	}
	endpoint := cred.BaseURL
	if endpoint == "" {
		endpoint = defaultCapacityEndpoint(cred.Provider)
	}
	identity, err := models.EndpointIdentity(cred.Provider, endpoint)
	if err != nil {
		return protocol.ModelCapacityProfile{}, err
	}
	profile, err := e.Store.GetModelCapacityProfile(ctx, cred.Provider, identity, strings.TrimSpace(p.Model))
	if errors.Is(err, store.ErrNotFound) {
		return protocol.ModelCapacityProfile{Provider: cred.Provider, EndpointID: identity, Model: strings.TrimSpace(p.Model), Backend: "generic"}, nil
	}
	return profile, err
}
func (e *Engine) ModelCapacity(ctx context.Context, p protocol.ModelCapacityParams) (protocol.ModelCapacityResult, error) {
	profile, err := e.capacityProfile(ctx, p)
	if err != nil {
		return protocol.ModelCapacityResult{}, err
	}
	record, err := e.Store.GetCredential(ctx, p.CredentialID)
	if err != nil {
		return protocol.ModelCapacityResult{}, err
	}
	r := resolved{sel: protocol.ModelSelection{Provider: record.Provider, Model: profile.Model, CredentialID: record.ID}}
	r.cred.Record = record
	r.cred.Material.BaseURL = record.BaseURL
	if profile.Backend == "ollama" {
		r.cred, err = e.Creds.Resolve(ctx, record.Provider, record.ID)
		if err != nil {
			return protocol.ModelCapacityResult{}, err
		}
	}
	if e.Catalog != nil {
		r.meta = e.Catalog.Lookup(ctx, record.Provider, profile.Model)
	}
	if p.Refresh {
		e.invalidateCapacity(r)
	}
	cap, capacityErr := e.resolveCapacity(ctx, r)
	result := protocol.ModelCapacityResult{Profile: profile, Source: "unavailable"}
	if capacityErr == nil {
		n := cap.Window
		result.EffectiveContextWindow = &n
		result.Source = cap.Source
		if !cap.ObservedAt.IsZero() {
			at := cap.ObservedAt
			result.ObservedAt = &at
		}
	}
	if capacityErr != nil && !errors.Is(capacityErr, ErrCapacityUnknown) {
		return result, capacityErr
	}
	return result, nil
}
func (e *Engine) SetModelCapacity(ctx context.Context, p protocol.ModelCapacityParams) (protocol.ModelCapacityResult, error) {
	current, err := e.capacityProfile(ctx, p)
	if err != nil {
		return protocol.ModelCapacityResult{}, err
	}
	profile := current
	if p.Profile != nil {
		profile = *p.Profile
		if profile.Provider != current.Provider || profile.EndpointID != current.EndpointID || profile.Model != current.Model {
			return protocol.ModelCapacityResult{}, protocol.Errorf(protocol.CodeInvalidParams, "capacity profile does not match the selected endpoint and model")
		}
	}
	if err = profile.Validate(); err != nil {
		return protocol.ModelCapacityResult{}, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	if err = e.Store.SetModelCapacityProfile(ctx, profile); err != nil {
		return protocol.ModelCapacityResult{}, err
	}
	e.invalidateCapacityProfile(profile)
	return e.ModelCapacity(ctx, p)
}

// DefaultContextWindow is assumed when no runtime, catalog, override or learned
// window exists. It never blocks a turn: a confirmed overflow lowers it to the
// limit the server reports, and observed usage near it raises it.
const DefaultContextWindow = 256000

// minLearnedWindow keeps a guessed window from collapsing below a usable size.
const minLearnedWindow = 8192

// ErrCapacityUnknown is returned only for a request whose capacity has no
// positive window. resolveCapacity always supplies one, so it is a safety net.
var ErrCapacityUnknown = errors.New("model context window is not set")

// observedWindow is a window learned from a provider response. Authoritative
// values were stated by the server and are never raised by later usage.
type observedWindow struct {
	Window        int
	Authoritative bool
}

func (e *Engine) observedWindowFor(key capacityKey) (observedWindow, bool) {
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	w, ok := e.observedCapacity[key]
	return w, ok && w.Window > 0
}

// learnContextOverflow records the serving window after a confirmed context
// overflow: the limit parsed from the provider error when stated, otherwise
// half of the rejected request when the window was only assumed.
func (e *Engine) learnContextOverflow(res resolved, err error, r requestBudgetReport) {
	key, keyErr := capacityIdentity(res)
	if keyErr != nil {
		return
	}
	window, authoritative := 0, false
	var provider *llm.Error
	if errors.As(err, &provider) {
		window = llm.ParseContextLimit(provider.Body)
		authoritative = window > 0
	}
	if window == 0 {
		if r.CapacitySource != "default" && r.CapacitySource != "observed" {
			return
		}
		window = max(minLearnedWindow, (r.EstimatedInput+r.ReservedOutput)/2)
		if r.Window > 0 && window >= r.Window {
			return
		}
	}
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	if e.observedCapacity == nil {
		e.observedCapacity = make(map[capacityKey]observedWindow)
	}
	cur := e.observedCapacity[key]
	if !authoritative && cur.Authoritative {
		return
	}
	e.observedCapacity[key] = observedWindow{Window: window, Authoritative: authoritative}
	delete(e.capacityCache, key)
}

// raiseObservedWindow lets an assumed window grow when a successful request
// came close to it, so large-context servers are not compacted needlessly.
func (e *Engine) raiseObservedWindow(res resolved, r requestBudgetReport, input int64) {
	if (r.CapacitySource != "default" && r.CapacitySource != "observed") || r.Window <= 0 || input <= 0 {
		return
	}
	if input*10 < int64(r.Window)*8 {
		return
	}
	target := min(input*5/4, int64(2147483647))
	key, err := capacityIdentity(res)
	if err != nil {
		return
	}
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	if e.observedCapacity == nil {
		e.observedCapacity = make(map[capacityKey]observedWindow)
	}
	cur := e.observedCapacity[key]
	if cur.Authoritative || target <= int64(max(cur.Window, r.Window)) {
		return
	}
	e.observedCapacity[key] = observedWindow{Window: int(target)}
	delete(e.capacityCache, key)
}

type capacityKey struct{ Provider, EndpointID, Model, CredentialID string }
type capacityCacheEntry struct {
	Capacity  models.Capacity
	ExpiresAt time.Time
}

func capacityIdentity(res resolved) (capacityKey, error) {
	provider := res.sel.Provider
	base := res.cred.Material.BaseURL
	if base == "" {
		base = res.cred.Record.BaseURL
	}
	if base == "" {
		base = defaultCapacityEndpoint(provider)
	}
	var id string
	if base == "" {
		// Custom adapters can provide explicit metadata without an HTTP endpoint.
		sum := sha256.Sum256([]byte("adapter:" + provider))
		id = hex.EncodeToString(sum[:])
	} else {
		var err error
		id, err = models.EndpointIdentity(provider, base)
		if err != nil {
			return capacityKey{}, err
		}
	}
	return capacityKey{Provider: provider, EndpointID: id, Model: res.sel.Model, CredentialID: res.cred.Record.ID}, nil
}
func (e *Engine) invalidateCapacity(res resolved) {
	key, err := capacityIdentity(res)
	if err != nil {
		return
	}
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	delete(e.capacityCache, key)
}
func (e *Engine) invalidateCapacityProfile(p protocol.ModelCapacityProfile) {
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	for key := range e.capacityCache {
		if key.Provider == p.Provider && key.EndpointID == p.EndpointID && key.Model == p.Model {
			delete(e.capacityCache, key)
		}
	}
}
func (e *Engine) resolveCapacity(ctx context.Context, res resolved) (models.Capacity, error) {
	key, err := capacityIdentity(res)
	if err != nil {
		return models.Capacity{}, err
	}
	e.capacityMu.Lock()
	cached, ok := e.capacityCache[key]
	e.capacityMu.Unlock()
	if ok && time.Now().Before(cached.ExpiresAt) {
		return cached.Capacity, nil
	}
	profile := protocol.ModelCapacityProfile{Provider: key.Provider, EndpointID: key.EndpointID, Model: key.Model, Backend: "generic"}
	if e.Store != nil && key.CredentialID != "" {
		saved, readErr := e.Store.GetModelCapacityProfile(ctx, key.Provider, key.EndpointID, key.Model)
		if readErr != nil && !errors.Is(readErr, store.ErrNotFound) {
			return models.Capacity{}, readErr
		}
		if readErr == nil {
			profile = saved
		}
	}
	cap := models.Capacity{EndpointID: key.EndpointID}
	var runtime llm.RuntimeCapacity
	var runtimeErr error
	if profile.Backend == "ollama" {
		cred := res.cred.Material
		if cred.BaseURL == "" {
			cred.BaseURL = res.cred.Record.BaseURL
		}
		runtime, runtimeErr = llm.DiscoverRuntimeCapacity(ctx, &http.Client{}, cred, profile.Backend, res.sel.Model)
	} else if e.LLMs != nil {
		if provider, exists := e.LLMs.Get(res.sel.Provider); exists {
			if source, yes := provider.(llm.RuntimeCapacityProvider); yes {
				runtime, runtimeErr = source.RuntimeCapacity(ctx, res.cred.Material, res.sel.Model)
			}
		}
	}
	if runtimeErr == nil && runtime.ContextWindow > 0 {
		cap.Window = runtime.ContextWindow
		cap.MaxOutput = runtime.MaxOutputTokens
		cap.Source = "runtime"
		cap.ObservedAt = time.Now().UTC()
	}
	if cap.Window == 0 && res.sel.Provider != "openai_compatible" && res.meta.ContextWindow > 0 {
		cap.Window = res.meta.ContextWindow
		cap.Source = "catalog"
	}
	if profile.ContextWindow != nil {
		if cap.Window == 0 || *profile.ContextWindow < cap.Window {
			cap.Window = *profile.ContextWindow
		}
		if cap.Source == "runtime" {
			cap.Source = "runtime+override"
		} else {
			cap.Source = "override"
		}
	}
	if observed, ok := e.observedWindowFor(key); ok {
		switch {
		case cap.Window == 0:
			cap.Window, cap.Source = observed.Window, "observed"
		case cap.Source != "runtime" && observed.Window < cap.Window:
			cap.Window, cap.Source = observed.Window, "observed"
		}
	}
	if cap.Window == 0 {
		cap.Window, cap.Source = DefaultContextWindow, "default"
	}
	if profile.MaxOutputTokens != nil && (cap.MaxOutput == 0 || *profile.MaxOutputTokens < cap.MaxOutput) {
		cap.MaxOutput = *profile.MaxOutputTokens
	}
	if cap.MaxOutput > cap.Window {
		cap.MaxOutput = cap.Window
	}
	if profile.ResponseReserveTokens != nil {
		cap.ResponseReserve = *profile.ResponseReserveTokens
	}
	e.capacityMu.Lock()
	if e.capacityCache == nil {
		e.capacityCache = make(map[capacityKey]capacityCacheEntry)
	}
	e.capacityCache[key] = capacityCacheEntry{Capacity: cap, ExpiresAt: time.Now().Add(30 * time.Second)}
	e.capacityMu.Unlock()
	return cap, nil
}
