// Package work records what the engine can observe about a unit of user work:
// its goal, verification criteria, changed artifacts, verification attempts and
// tool failures. Recording is best-effort and never changes engine behavior.
package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// Service records and evaluates works. All methods find the thread's open work
// themselves; with none open, Observe and End do nothing.
type Service struct {
	Store    *store.Store
	Log      *slog.Logger
	Now      func() time.Time // defaults to time.Now().UTC()
	Failures atomic.Int64     // best-effort recording errors
}

// Observation is one finished tool call as seen by the engine.
type Observation struct {
	Tool   string          // dotted name, e.g. "file.write"
	Args   json.RawMessage // tool arguments
	Output string          // tool output
	Err    string          // non-empty when the tool failed or was denied
	Risk   string          // "green", "yellow" or "red"
	Root   string          // project root, "" without a project
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) count(op string, err error) error {
	if err != nil {
		s.Failures.Add(1)
		if s.Log != nil {
			s.Log.Warn("work recording failed", "op", op, "err", err)
		}
	}
	return err
}

// Begin opens a work for the thread, or continues its open one.
func (s *Service) Begin(ctx context.Context, th protocol.Thread, text string) error {
	return s.count("begin", s.begin(ctx, th, text))
}

func (s *Service) begin(ctx context.Context, th protocol.Thread, text string) error {
	if _, ok, err := s.Store.OpenWorkForThread(ctx, th.ID); err != nil || ok {
		return err
	}
	goal := capText(text, goalLimit)
	now := s.now()
	w, err := s.Store.CreateWork(ctx, protocol.Work{ThreadID: th.ID, ProjectID: th.ProjectID, Goal: goal, CreatedAt: now})
	if err != nil {
		return err
	}
	_, err = s.Store.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeGoal, Title: goal, Status: "active",
		ValidFrom: now, CreatedAt: now, UpdatedAt: now})
	return err
}

// Observe records one finished tool call.
func (s *Service) Observe(ctx context.Context, threadID string, o Observation) error {
	return s.count("observe", s.observe(ctx, threadID, o))
}

func (s *Service) observe(ctx context.Context, threadID string, o Observation) error {
	w, ok, err := s.Store.OpenWorkForThread(ctx, threadID)
	if err != nil || !ok {
		return err
	}
	if (o.Err == "" || isVerificationTool(o.Tool)) && EscalatesToGuided(o.Tool, o.Risk) && w.WorkflowDepth != protocol.DepthGuided {
		if err := s.Store.SetWorkDepth(ctx, w.ID, protocol.DepthGuided); err != nil {
			return err
		}
	}
	if o.Err != "" {
		return s.recordFailure(ctx, w.ID, o)
	}
	switch o.Tool {
	case "file.write", "file.edit", "verification.plan", "verification.run", "browser.verify":
	default:
		return nil
	}
	d, err := s.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return err
	}
	switch o.Tool {
	case "file.write", "file.edit":
		return s.recordFileChange(ctx, d, o)
	case "verification.plan":
		return s.recordPlan(ctx, d, o)
	case "verification.run":
		return s.recordRun(ctx, d, o)
	case "browser.verify":
		return s.recordBrowser(ctx, d, o)
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, workID string, o Observation) error {
	now := s.now()
	content, _ := json.Marshal(map[string]string{"tool": o.Tool, "error": capText(o.Err, summaryLimit)})
	n, err := s.Store.AddWorkNode(ctx, protocol.WorkNode{WorkID: workID, Kind: protocol.NodeFact, Title: o.Tool + " failed",
		Content: content, Status: "active", ValidFrom: now, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return err
	}
	_, err = s.Store.AddEvidence(ctx, protocol.Evidence{WorkID: workID, NodeID: n.ID, Kind: protocol.EvidenceToolError,
		SourceURI: o.Tool, Summary: capText(o.Err, summaryLimit), ObservedAt: now})
	return err
}

func goalNode(d protocol.WorkDetail) (protocol.WorkNode, bool) {
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeGoal {
			return n, true
		}
	}
	return protocol.WorkNode{}, false
}

