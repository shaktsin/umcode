// Package models holds the model catalog (bundled metadata + prices merged
// with user preferences and provider model lists), cost calculation and the
// complexity presets.
package models

import (
	"context"
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

//go:embed catalog.json
var catalogJSON []byte

// Meta is what the engine knows about one model.
type Meta struct {
	Provider       string  `json:"provider"`
	ID             string  `json:"id"`
	DisplayName    string  `json:"displayName"`
	ContextWindow  int     `json:"contextWindow"`
	Tools          bool    `json:"tools"`
	Images         bool    `json:"images"`
	Reasoning      bool    `json:"reasoning"`
	ReasoningStyle string  `json:"reasoningStyle"`
	In             float64 `json:"in"`
	CachedIn       float64 `json:"cachedIn"`
	Out            float64 `json:"out"`
	PriceUnknown   bool    `json:"priceUnknown"`
	Bundled        bool    `json:"-"`
}

type bundle struct {
	Models   []Meta            `json:"models"`
	Defaults map[string]string `json:"defaults"`
	Cheap    map[string]string `json:"cheap"`
}

// Catalog merges bundled data with the store.
type Catalog struct {
	store    *store.Store
	mu       sync.RWMutex
	bundled  map[string]Meta // key provider/model
	order    []string
	defaults map[string]string
	cheap    map[string]string
}

// NewCatalog loads the bundled catalog.
func NewCatalog(st *store.Store) (*Catalog, error) {
	var b bundle
	if err := json.Unmarshal(catalogJSON, &b); err != nil {
		return nil, err
	}
	c := &Catalog{store: st, bundled: map[string]Meta{}, defaults: b.Defaults, cheap: b.Cheap}
	for _, m := range b.Models {
		m.Bundled = true
		k := m.Provider + "/" + m.ID
		c.bundled[k] = m
		c.order = append(c.order, k)
	}
	return c, nil
}

// DefaultModel returns the bundled default model for a provider.
func (c *Catalog) DefaultModel(provider string) string { return c.defaults[provider] }

// CheapModel returns the cheapest sensible model for background jobs (titles, Auto classification).
func (c *Catalog) CheapModel(provider string) string {
	if m := c.cheap[provider]; m != "" {
		return m
	}
	return c.defaults[provider]
}

// Lookup returns metadata for a model, inferring sensible values for unknown ids,
// with user price overrides applied.
func (c *Catalog) Lookup(ctx context.Context, provider, model string) Meta {
	c.mu.RLock()
	m, ok := c.bundled[provider+"/"+model]
	c.mu.RUnlock()
	if !ok {
		m = infer(provider, model)
	}
	if c.store != nil {
		if prefs, err := c.store.ListModelPrefs(ctx); err == nil {
			if p, ok := prefs[provider+"/"+model]; ok && p.PriceOverride {
				m.In, m.CachedIn, m.Out, m.PriceUnknown = p.InputPerMTok, p.CachedInputPerMTok, p.OutputPerMTok, false
			}
		}
	}
	return m
}

func infer(provider, model string) Meta {
	m := Meta{Provider: provider, ID: model, DisplayName: model, Tools: true, PriceUnknown: true}
	id := strings.ToLower(model)
	switch provider {
	case "claude":
		m.Images, m.Reasoning, m.ReasoningStyle = true, true, "adaptive"
		if strings.Contains(id, "haiku") || strings.Contains(id, "3-") {
			m.ReasoningStyle = "budget"
		}
	case "openai":
		m.Images = true
		m.Reasoning = strings.HasPrefix(id, "o") || strings.HasPrefix(id, "gpt-5")
	case "gemini":
		m.Images, m.Reasoning = true, true
		m.ReasoningStyle = "level"
		if strings.HasPrefix(id, "gemini-2") {
			m.ReasoningStyle = "budget"
		}
	case "openai_compatible":
		// Local models: price 0, reasoning unknown.
		m.PriceUnknown = false
	}
	return m
}

// Cost returns the USD cost of usage for a model (0 if its price is unknown).
func Cost(m Meta, u llm.Usage) float64 {
	if m.PriceUnknown || u.Subscription {
		return 0
	}
	uncached := u.InputTokens - u.CachedInputTokens
	if uncached < 0 {
		uncached = 0
	}
	cachedPrice := m.CachedIn
	if cachedPrice == 0 {
		cachedPrice = m.In
	}
	return (float64(uncached)*m.In + float64(u.CachedInputTokens)*cachedPrice + float64(u.OutputTokens)*m.Out) / 1e6
}

// List returns the models for the picker: bundled models plus any the provider
// API reported, with hidden flags and price overrides applied.
func (c *Catalog) List(ctx context.Context, provider string, includeHidden bool) ([]protocol.Model, error) {
	prefs, err := c.store.ListModelPrefs(ctx)
	if err != nil {
		return nil, err
	}
	cache, err := c.store.ListModelCache(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []protocol.Model
	add := func(m Meta, source string) {
		k := m.Provider + "/" + m.ID
		if seen[k] || (provider != "" && m.Provider != provider) {
			return
		}
		seen[k] = true
		pm := protocol.Model{
			Provider: m.Provider, ID: m.ID, DisplayName: m.DisplayName, ContextWindow: m.ContextWindow,
			SupportsTools: m.Tools, SupportsImages: m.Images, SupportsReasoning: m.Reasoning,
			InputPerMTok: m.In, CachedInputPerMTok: m.CachedIn, OutputPerMTok: m.Out, Source: source,
		}
		if p, ok := prefs[k]; ok {
			pm.Hidden = p.Hidden
			if p.PriceOverride {
				pm.InputPerMTok, pm.CachedInputPerMTok, pm.OutputPerMTok = p.InputPerMTok, p.CachedInputPerMTok, p.OutputPerMTok
				pm.Source = "user"
			}
		}
		if pm.Hidden && !includeHidden {
			return
		}
		out = append(out, pm)
	}
	c.mu.RLock()
	for _, k := range c.order {
		add(c.bundled[k], "bundled")
	}
	c.mu.RUnlock()
	provs := make([]string, 0, len(cache))
	for p := range cache {
		provs = append(provs, p)
	}
	sort.Strings(provs)
	for _, p := range provs {
		for _, id := range cache[p] {
			if !chatModel(p, id) {
				continue
			}
			add(infer(p, id), "api")
		}
	}
	return out, nil
}

// chatModel filters provider model lists down to chat models.
func chatModel(provider, id string) bool {
	id = strings.ToLower(id)
	for _, skip := range []string{"embed", "tts", "whisper", "dall-e", "image", "audio", "moderation", "transcribe", "realtime", "search", "aqa"} {
		if strings.Contains(id, skip) {
			return false
		}
	}
	if provider == "openai" {
		return strings.HasPrefix(id, "gpt") || strings.HasPrefix(id, "o") || strings.HasPrefix(id, "chatgpt")
	}
	return true
}
