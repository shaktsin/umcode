package agentrt

import (
	"fmt"
	"math"

	"github.com/shaktsin/umcode/internal/protocol"
)

// SpawnRequest is a request to spawn an agent from an approved plan.
// PlanEntry is the plan agent's Name; a nil Capabilities means "use the
// plan entry's".
type SpawnRequest struct {
	TaskID, CallID, CallerAgentID, Name, PlanEntry string
	Assignment                                     protocol.AgentAssignment
	Capabilities                                   *protocol.AgentCapabilities
	Model, ContextMode                             string
	BudgetShare                                    float64
}

// AdmissionInput is everything Admit needs; Admit performs no I/O.
type AdmissionInput struct {
	Plan            protocol.AgentPlan
	Caller          protocol.RuntimeAgent
	All             []protocol.RuntimeAgent
	ParentShareFree float64
	Options         ResolveOptions
	Request         SpawnRequest
}

// Admission is the result of a successful admission.
type Admission struct {
	Entry    protocol.PlanAgent
	Granted  protocol.AgentCapabilities
	Rejected []protocol.CapabilityRejection
	Model    string
	Share    float64
	Depth    int
}

// AdmissionError is a refusal naming the offending field.
type AdmissionError struct {
	Field, Message string
}

func (e *AdmissionError) Error() string {
	return fmt.Sprintf("admission refused (%s): %s", e.Field, e.Message)
}

func refuse(field, format string, args ...any) error {
	return &AdmissionError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Admit checks a spawn request against the approved plan. The first failing
// check wins; capability shortfalls are a partial grant, never an error.
func Admit(in AdmissionInput) (Admission, error) {
	if in.Plan.State != protocol.PlanApproved {
		return Admission{}, refuse("plan", "plan is %q; it must be approved before agents can be spawned", string(in.Plan.State))
	}
	var entry protocol.PlanAgent
	found := false
	for _, a := range in.Plan.Agents {
		if a.Name == in.Request.PlanEntry {
			entry, found = a, true
			break
		}
	}
	if !found {
		return Admission{}, refuse("planEntry", "plan has no agent named %q", in.Request.PlanEntry)
	}

	children := 0
	for _, a := range in.All {
		if a.ParentAgentID == in.Caller.ID {
			children++
		}
	}
	if in.Caller.Capabilities.SpawnMax <= children {
		return Admission{}, refuse("spawn", "caller may spawn at most %d agents and already has %d", in.Caller.Capabilities.SpawnMax, children)
	}

	lim := in.Plan.Limits
	depth := in.Caller.Depth + 1
	if depth > lim.MaxDepth {
		return Admission{}, refuse("depth", "depth %d exceeds the plan maximum of %d", depth, lim.MaxDepth)
	}
	if len(in.All) >= lim.MaxTotal {
		return Admission{}, refuse("total", "plan allows at most %d agents and %d exist", lim.MaxTotal, len(in.All))
	}

	model := in.Request.Model
	if model == "" {
		model = entry.Model
	} else if model != entry.Model {
		return Admission{}, refuse("model", "model %q differs from the plan entry's model %q. Propose a revised plan.", model, entry.Model)
	}

	share := in.Request.BudgetShare
	if share == 0 {
		share = lim.SliceShare
	}
	if math.IsNaN(share) || share < 0 || share > 1 {
		return Admission{}, refuse("budget.share", "requested share %v is invalid; it must be in (0, 1]", share)
	}
	if share > in.ParentShareFree+shareEpsilon {
		return Admission{}, refuse("budget.share", "requested share %.4g exceeds the free share %.4g", share, in.ParentShareFree)
	}

	requested := entry.Capabilities
	if in.Request.Capabilities != nil {
		requested = *in.Request.Capabilities
	}
	granted, rejected := Resolve(in.Caller.Capabilities, entry.Capabilities, requested, in.Options)
	if !lim.Network && granted.Network {
		granted.Network = false
	}
	if !lim.Network && in.Request.Capabilities != nil && requested.Network {
		granted.Network = false
		dup := false
		for _, r := range rejected {
			if r.Field == "capabilities.network" {
				dup = true
			}
		}
		if !dup {
			rejected = append(rejected, protocol.CapabilityRejection{Field: "capabilities.network", Reason: "network access is disabled by the plan"})
		}
	}
	return Admission{Entry: entry, Granted: granted, Rejected: rejected, Model: model, Share: share, Depth: depth}, nil
}
