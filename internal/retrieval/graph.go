package retrieval

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/protocol"
	"sort"
	"strconv"
)

func liveNode(n protocol.WorkNode) bool {
	return n.ValidUntil == nil && n.SupersededBy == "" && n.Status != protocol.StatusRejected && n.Status != protocol.StatusSuperseded
}

func GraphCandidates(d protocol.WorkDetail, active []protocol.Evidence, stale map[string]bool) ([]Candidate, error) {
	if d.Work.ID == "" || len(d.Nodes) > 4096 || len(d.Edges) > 8192 {
		return nil, errors.New("invalid retrieval graph")
	}
	nodes := map[string]protocol.WorkNode{}
	distance := map[string]int{}
	var queue []string
	for _, n := range d.Nodes {
		if n.ID == "" || n.WorkID != d.Work.ID {
			return nil, errors.New("invalid retrieval node")
		}
		if _, ok := nodes[n.ID]; ok {
			return nil, errors.New("duplicate retrieval node")
		}
		nodes[n.ID] = n
		if !liveNode(n) {
			continue
		}
		seed := n.Kind == protocol.NodeTask && n.Status != protocol.StatusCompleted || n.Kind == protocol.NodeCriterion || n.Kind == protocol.NodeDecision && n.Status == protocol.StatusApproved
		if n.Kind == protocol.NodeUnknown {
			var m struct {
				Blocking bool `json:"blocking"`
			}
			if len(n.Content) > 0 && json.Unmarshal(n.Content, &m) != nil {
				return nil, errors.New("invalid unknown metadata")
			}
			seed = m.Blocking && n.Status != protocol.StatusResolved
		}
		if seed {
			distance[n.ID] = 0
			queue = append(queue, n.ID)
		}
	}
	adj := map[string][]string{}
	for _, e := range d.Edges {
		if e.WorkID != d.Work.ID {
			return nil, errors.New("foreign retrieval edge")
		}
		if _, ok := nodes[e.FromNodeID]; !ok {
			return nil, errors.New("missing retrieval edge source")
		}
		if _, ok := nodes[e.ToNodeID]; !ok {
			return nil, errors.New("missing retrieval edge target")
		}
		switch e.Relation {
		case protocol.RelRequires, protocol.RelServes, protocol.RelDependsOn, protocol.RelSupports, protocol.RelImplements, protocol.RelVerifies, protocol.RelContradicts:
			adj[e.FromNodeID] = append(adj[e.FromNodeID], e.ToNodeID)
			adj[e.ToNodeID] = append(adj[e.ToNodeID], e.FromNodeID)
		}
	}
	sort.Strings(queue)
	for i := 0; i < len(queue); i++ {
		if len(distance) > MaxGraphNodes {
			return nil, errors.New("retrieval graph oversized")
		}
		id := queue[i]
		if distance[id] >= MaxGraphDepth {
			continue
		}
		sort.Strings(adj[id])
		for _, next := range adj[id] {
			if !liveNode(nodes[next]) {
				continue
			}
			if _, ok := distance[next]; !ok {
				distance[next] = distance[id] + 1
				queue = append(queue, next)
			}
		}
	}
	makeCandidate := func(id, kind, body string, dist int) Candidate {
		return Candidate{ID: id, Kind: kind, Body: body, Distance: dist, ThreadID: d.Work.ThreadID, WorkID: d.Work.ID, ProjectID: d.Work.ProjectID}
	}
	var out []Candidate
	refs := map[string]int{}
	for id, dist := range distance {
		n := nodes[id]
		for _, eid := range n.EvidenceIDs {
			old, ok := refs[eid]
			if !ok || dist < old {
				refs[eid] = dist
			}
		}
		if len(n.Title) > MaxBodyBytes {
			continue
		}
		switch n.Kind {
		case protocol.NodeRequirement, protocol.NodeArtifact:
		case protocol.NodeDecision:
			if n.Status != protocol.StatusApproved {
				continue
			}
		default:
			continue
		}
		c := makeCandidate("node:"+id, n.Kind, n.Title, dist)
		c.SourceRevision = strconv.Itoa(n.Revision)
		out = append(out, c)
	}
	allowed := map[string]bool{}
	for _, e := range active {
		if e.WorkID == d.Work.ID {
			allowed[e.ID] = true
		}
	}
	for _, e := range d.Evidence {
		if e.WorkID != d.Work.ID || !allowed[e.ID] || e.StaleAt != nil || stale[e.NodeID] || len(e.Summary) > MaxBodyBytes {
			continue
		}
		dist, linked := distance[e.NodeID]
		if r, ok := refs[e.ID]; ok && (!linked || r < dist) {
			dist, linked = r, true
		}
		if !linked {
			continue
		}
		c := makeCandidate("evidence:"+e.ID, "evidence", e.Summary, dist)
		c.SourceRevision = e.SourceRevision
		if e.Availability == protocol.AvailUnavailable {
			c.Body += " [source body unavailable; summary only]"
		}
		out = append(out, c)
	}
	for _, e := range d.Edges {
		if e.Relation != protocol.RelContradicts {
			continue
		}
		_, a := distance[e.FromNodeID]
		_, b := distance[e.ToNodeID]
		if a || b {
			out = append(out, makeCandidate("contradiction:"+e.FromNodeID+":"+e.ToNodeID, "artifact", fmt.Sprintf("Unresolved contradiction between %q and %q; do not treat either as resolved by retrieval.", e.FromNodeID, e.ToNodeID), 0))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
