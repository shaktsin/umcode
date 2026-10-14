package evaluation

import (
	"errors"
	"github.com/shaktsin/umcode/internal/optimization"
)

type Arm struct {
	ID          string               `json:"id"`
	Mode        string               `json:"mode"`
	Policy      *optimization.Policy `json:"policy,omitempty"`
	Attribution string               `json:"attribution,omitempty"`
}

func EffectivePolicy(a Arm) (optimization.Policy, error) {
	if a.Mode == "product-off" || a.Mode == "product-on" {
		if a.Policy != nil {
			return optimization.Policy{}, errors.New("product arm cannot override policy")
		}
		return optimization.All(a.Mode == "product-on"), nil
	}
	if a.Mode != "override" || a.Policy == nil {
		return optimization.Policy{}, errors.New("unknown or incomplete evaluation arm")
	}
	p := *a.Policy
	if p.ContextRetrieval && !p.ContextCompiler || p.AutoPromote && !p.DesignedWorkflow || p.AutomaticWorkflow && !p.DesignedWorkflow || p.DesignedWorkflow && !p.AutomaticWorkflow {
		return p, errors.New("invalid optimization dependencies")
	}
	return p, nil
}
