package engine

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/store"
	"sync"
	"time"
)

// evaluationProvider records metadata only and forwards every event unchanged.
type evaluationProvider struct {
	inner     llm.Provider
	model     evaluation.Model
	synthetic bool
	sink      func(evaluation.Attempt) error
	before    func(context.Context, string, float64) error
	mu        sync.Mutex
	failure   error
}

func (p *evaluationProvider) ID() string { return p.inner.ID() }
func (p *evaluationProvider) ListModels(ctx context.Context, c llm.Credential) ([]string, error) {
	return p.inner.ListModels(ctx, c)
}
func (p *evaluationProvider) diagnostic() error { p.mu.Lock(); defer p.mu.Unlock(); return p.failure }
func (p *evaluationProvider) record(a evaluation.Attempt) {
	if p.sink == nil {
		return
	}
	if err := p.sink(a); err != nil {
		p.mu.Lock()
		p.failure = errors.New("evaluation attempt persistence failed")
		p.mu.Unlock()
	}
}
func (p *evaluationProvider) Stream(ctx context.Context, c llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	a := evaluation.Attempt{ID: store.NewID("dispatch"), Provider: p.ID(), Model: req.Model, PriceID: p.model.Price.ID, Synthetic: p.synthetic, Subscription: c.Kind == llm.KindChatGPT, Outcome: "error"}
	start := time.Now()
	schemas, _ := json.Marshal(req.Tools)
	a.SchemaTokens = llm.EstimateTokens(string(schemas))
	a.LayerEstimates = map[string]int64{"system": llm.EstimateTokens(req.System), "input": estimateInput(req)}
	if req.MaxTokens <= 0 || int64(llm.EffectiveOutputLimit(p.ID(), req)) > p.model.MaxOutput {
		return nil, errors.New("evaluation request exceeds declared output limit")
	}
	if !p.synthetic {
		if req.Model != p.model.ID {
			return nil, errors.New("live evaluation cannot fall back to an undeclared model")
		}
		body, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		if int64(len(body))+1024+p.model.MaxOutput > p.model.Window {
			return nil, errors.New("evaluation request exceeds conservative declared context bound")
		}
	}
	maximum := (float64(p.model.Window)*p.model.Price.Input + float64(p.model.MaxOutput)*p.model.Price.Output) / 1e6
	a.ReservedUSD = maximum
	if p.before != nil {
		if err := p.before(ctx, a.ID, maximum); err != nil {
			return nil, err
		}
	} else if !p.synthetic {
		return nil, errors.New("live evaluation requires durable reservation callback")
	}
	ch, err := p.inner.Stream(ctx, c, req)
	if err != nil {
		a.MissingReason = "provider failed before usage"
		a.ElapsedMS = time.Since(start).Milliseconds()
		p.record(a)
		return nil, err
	}
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		defer func() {
			a.ElapsedMS = time.Since(start).Milliseconds()
			cost, reason, e := evaluation.AttemptCost(a, p.model.Price)
			a.ObservedUSD = cost
			a.MissingReason = reason
			if e != nil {
				a.Reported = false
				a.ObservedUSD = nil
				a.MissingReason = "invalid provider usage"
			}
			p.record(a)
		}()
		for {
			var event llm.Event
			select {
			case <-ctx.Done():
				a.Outcome = "cancelled"
				p.drainCancelled(ch, &a)
				return
			case next, ok := <-ch:
				if !ok {
					return
				}
				event = next
			}
			if event.Type == llm.EventToolCall {
				a.ToolRounds = 1
			}
			if event.Usage.Reported {
				u := event.Usage
				a.Reported = true
				a.Subscription = a.Subscription || u.Subscription
				input, cached, output, reasoning := u.InputTokens, u.CachedInputTokens, u.OutputTokens, u.ReasoningTokens
				a.Input = &input
				a.Cached = &cached
				a.Output = &output
				a.Reasoning = &reasoning
			}
			if event.Type == llm.EventDone {
				a.Outcome = "ok"
			}
			if event.Type == llm.EventError {
				a.Outcome = "error"
				a.Partial = a.Reported
			}
			select {
			case out <- event:
			case <-ctx.Done():
				a.Outcome = "cancelled"
				p.drainCancelled(ch, &a)
				return
			}
		}
	}()
	return out, nil
}

// A bounded drain lets cancellation-aware providers deliver their terminal
// accounting without stranding a producer blocked on its final send.
func (p *evaluationProvider) drainCancelled(ch <-chan llm.Event, a *evaluation.Attempt) {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			a.Partial = a.Reported
			return
		case event, ok := <-ch:
			if !ok {
				a.Partial = a.Reported
				return
			}
			if event.Type == llm.EventToolCall {
				a.ToolRounds = 1
			}
			if event.Usage.Reported {
				u := event.Usage
				a.Reported = true
				a.Partial = true
				a.Subscription = a.Subscription || u.Subscription
				input, cached, output, reasoning := u.InputTokens, u.CachedInputTokens, u.OutputTokens, u.ReasoningTokens
				a.Input = &input
				a.Cached = &cached
				a.Output = &output
				a.Reasoning = &reasoning
			}
		}
	}
}
