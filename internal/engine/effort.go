package engine

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/llm"
)

// lowerLevel returns the effort level one step below level; off stays off and
// an unrecognized value is returned unchanged.
func lowerLevel(level string) string {
	switch level {
	case "high":
		return "medium"
	case "medium":
		return "low"
	case "low", "off":
		return "off"
	}
	return level
}

// roundEffort returns the level for one model request. Self-adapting models get
// the chosen level every round. Other models get it on the first round and after
// a failed tool call or check, and one level lower otherwise ("low" lowers to
// "off").
func roundEffort(chosen string, selfAdapting bool, round int, afterFailure bool) string {
	if selfAdapting || round <= 0 || afterFailure {
		return chosen
	}
	return lowerLevel(chosen)
}

// templateCapsEntry is a cached template detection. A disabled entry never
// expires: the server rejected chat_template_kwargs, so none are sent again.
type templateCapsEntry struct {
	Caps      llm.TemplateCaps
	ExpiresAt time.Time
	Disabled  bool
}

func (e *Engine) templateCaps(ctx context.Context, res *resolved) llm.TemplateCaps {
	key, err := capacityIdentity(*res)
	if err != nil {
		return llm.TemplateCaps{}
	}
	e.capacityMu.Lock()
	cached, ok := e.templateCache[key]
	e.capacityMu.Unlock()
	if ok && (cached.Disabled || time.Now().Before(cached.ExpiresAt)) {
		return cached.Caps
	}
	cred := res.cred.Material
	if cred.BaseURL == "" {
		cred.BaseURL = res.cred.Record.BaseURL
	}
	caps, derr := llm.DiscoverTemplateCaps(ctx, &http.Client{}, cred)
	if derr != nil {
		caps = llm.TemplateCaps{}
	}
	e.capacityMu.Lock()
	if e.templateCache == nil {
		e.templateCache = make(map[capacityKey]templateCapsEntry)
	}
	if cur, exists := e.templateCache[key]; !exists || !cur.Disabled {
		e.templateCache[key] = templateCapsEntry{Caps: caps, ExpiresAt: time.Now().Add(30 * time.Second)}
	}
	e.capacityMu.Unlock()
	return caps
}

// disableTemplateKwargs remembers that the model's server rejected the
// thinking controls.
func (e *Engine) disableTemplateKwargs(res *resolved) {
	key, err := capacityIdentity(*res)
	if err != nil {
		return
	}
	e.capacityMu.Lock()
	defer e.capacityMu.Unlock()
	if e.templateCache == nil {
		e.templateCache = make(map[capacityKey]templateCapsEntry)
	}
	e.templateCache[key] = templateCapsEntry{Disabled: true}
}

// templateKwargsRejected reports a 400 whose body names chat_template_kwargs or
// one of the fields that was sent in it.
func templateKwargsRejected(err error, sent map[string]any) bool {
	var pe *llm.Error
	if len(sent) == 0 || !errors.As(err, &pe) || pe.Status != http.StatusBadRequest {
		return false
	}
	if strings.Contains(pe.Body, "chat_template_kwargs") {
		return true
	}
	for k := range sent {
		if strings.Contains(pe.Body, k) {
			return true
		}
	}
	return false
}

// applyEffort sets the thinking controls of req for one model request.
func (e *Engine) applyEffort(ctx context.Context, res *resolved, req *llm.Request, round int, afterFailure bool) {
	e.applyEffortLowered(ctx, res, req, round, afterFailure, 0)
}

// applyEffortLowered is applyEffort with extra downshifts, used when a
// continuation follows a response that was mostly reasoning.
func (e *Engine) applyEffortLowered(ctx context.Context, res *resolved, req *llm.Request, round int, afterFailure bool, lowered int) {
	e.applyLevel(ctx, res, req, func(chosen string) string {
		level := roundEffort(chosen, res.meta.SelfAdapting, round, afterFailure)
		for range lowered {
			level = lowerLevel(level)
		}
		return level
	})
}

// applyAuxEffort sets the thinking controls for an auxiliary call with a small
// fixed output cap (compaction, task intent). Self-adapting models keep the
// chosen level; others do not think, so the cap is not spent on reasoning.
func (e *Engine) applyAuxEffort(ctx context.Context, res *resolved, req *llm.Request) {
	e.applyLevel(ctx, res, req, func(chosen string) string {
		if res.meta.SelfAdapting {
			return chosen
		}
		return "off"
	})
}

// applyLevel clears and then sets req's thinking controls from the level pick
// returns for the model's chosen level.
func (e *Engine) applyLevel(ctx context.Context, res *resolved, req *llm.Request, pick func(chosen string) string) {
	req.Reasoning, req.ReasoningStyle, req.TemplateKwargs = "", "", nil
	if !res.meta.Reasoning {
		return
	}
	chosen, ok := res.meta.ResolveLevel(res.sel.Complexity)
	if !ok {
		return
	}
	level := pick(chosen)
	if res.meta.CatalogReasoning() {
		req.Reasoning, req.ReasoningStyle = level, res.meta.ReasoningStyle
		return
	}
	if res.meta.ReasoningStyle != "template" {
		return
	}
	if caps := e.templateCaps(ctx, res); caps.Any() {
		req.TemplateKwargs = llm.TemplateKwargs(caps, level)
	}
}
