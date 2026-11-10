package engine

import (
	"context"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/router"
)

// RoutePreview answers "what would the next message run on, and what would it
// fall back to" without starting a turn. The composer uses it for the model
// chip; Settings uses it to show what a key is actually good for.
func (e *Engine) RoutePreview(ctx context.Context, p protocol.ModelRouteParams) (protocol.ModelRouteResult, error) {
	var res protocol.ModelRouteResult
	sel := p.Override
	if p.ThreadID != "" {
		th, err := e.Store.GetThread(ctx, p.ThreadID)
		if err != nil {
			return res, err
		}
		if sel.Provider == "" {
			sel.Provider, sel.Model, sel.CredentialID = th.Settings.Provider, th.Settings.Model, th.Settings.CredentialID
		}
		if sel.Complexity == "" {
			sel.Complexity = th.Settings.Complexity
		}
	}
	e.mu.Lock()
	def := e.defaultCplx
	e.mu.Unlock()
	level := protocol.NormalizeComplexity(firstNonEmpty(string(p.Complexity), string(sel.Complexity), string(def)))
	if level == "" {
		level = protocol.ComplexityMedium
	}
	if p.Pool != "" {
		sel.Provider, sel.Model = protocol.PoolProvider, p.Pool
	}
	pick, err := e.pickCandidates(sel)
	if err != nil {
		return res, err
	}
	pinned := protocol.ModelSelection{CredentialID: sel.CredentialID}
	if pick.explicit {
		pinned.Provider, pinned.Model = pick.provider, pick.model
	}
	plan, err := e.Router.Plan(ctx, router.Request{
		Pinned:     pinned,
		Tier:       router.TierForStrategy(pick.strategy),
		Candidates: pick.candidates,
		Strategy:   pick.strategy,
		Need:       router.Capabilities{Tools: true},
	})
	if err != nil {
		return res, err
	}
	res.Complexity = level
	res.Reason = plan.Reason
	res.Alternatives = []protocol.RouteInfo{}
	for i, rt := range plan.Routes {
		info := rt.Info()
		if i == 0 {
			res.Chosen = &info
			continue
		}
		res.Alternatives = append(res.Alternatives, info)
	}
	res.Alternatives = append(res.Alternatives, plan.Blocked...)
	return res, nil
}

// ModelHealth reports what the router has learned: which keys and models are
// cooling down, and how requests have been going.
func (e *Engine) ModelHealth(ctx context.Context) (protocol.ModelHealthResult, error) {
	rows, err := e.Router.Health(ctx)
	if err != nil {
		return protocol.ModelHealthResult{}, err
	}
	if rows == nil {
		rows = []protocol.ModelHealthRow{}
	}
	return protocol.ModelHealthResult{Rows: rows}, nil
}

// ClearCooldown lets a key be tried again straight away.
func (e *Engine) ClearCooldown(ctx context.Context, provider, model, credID string) error {
	return e.Store.ClearCooldown(ctx, provider, model, credID)
}
