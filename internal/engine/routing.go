package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

const routingSettingKey = "model_routing"

func (e *Engine) loadRouting(ctx context.Context) error {
	var saved protocol.RoutingConfig
	if err := e.Store.GetSetting(ctx, routingSettingKey, &saved); err == nil {
		if err := validateRouting(saved); err != nil {
			return fmt.Errorf("saved model routing: %w", err)
		}
		e.routing = saved
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	for _, m := range e.Cfg.Models.Configured {
		e.routing.Models = append(e.routing.Models, protocol.ConfiguredModel{
			ID: m.ID, Name: m.Name, Provider: config.NormalizeProvider(m.Provider), Model: m.Model, Enabled: m.IsEnabled(),
		})
	}
	for _, p := range e.Cfg.Models.Pools {
		e.routing.Pools = append(e.routing.Pools, protocol.ModelPool{
			ID: p.ID, Name: p.Name, Strategy: p.Strategy, Models: append([]string(nil), p.Models...), Enabled: p.IsEnabled(),
		})
	}
	e.routing.DefaultPool = e.Cfg.Models.DefaultPool

	// Existing installations start with their configured default as the only
	// approved model. Provider catalogs remain suggestions, not configuration.
	if len(e.routing.Models) == 0 {
		provider := config.NormalizeProvider(e.Cfg.LLM.Provider)
		if provider == "" {
			provider = "claude"
		}
		model := e.Cfg.LLM.Model
		if model == "" {
			model = e.defaultModel(provider)
		}
		if model != "" {
			e.routing.Models = []protocol.ConfiguredModel{{
				ID: provider + "-default", Name: providerName(provider), Provider: provider, Model: model, Enabled: true,
			}}
		}
	}
	if err := validateRouting(e.routing); err != nil {
		return err
	}
	return e.Store.SetSetting(ctx, routingSettingKey, e.routing)
}

func providerName(provider string) string {
	switch provider {
	case "claude":
		return "Claude"
	case "openai":
		return "OpenAI"
	case "gemini":
		return "Gemini"
	case "openai_compatible":
		return "OpenAI-compatible"
	default:
		return provider
	}
}

func validateRouting(c protocol.RoutingConfig) error {
	models := map[string]bool{}
	for i := range c.Models {
		m := &c.Models[i]
		m.ID, m.Name, m.Provider, m.Model = strings.TrimSpace(m.ID), strings.TrimSpace(m.Name), config.NormalizeProvider(m.Provider), strings.TrimSpace(m.Model)
		if m.ID == "" || m.Provider == "" || m.Model == "" {
			return fmt.Errorf("configured models require id, provider and model")
		}
		if models[m.ID] {
			return fmt.Errorf("duplicate configured model %q", m.ID)
		}
		models[m.ID] = true
	}
	pools := map[string]bool{}
	for _, p := range c.Pools {
		if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("model pools require id and name")
		}
		if pools[p.ID] {
			return fmt.Errorf("duplicate model pool %q", p.ID)
		}
		pools[p.ID] = true
		if p.Strategy != "" && p.Strategy != "priority" && p.Strategy != "balanced" && p.Strategy != "quality" && p.Strategy != "fast" && p.Strategy != "cheap" {
			return fmt.Errorf("model pool %q has unknown strategy %q", p.ID, p.Strategy)
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("model pool %q is empty", p.ID)
		}
		for _, id := range p.Models {
			if !models[id] {
				return fmt.Errorf("model pool %q refers to unknown model %q", p.ID, id)
			}
		}
	}
	if c.DefaultPool != "" && !pools[c.DefaultPool] {
		return fmt.Errorf("unknown default model pool %q", c.DefaultPool)
	}
	return nil
}

func (e *Engine) RoutingConfig() protocol.RoutingConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Always answer with lists, never null: a client that reads `.pools.length`
	// should not have to special-case an empty registry.
	c := e.routing
	c.Models = append(make([]protocol.ConfiguredModel, 0, len(c.Models)), c.Models...)
	c.Pools = append(make([]protocol.ModelPool, 0, len(c.Pools)), c.Pools...)
	for i := range c.Pools {
		c.Pools[i].Models = append([]string(nil), c.Pools[i].Models...)
	}
	return c
}

