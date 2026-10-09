// Package workflowgraph contains persistence-independent workflow derivation.
package workflowgraph

import (
	"encoding/json"

	"github.com/shaktsin/umcode/internal/protocol"
)

// ValidGate is shared by graph preparation and the approval transaction.
func ValidGate(kind string) bool {
	switch kind {
	case "public_contract", "persisted_schema", "security", "destructive", "billing", "architecture_choice", "accepted_risk":
		return true
	}
	return false
}

func active(n protocol.WorkNode) bool {
	return n.Status != protocol.StatusRejected && n.Status != protocol.StatusSuperseded && n.ValidUntil == nil && n.SupersededBy == ""
}

// DeriveApprovalTaskStatuses applies the one outcome-specific rule: declining
// an open blocking risk leaves it open and finalizes waiting tasks as blocked.
// All normal readiness rules remain in DeriveTaskStatuses.
func DeriveApprovalTaskStatuses(d protocol.WorkDetail, deniedRisk bool) map[string]string {
	statuses := DeriveTaskStatuses(d)
	if deniedRisk {
		for _, n := range d.Nodes {
			if n.Kind == protocol.NodeTask && (n.Status == protocol.StatusPending || n.Status == protocol.StatusReady) {
				statuses[n.ID] = protocol.StatusBlocked
			}
		}
	}
	return statuses
}
func required(n protocol.WorkNode) bool {
	var c struct{ Required *bool }
	_ = json.Unmarshal(n.Content, &c)
	if c.Required != nil {
		return *c.Required
	}
	return n.Kind == protocol.NodeCriterion
}
func blocking(n protocol.WorkNode) bool {
	var c struct{ Blocking bool }
	_ = json.Unmarshal(n.Content, &c)
	return c.Blocking
}
func graphNodes(d protocol.WorkDetail) map[string]protocol.WorkNode {
	out := map[string]protocol.WorkNode{}
	for _, n := range d.Nodes {
		out[n.ID] = n
	}
	return out
}
func selectedOption(d protocol.WorkDetail, n protocol.WorkNode) (protocol.WorkNode, bool) {
	nodes := graphNodes(d)
	var selected protocol.WorkNode
	count := 0
	for _, e := range d.Edges {
		if e.FromNodeID == n.ID && e.Relation == protocol.RelSelects {
			selected = nodes[e.ToNodeID]
			count++
		}
	}
	var c struct {
		Rung int `json:"solution_rung"`
	}
	_ = json.Unmarshal(selected.Content, &c)
	return selected, count == 1 && selected.Kind == protocol.NodeOption && active(selected) && c.Rung >= 1 && c.Rung <= 6
}
func hasCriterion(d protocol.WorkDetail, id string) bool {
	nodes := graphNodes(d)
	for _, e := range d.Edges {
		if e.Relation == protocol.RelVerifies && e.ToNodeID == id {
			n := nodes[e.FromNodeID]
			if n.Kind == protocol.NodeCriterion && active(n) {
				return true
			}
		}
	}
	return false
}
func DeriveTaskStatuses(d protocol.WorkDetail) map[string]string {
	out := map[string]string{}
	nodes := graphNodes(d)
	riskOpen := false
	solution := false
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeDecision && active(n) && n.Status == protocol.StatusApproved {
			if _, ok := selectedOption(d, n); ok {
				solution = true
			}
		}
	}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeUnknown && active(n) && blocking(n) && n.Status != protocol.StatusResolved && n.Status != protocol.StatusAcceptedRisk {
			riskOpen = true
		}
	}
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeTask {
			continue
		}
		status := n.Status
		if status != protocol.StatusPending && status != protocol.StatusReady {
			out[n.ID] = status
			continue
		}
		rejected := false
		ready := !riskOpen && (!required(n) || solution && hasCriterion(d, n.ID))
		for _, e := range d.Edges {
			if e.FromNodeID != n.ID {
				continue
			}
			dep, ok := nodes[e.ToNodeID]
			switch e.Relation {
			case protocol.RelDependsOn:
				seen := map[string]bool{}
				for ok && dep.Status == protocol.StatusSuperseded && dep.SupersededBy != "" {
					if seen[dep.ID] {
						ok = false
						break
					}
					seen[dep.ID] = true
					dep, ok = nodes[dep.SupersededBy]
				}
				ready = ready && ok && dep.Kind == protocol.NodeTask && dep.Status == protocol.StatusCompleted
			case protocol.RelImplements:
				if dep.Kind == protocol.NodeDecision && required(dep) {
					rejected = rejected || dep.Status == protocol.StatusRejected
					_, selected := selectedOption(d, dep)
					ready = ready && active(dep) && dep.Status == protocol.StatusApproved && selected
				}
			}
		}
		if ready {
			status = protocol.StatusReady
		} else if status == protocol.StatusReady || rejected {
			status = protocol.StatusBlocked
		}
		out[n.ID] = status
	}
	return out
}