func (s *Service) recordFileChange(ctx context.Context, d protocol.WorkDetail, o Observation) error {
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(o.Args, &a) != nil || strings.TrimSpace(a.Path) == "" {
		return nil
	}
	rel, full := normalizePath(o.Root, a.Path)
	a.Path = rel
	now := s.now()
	var art *protocol.WorkNode
	for i := range d.Nodes {
		if d.Nodes[i].Kind == protocol.NodeArtifact && d.Nodes[i].Title == a.Path {
			art = &d.Nodes[i]
			break
		}
	}
	if art == nil {
		n, err := s.Store.AddWorkNode(ctx, protocol.WorkNode{WorkID: d.Work.ID, Kind: protocol.NodeArtifact, Title: a.Path,
			Status: "active", ValidFrom: now, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		art = &n
		if g, ok := goalNode(d); ok {
			if err := s.Store.AddWorkEdge(ctx, protocol.WorkEdge{WorkID: d.Work.ID, FromNodeID: n.ID, Relation: protocol.RelServes, ToNodeID: g.ID}); err != nil {
				return err
			}
		}
	} else if err := s.Store.UpdateWorkNode(ctx, art.ID, "active", art.Revision+1, now); err != nil {
		return err
	}
	_, err := s.Store.AddEvidence(ctx, protocol.Evidence{WorkID: d.Work.ID, NodeID: art.ID, Kind: protocol.EvidenceFileChange,
		SourceURI: a.Path, ContentHash: fileHash(full), Summary: o.Tool, ObservedAt: now})
	return err
}

// normalizePath returns the project-relative, slash-separated spelling of a
// tool path and the absolute path to read. Paths that resolve outside root
// (or any path when there is no root) keep their cleaned spelling and have no
// absolute path, so they are never hashed.
func normalizePath(root, path string) (rel, full string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if root == "" {
		return filepath.ToSlash(path), ""
	}
	full = path
	if !filepath.IsAbs(path) {
		full = filepath.Join(root, path)
	}
	r, err := filepath.Rel(root, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(path), ""
	}
	return filepath.ToSlash(r), full
}

// fileHash returns the hex SHA-256 of a file, or "" when there is no path or it cannot be read.
func fileHash(full string) string {
	if full == "" {
		return ""
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Service) recordPlan(ctx context.Context, d protocol.WorkDetail, o Observation) error {
	known := map[string]bool{}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeCriterion && n.Status != StatusSuperseded {
			known[criterionCommand(n)] = true
		}
	}
	goal, hasGoal := goalNode(d)
	checks := parsePlan(o.Output)
	if len(checks) > 0 {
		// A new plan replaces the old one: criteria it no longer lists stop counting.
		planned := map[string]bool{}
		for _, c := range checks {
			planned[c.Command] = true
		}
		for _, n := range d.Nodes {
			if n.Kind == protocol.NodeCriterion && n.Status != StatusSuperseded && !planned[criterionCommand(n)] {
				if err := s.Store.UpdateWorkNode(ctx, n.ID, StatusSuperseded, n.Revision, s.now()); err != nil {
					return err
				}
			}
		}
	}
	for _, c := range checks {
		if known[c.Command] {
			continue
		}
		known[c.Command] = true
		now := s.now()
		content, _ := json.Marshal(map[string]string{"command": c.Command, "directory": c.Directory, "reason": c.Reason})
		title := c.Label
		if title == "" {
			title = c.Command
		}
		n, err := s.Store.AddWorkNode(ctx, protocol.WorkNode{WorkID: d.Work.ID, Kind: protocol.NodeCriterion, Title: title,
			Content: content, Status: "pending", ValidFrom: now, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		if hasGoal {
			if err := s.Store.AddWorkEdge(ctx, protocol.WorkEdge{WorkID: d.Work.ID, FromNodeID: goal.ID, Relation: protocol.RelRequires, ToNodeID: n.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}

// criterionFor finds the criterion whose planned command equals command.
func criterionFor(d protocol.WorkDetail, command string) *protocol.WorkNode {
	for i := range d.Nodes {
		if d.Nodes[i].Kind == protocol.NodeCriterion && d.Nodes[i].Status != StatusSuperseded && criterionCommand(d.Nodes[i]) == command {
			return &d.Nodes[i]
		}
	}
	return nil
}

// recordAttempt stores evidence and an append-only attempt, and updates the matched criterion.
func (s *Service) recordAttempt(ctx context.Context, d protocol.WorkDetail, checkType, command, status string, exit *int, summary string) error {
	now := s.now()
	crit := criterionFor(d, command)
	critID := ""
	if crit != nil {
		critID = crit.ID
	}
	ev, err := s.Store.AddEvidence(ctx, protocol.Evidence{WorkID: d.Work.ID, NodeID: critID, Kind: protocol.EvidenceVerificationOutput,
		SourceURI: command, Summary: capText(summary, summaryLimit), ObservedAt: now})
	if err != nil {
		return err
	}
	if exit == nil && (status == protocol.AttemptPassed || status == protocol.AttemptFailed) {
		zero := 0
		exit = &zero
	}
	if _, err := s.Store.AddVerificationAttempt(ctx, protocol.VerificationAttempt{WorkID: d.Work.ID, CriterionNodeID: critID,
		CheckType: checkType, Command: command, Status: status, ExitCode: exit, EvidenceID: ev.ID, StartedAt: now, FinishedAt: now}); err != nil {
		return err
	}
	if crit != nil && status != protocol.AttemptNotRun {
		return s.Store.UpdateWorkNode(ctx, crit.ID, status, crit.Revision, now)
	}
	return nil
}

func (s *Service) recordRun(ctx context.Context, d protocol.WorkDetail, o Observation) error {
	for _, r := range parseRun(o.Output) {
		summary := r.Output
		if summary == "" {
			summary = r.Error
		}
		if err := s.recordAttempt(ctx, d, "command", r.Command, r.Status, r.ExitCode, summary); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) recordBrowser(ctx context.Context, d protocol.WorkDetail, o Observation) error {
	r, ok := parseBrowser(o.Output)
	if !ok {
		return nil
	}
	summary := r.Output
	if summary == "" {
		summary = r.Reason
	}
	return s.recordAttempt(ctx, d, "browser", r.Command, r.Status, r.ExitCode, summary)
}

// End evaluates the thread's open work when a turn finishes. The work closes as
// completed only when the turn completed, was not paused, and no criterion is
// unresolved; otherwise it stays open for the next turn.
func (s *Service) End(ctx context.Context, threadID, turnStatus string, paused bool) error {
	return s.count("end", s.end(ctx, threadID, turnStatus, paused))
}

func (s *Service) end(ctx context.Context, threadID, turnStatus string, paused bool) error {
	w, ok, err := s.Store.OpenWorkForThread(ctx, threadID)
	if err != nil || !ok {
		return err
	}
	if turnStatus != protocol.TurnCompleted || paused {
		return nil
	}
	d, err := s.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return err
	}
	if len(Unresolved(d)) > 0 {
		return nil
	}
	return s.Store.CloseWork(ctx, w.ID, protocol.WorkCompleted, s.now())
}