func (e *Engine) SetRoutingConfig(ctx context.Context, c protocol.RoutingConfig) (protocol.RoutingConfig, error) {
	for i := range c.Models {
		c.Models[i].Provider = config.NormalizeProvider(c.Models[i].Provider)
	}
	if err := validateRouting(c); err != nil {
		return protocol.RoutingConfig{}, err
	}
	if err := e.Store.SetSetting(ctx, routingSettingKey, c); err != nil {
		return protocol.RoutingConfig{}, err
	}
	e.mu.Lock()
	e.routing = c
	e.mu.Unlock()
	return e.RoutingConfig(), nil
}

// defaultPool is the pool a chat with no model of its own uses.
func (e *Engine) defaultPool() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.routing.DefaultPool
}

// poolModels returns a pool and its enabled models, in the order it lists them.
func (e *Engine) poolModels(id string) (protocol.ModelPool, []protocol.ConfiguredModel, error) {
	c := e.RoutingConfig()
	byID := make(map[string]protocol.ConfiguredModel, len(c.Models))
	for _, m := range c.Models {
		byID[m.ID] = m
	}
	for _, p := range c.Pools {
		if p.ID != id {
			continue
		}
		if !p.Enabled {
			return p, nil, fmt.Errorf("model pool %q is turned off", p.Name)
		}
		out := make([]protocol.ConfiguredModel, 0, len(p.Models))
		for _, modelID := range p.Models {
			if m, ok := byID[modelID]; ok && m.Enabled {
				out = append(out, m)
			}
		}
		if len(out) == 0 {
			return p, nil, fmt.Errorf("model pool %q has no enabled models", p.Name)
		}
		return p, out, nil
	}
	return protocol.ModelPool{}, nil, fmt.Errorf("model pool %q is not configured", id)
}

// configuredFrom pins a direct choice. Other models require an explicit pool.
func (e *Engine) configuredFrom(provider, model string) []protocol.ConfiguredModel {
	head := protocol.ConfiguredModel{ID: provider + "/" + model, Provider: provider, Model: model, Enabled: true}
	for _, m := range e.RoutingConfig().Models {
		if m.Provider == provider && m.Model == model {
			head = m
			head.Enabled = true
			break
		}
	}
	return []protocol.ConfiguredModel{head}
}

// candidateSet is what a selection resolves to before the router sees it: the
// approved models it may use, in the order to try them.
type candidateSet struct {
	provider   string // "" when a pool decides
	model      string
	explicit   bool // the user chose this model or pool, rather than a default
	poolID     string
	strategy   string
	candidates []protocol.ConfiguredModel
}

// pickCandidates turns a selection into that set. A selection names either one
// model ("claude/claude-sonnet-5") or a pool ("pool/coding"); with neither, the
// default pool decides, and failing that the configured provider does.
func (e *Engine) pickCandidates(sel protocol.ModelSelection) (candidateSet, error) {
	var out candidateSet
	provider := config.NormalizeProvider(sel.Provider)
	model := sel.Model
	out.explicit = provider != "" || model != ""
	if provider == "" {
		// A bare model name still means that model, on the configured provider;
		// only a selection with nothing at all falls through to the pool.
		if pool := e.defaultPool(); pool != "" && model == "" {
			provider, model = protocol.PoolProvider, pool
		} else {
			provider = firstNonEmpty(e.Cfg.LLM.Provider, "claude")
		}
	}
	if provider == protocol.PoolProvider {
		poolID := firstNonEmpty(model, e.defaultPool())
		if poolID == "" {
			return out, errors.New("no model pool is selected")
		}
		pool, ms, err := e.poolModels(poolID)
		if err != nil {
			return out, err
		}
		out.poolID, out.strategy, out.candidates = poolID, pool.Strategy, ms
		return out, nil
	}
	if _, ok := e.LLMs.Get(provider); !ok {
		return out, fmt.Errorf("unknown provider %q", provider)
	}
	if model == "" {
		model = e.defaultModel(provider)
	}
	if model == "" {
		return out, fmt.Errorf("no model selected for %s; pick one in the chat or set a default in Settings", provider)
	}
	// A direct selection remains confined to this model.
	out.provider, out.model = provider, model
	out.candidates = e.configuredFrom(provider, model)
	return out, nil
}
