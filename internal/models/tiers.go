package models

import (
	"context"
	"sort"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Tier lists a provider's models in the order the router should try them for a
// complexity level, best fit first.
//
// The ranking is derived from the catalog rather than hard-coded names, so a
// model released tomorrow slots in on its own: Deep wants the strongest
// reasoning model (price is the only proxy we have for "strongest"), Quick
// wants the cheapest model that can still use tools, and Standard starts at the
// provider's default and works outwards.
func (c *Catalog) Tier(ctx context.Context, provider string, level protocol.Complexity) []protocol.Model {
	list, err := c.List(ctx, provider, false)
	if err != nil || len(list) == 0 {
		return nil
	}
	def := c.DefaultModel(provider)
	cheap := c.CheapModel(provider)

	// Price per million tokens, weighted towards output, which dominates cost.
	price := func(m protocol.Model) float64 { return m.InputPerMTok + 3*m.OutputPerMTok }
	// A model with no known price is a local or unlisted one: treat it as cheap
	// but not free, so a priced model of the same class wins on capability.
	unpriced := func(m protocol.Model) bool { return m.InputPerMTok == 0 && m.OutputPerMTok == 0 }

	out := make([]protocol.Model, len(list))
	copy(out, list)

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch level {
		case protocol.ComplexityHigh:
			// Reasoning first, then the more expensive (stronger) model.
			if a.SupportsReasoning != b.SupportsReasoning {
				return a.SupportsReasoning
			}
			if unpriced(a) != unpriced(b) {
				return unpriced(b)
			}
			return price(a) > price(b)
		case protocol.ComplexityLow:
			// The cheapest model that still takes tools.
			if a.SupportsTools != b.SupportsTools {
				return a.SupportsTools
			}
			if a.ID == cheap != (b.ID == cheap) {
				return a.ID == cheap
			}
			if unpriced(a) != unpriced(b) {
				return unpriced(a)
			}
			return price(a) < price(b)
		default:
			// Standard: the provider's default, then by price ascending.
			if (a.ID == def) != (b.ID == def) {
				return a.ID == def
			}
			if a.SupportsTools != b.SupportsTools {
				return a.SupportsTools
			}
			if unpriced(a) != unpriced(b) {
				return unpriced(b)
			}
			return price(a) < price(b)
		}
	})
	return out
}

// CostPerMTok is a single number for showing "how expensive is this model",
// weighted the same way the tier ranking weighs it.
func CostPerMTok(m protocol.Model) float64 { return m.InputPerMTok + 3*m.OutputPerMTok }
