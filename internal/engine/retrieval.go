package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"github.com/shaktsin/umcode/internal/work"
	"reflect"
	"strings"
)

var retrievalHook func(context.Context)

// retrieve either returns one validated snapshot or discards the entire attempt.
// Error and panic details never enter diagnostics because they may contain source text.
func (e *Engine) retrieve(ctx context.Context, scope retrieval.Scope, d protocol.WorkDetail, request, root string, items []protocol.Item, turnID string) (out []retrieval.Candidate, report retrieval.Report, err error) {
	ctx, cancel := context.WithTimeout(ctx, retrieval.Deadline)
	defer cancel()
	defer func() {
		if recover() != nil {
			out = nil
			report = retrieval.Report{Fallback: "panic"}
			err = errors.New("retrieval failed")
		}
	}()
	if retrievalHook != nil {
		retrievalHook(ctx)
	}
	if err = ctx.Err(); err != nil {
		return nil, retrieval.Report{Fallback: "deadline"}, err
	}
	if d.Work.ID != scope.WorkID || d.Work.ThreadID != scope.ThreadID || d.Work.ProjectID != scope.ProjectID || d.Work.Status != "open" {
		return nil, report, errors.New("retrieval identity mismatch")
	}
	stale := work.Staleness(d, "")
	active := work.ActiveEvidence(d)
	graph, err := retrieval.GraphCandidates(d, active, stale)
	if err != nil {
		return nil, report, err
	}
	q := retrieval.BuildQuery(request, retrievalTaskTitles(d))
	lexical, err := e.Store.SearchRetrieval(ctx, scope, q)
	if err != nil {
		return nil, report, err
	}
	seen := map[string]bool{}
	for _, c := range graph {
		seen[c.ID] = true
	}
	activeIDs := map[string]bool{}
	for _, ev := range active {
		if !stale[ev.NodeID] {
			activeIDs["evidence:"+ev.ID] = true
		}
	}
	for _, c := range lexical {
		if c.ThreadID != scope.ThreadID || (c.Kind != "conversation" && c.WorkID != scope.WorkID) || c.ProjectID != scope.ProjectID {
			return nil, report, errors.New("retrieval identity mismatch")
		}
		if seen[c.ID] || c.Kind == "evidence" && !activeIDs[c.ID] {
			continue
		}
		c.Distance = retrieval.MaxGraphDepth + 1
		graph = append(graph, c)
		seen[c.ID] = true
	}
	out, err = validateRetrievalFiles(ctx, root, graph)
	if err != nil {
		return nil, report, err
	}
	// Reject concurrent canonical changes rather than mix snapshots or trust a stale pass.
	current, err := e.Store.GetWorkDetail(ctx, scope.WorkID)
	if err != nil {
		return nil, report, err
	}
	if !reflect.DeepEqual(current, d) {
		return nil, report, errors.New("retrieval snapshot changed")
	}
	if err = ctx.Err(); err != nil {
		return nil, report, err
	}
	report.Candidates = len(out)
	return out, report, nil
}

func retrievalTaskTitles(d protocol.WorkDetail) []string {
	var titles []string
	size := 0
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeTask && n.Status != protocol.StatusCompleted && n.ValidUntil == nil && n.SupersededBy == "" {
			if size+len(n.Title) > retrieval.MaxInputBytes {
				break
			}
			titles = append(titles, strings.TrimSpace(n.Title))
			size += len(n.Title)
		}
	}
	return titles
}
