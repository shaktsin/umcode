package evaluation

import "github.com/shaktsin/umcode/internal/optimization"

// qualificationEvidence checks declared coverage and measurement provenance.
// Smaller campaigns are useful diagnostics but cannot qualify a release.
func qualificationEvidence(c Campaign) []string {
	reasons := []string{}
	full := optimization.All(true)
	required := []optimization.Policy{optimization.All(false), full, {ContextCompiler: true}, {ContextCompiler: true, ContextRetrieval: true}, {ToolResultReducers: true}, {ProgressiveTools: true}, {DesignedWorkflow: true, AutomaticWorkflow: true}, {DesignedWorkflow: true, AutomaticWorkflow: true, AutoPromote: true}}
	for _, field := range []string{"compiler", "retrieval", "reducers", "progressive", "workflow", "memory"} {
		p := full
		switch field {
		case "compiler":
			p.ContextCompiler = false
			p.ContextRetrieval = false
		case "retrieval":
			p.ContextRetrieval = false
		case "reducers":
			p.ToolResultReducers = false
		case "progressive":
			p.ProgressiveTools = false
		case "workflow":
			p.DesignedWorkflow = false
			p.AutomaticWorkflow = false
			p.AutoPromote = false
		case "memory":
			p.AutoPromote = false
		}
		required = append(required, p)
	}
	policies := map[optimization.Policy]string{}
	for _, a := range c.Manifest.Arms {
		p, err := EffectivePolicy(a)
		if err == nil {
			policies[p] = a.ID
		}
	}
	for _, p := range required {
		if policies[p] == "" {
			reasons = append(reasons, "Required component/removal arm coverage is incomplete.")
			break
		}
	}
	policyPairs := map[[2]optimization.Policy]bool{}
	for _, pair := range c.Manifest.Comparisons {
		b, _ := c.Manifest.Arm(pair.Baseline)
		t, _ := c.Manifest.Arm(pair.Treatment)
		bp, be := EffectivePolicy(b)
		tp, te := EffectivePolicy(t)
		if be == nil && te == nil {
			policyPairs[[2]optimization.Policy{bp, tp}] = true
		}
	}
	for i, p := range required {
		base := optimization.All(false)
		treat := p
		if i == 0 {
			continue
		}
		if i == 3 {
			base = required[2]
		}
		if i == 7 {
			base = required[6]
		}
		if i >= 8 {
			base = p
			treat = full
		}
		if !policyPairs[[2]optimization.Policy{base, treat}] {
			reasons = append(reasons, "Required causal component/removal comparison is missing.")
			break
		}
	}
	scheduled, err := Schedule(c.Manifest)
	if err != nil {
		return append(reasons, "Manifest cannot produce a valid qualification schedule.")
	}
	expected := map[string]Unit{}
	for _, u := range scheduled {
		expected[u.ID] = u
	}
	actual := map[string]bool{}
	for _, u := range c.Units {
		if expected[u.ID] != u || actual[u.ID] {
			reasons = append(reasons, "Campaign schedule integrity mismatch.")
		}
		actual[u.ID] = true
	}
	if len(actual) != len(expected) {
		reasons = append(reasons, "Campaign schedule coverage is incomplete.")
	}
	seen := map[string]bool{}
	dispatches := map[string]bool{}
	for _, r := range c.Results {
		if u, ok := expected[r.Unit.ID]; !ok || u != r.Unit || seen[r.Unit.ID] {
			reasons = append(reasons, "Result identity/coverage mismatch.")
		}
		seen[r.Unit.ID] = true
		model, ok := c.Manifest.Model(r.Unit.ModelID)
		if !ok {
			continue
		}
		if r.ConfigurationHash != c.ManifestHash || len(r.Attempts) == 0 {
			reasons = append(reasons, "Missing result configuration/attempt provenance.")
		}
		for _, a := range r.Attempts {
			if a.ID == "" || dispatches[a.ID] || a.Synthetic || a.Subscription || a.Provider != model.Provider || a.Model != model.ID || a.PriceID != model.Price.ID || a.PriceID == "" {
				reasons = append(reasons, "Attempt has synthetic or mismatched provider/model/pricing provenance.")
			}
			dispatches[a.ID] = true
			cost, _, e := AttemptCost(a, model.Price)
			if e != nil || cost == nil || a.ObservedUSD == nil || *cost != *a.ObservedUSD {
				reasons = append(reasons, "Attempt billing evidence is missing or inconsistent with frozen pricing.")
			}
		}
	}
	if len(seen) != len(expected) {
		reasons = append(reasons, "Required result coverage is incomplete.")
	}
	// Component arms must actually occur in the declared schedule.
	for _, id := range policies {
		found := false
		for _, u := range scheduled {
			if u.ArmID == id {
				found = true
				break
			}
		}
		if !found {
			reasons = append(reasons, "Required arm is omitted from comparisons.")
			break
		}
	}
	unique := []string{}
	seenReason := map[string]bool{}
	for _, reason := range reasons {
		if !seenReason[reason] {
			seenReason[reason] = true
			unique = append(unique, reason)
		}
	}
	return unique
}
