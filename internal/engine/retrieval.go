package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"github.com/shaktsin/umcode/internal/work"
	"reflect"
	"strings"
	"time"
)

var retrievalHook func(context.Context)

// retrieve either returns one validated snapshot or discards the entire attempt.
// Error and panic details never enter diagnostics because they may contain source text.
func (e *Engine) retrieve(ctx context.Context, scope retrieval.Scope, d protocol.WorkDetail, request, root string, items []protocol.Item, turnID string) (out []retrieval.Candidate, report retrieval.Report, err error) {
	report.Drops = map[string]int{}
	started := time.Now()
	defer func() {
		report.DurationMS = time.Since(started).Milliseconds()
		if errors.Is(err, context.DeadlineExceeded) {
			report.Fallback = "deadline"
		}
	}()
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
	active := retrievalActiveEvidence(d, stale)
	report.Fallback = "graph"
	graph, err := retrieval.GraphCandidates(d, active, stale)
	if err != nil {
		return nil, report, err
	}
	q := retrieval.BuildQuery(request, retrievalTaskTitles(d))
	report.Fallback = "store"
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
		if seen[c.ID] {
			report.Drops["duplicate"]++
			continue
		}
		if (c.Kind == "evidence" && !activeIDs[c.ID]) || (c.Kind == "excerpt" && !activeIDs["evidence:"+c.EvidenceID]) {
			report.Drops["inactive"]++
			continue
		}
		c.Distance = retrieval.MaxGraphDepth + 1
		graph = append(graph, c)
		seen[c.ID] = true
	}
	report.Fallback = "file"
	out, err = validateRetrievalFiles(ctx, root, graph)
	if err != nil {
		return nil, report, err
	}
	// Reject concurrent canonical changes rather than mix snapshots or trust a stale pass.
	report.Drops["source_mismatch"] += len(graph) - len(out)
	report.Fallback = "snapshot"
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
	report.Fallback = ""
	report.Candidates = len(out)
	report.CandidateSources = map[string]int{}
	for _, c := range out {
		report.CandidateSources[c.Kind]++
	}
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

// Match the compiler's latest-command policy even for checks without criteria.
func retrievalActiveEvidence(d protocol.WorkDetail, stale map[string]bool) []protocol.Evidence {
	newest := map[string]protocol.Evidence{}
	criterion := map[string]string{}
	for _, a := range d.Attempts {
		if a.EvidenceID != "" {
			criterion[a.EvidenceID] = a.CriterionNodeID
		}
	}
	for _, ev := range d.Evidence {
		if ev.Kind == protocol.EvidenceVerificationOutput {
			if prev, ok := newest[ev.SourceURI]; !ok || !ev.ObservedAt.Before(prev.ObservedAt) {
				newest[ev.SourceURI] = ev
			}
		}
	}
	var out []protocol.Evidence
	for _, ev := range work.ActiveEvidence(d) {
		if stale[ev.NodeID] || stale[criterion[ev.ID]] || ev.Kind == protocol.EvidenceVerificationOutput && newest[ev.SourceURI].ID != ev.ID {
			continue
		}
		out = append(out, ev)
	}
	return out
}
