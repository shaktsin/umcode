package engine

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/work"
)

// Identities are required even when the optional semantic compiler declines.
// Keep this separate from semantic prose and refuse oversized state rather
// than silently omitting the identities needed for a revision-checked update.
const maxWorkflowIdentityBytes = 64 * 1024

func (e *Engine) workflowIdentityLayer(ctx context.Context, threadID string) (promptLayer, error) {
	if e.Cfg == nil || !e.optimizationPolicy(ctx).DesignedWorkflow {
		return promptLayer{}, nil
	}
	w, ok, err := e.Store.OpenWorkForThread(ctx, threadID)
	if err != nil {
		return promptLayer{}, errWorkflowUnavailable
	}
	if !ok {
		return promptLayer{}, nil
	}
	d, err := e.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return promptLayer{}, errWorkflowUnavailable
	}
	type node struct {
		ID          string   `json:"id"`
		Kind        string   `json:"kind"`
		Status      string   `json:"status"`
		Revision    int      `json:"revision"`
		EvidenceIDs []string `json:"evidenceIds,omitempty"`
	}
	type evidence struct {
		ID     string `json:"id"`
		NodeID string `json:"nodeId,omitempty"`
		Kind   string `json:"kind"`
	}
	p := struct {
		Work struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			Depth    string `json:"workflowDepth"`
		} `json:"work"`
		Nodes    []node     `json:"nodes"`
		Evidence []evidence `json:"evidence"`
	}{}
	p.Work.ID, p.Work.Revision, p.Work.Depth = w.ID, w.Revision, w.WorkflowDepth
	for _, n := range d.Nodes {
		if n.Status == protocol.StatusSuperseded || n.ValidUntil != nil || n.SupersededBy != "" {
			continue
		}
		p.Nodes = append(p.Nodes, node{n.ID, n.Kind, n.Status, n.Revision, n.EvidenceIDs})
	}
	for _, ev := range work.ActiveEvidence(d) {
		if ev.StaleAt == nil && ev.Availability != protocol.AvailUnavailable {
			p.Evidence = append(p.Evidence, evidence{ev.ID, ev.NodeID, ev.Kind})
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return promptLayer{}, errWorkflowUnavailable
	}
	if len(raw) > maxWorkflowIdentityBytes {
		return promptLayer{}, errors.New("workflow identity context exceeds limit")
	}
	return promptLayer{Name: "workflow_identities", Text: "Canonical workflow identities (metadata only):\n" + string(raw)}, nil
}
