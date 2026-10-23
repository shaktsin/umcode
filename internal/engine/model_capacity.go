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

var ErrCapacityUnknown = errors.New("model serving capacity is unknown; set its context window in Settings → Models → Model context capacity")

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
	if cap.Window <= 0 {
		return models.Capacity{}, ErrCapacityUnknown
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
