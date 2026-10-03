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

	"github.com/shaktsin/umcode/internal/fingerprint"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/vault"
)

// fingerprintBudget bounds each workspace fingerprint taken by the service.
var fingerprintBudget = 2 * time.Second

// Service records and evaluates works. All methods find the thread's open work
// themselves; with none open, Observe and End do nothing.
type Service struct {
	Store    *store.Store
	Log      *slog.Logger
	Now      func() time.Time // defaults to time.Now().UTC()
	Failures atomic.Int64     // best-effort recording errors

	Vault       *vault.Vault                 // full outputs; nil disables the vault
	VaultDir    string                       // excluded from workspace fingerprints
	ToolVersion func(context.Context) string // identity of the toolchain, optional
	// Workspace takes a workspace fingerprint; nil uses fingerprint.TakeWorkspace.
	Workspace func(ctx context.Context, root string) (fingerprint.Workspace, bool)
}

// Observation is one finished tool call as seen by the engine.
type Observation struct {
	Tool   string          // dotted name, e.g. "file.write"
	Args   json.RawMessage // tool arguments
	Output string          // tool output as the model saw it (possibly clipped)
	Raw    string          // unclipped structured output, preferred over Output when set
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
	content, _ := json.Marshal(map[string]string{"tool": o.Tool, "error": tailText(o.Err, summaryLimit)})
	n, err := s.Store.AddWorkNode(ctx, protocol.WorkNode{WorkID: workID, Kind: protocol.NodeFact, Title: o.Tool + " failed",
		Content: content, Status: "active", ValidFrom: now, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return err
	}
	ev := protocol.Evidence{WorkID: workID, NodeID: n.ID, Kind: protocol.EvidenceToolError,
		SourceURI: o.Tool, Summary: tailText(o.Err, summaryLimit), ObservedAt: now}
	if obj, note := s.putVault(o.Err, now); obj != nil {
		if err := s.Store.UpsertVaultObject(ctx, *obj); err == nil {
			ev.VaultHash, ev.Availability = obj.Hash, protocol.AvailAvailable
		} else {
			s.count("vault index", err)
		}
	} else if note != "" {
		ev.Summary = tailText(o.Err, summaryLimit-len(note)) + note
	}
	_, err = s.Store.AddEvidence(ctx, ev)
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

const notRetained = "\n[full output not retained]"

// putVault stores text in the vault. It returns the index row, or a note for the
// summary when a configured vault could not keep the text; failures are counted.
func (s *Service) putVault(text string, at time.Time) (*protocol.VaultObjectRow, string) {
	if s.Vault == nil || text == "" {
		return nil, ""
	}
	o, err := s.Vault.Put([]byte(text))
	if err != nil {
		s.count("vault put", err)
		return nil, notRetained
	}
	return &protocol.VaultObjectRow{Hash: o.Hash, Class: o.Class, Status: "available", Size: o.Size,
		OriginalSize: o.OriginalSize, Truncated: o.Truncated, CreatedAt: at, LastReferencedAt: at}, ""
}

// workspace takes a fingerprint within its budget; false means "skip, invalidate nothing".
func (s *Service) workspace(ctx context.Context, root string) (fingerprint.Workspace, bool) {
	if root == "" {
		return fingerprint.Workspace{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, fingerprintBudget)
	defer cancel()
	if s.Workspace != nil {
		return s.Workspace(ctx, root)
	}
	return fingerprint.TakeWorkspace(ctx, root, s.VaultDir)
}

// recordAttempt stores the full output in the vault, then writes evidence, the
// append-only attempt, the workspace fingerprint and the criterion update in one
// transaction.
func (s *Service) recordAttempt(ctx context.Context, d protocol.WorkDetail, o Observation, checkType, command, status string, exit *int, full string) error {
	now := s.now()
	crit := criterionFor(d, command)
	critID := ""
	if crit != nil {
		critID = crit.ID
	}
	obj, note := s.putVault(full, now)
	summary := tailText(full, summaryLimit)
	if note != "" {
		summary = tailText(full, summaryLimit-len(note)) + note
	}
	ev := protocol.Evidence{WorkID: d.Work.ID, NodeID: critID, Kind: protocol.EvidenceVerificationOutput, SourceURI: command,
		Summary: summary, ObservedAt: now, EnvFingerprint: fingerprint.Environment(ctx, o.Root, s.ToolVersion)}
	if obj != nil {
		ev.VaultHash, ev.Availability = obj.Hash, protocol.AvailAvailable
	}
	if exit == nil && (status == protocol.AttemptPassed || status == protocol.AttemptFailed) {
		zero := 0
		exit = &zero
	}
	in := store.RecordAttemptInput{
		Attempt: protocol.VerificationAttempt{WorkID: d.Work.ID, CriterionNodeID: critID, CheckType: checkType, Command: command,
			Status: status, ExitCode: exit, StartedAt: now, FinishedAt: now},
		Evidence: ev, Object: obj,
	}
	if w, ok := s.workspace(ctx, o.Root); ok {
		in.Fingerprint = &protocol.Fingerprint{WorkID: d.Work.ID, Kind: protocol.FingerprintVerification, Value: w.Value, Paths: w.Paths, TakenAt: now}
	}
	if crit != nil && status != protocol.AttemptNotRun {
		in.Criterion = &store.CriterionUpdate{NodeID: crit.ID, Status: status, Revision: crit.Revision, At: now}
	}
	_, err := s.Store.RecordAttempt(ctx, in)
	return err
}

func (o Observation) structured() string {
	if o.Raw != "" {
		return o.Raw
	}
	return o.Output
}

func (s *Service) recordRun(ctx context.Context, d protocol.WorkDetail, o Observation) error {
	for _, r := range parseRun(o.structured()) {
		full := r.Output
		if full == "" {
			full = r.Error
		}
		if err := s.recordAttempt(ctx, d, o, "command", r.Command, r.Status, r.ExitCode, full); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) recordBrowser(ctx context.Context, d protocol.WorkDetail, o Observation) error {
	r, ok := parseBrowser(o.structured())
	if !ok {
		return nil
	}
	full := r.Output
	if full == "" {
		full = r.Reason
	}
	return s.recordAttempt(ctx, d, o, "browser", r.Command, r.Status, r.ExitCode, full)
}

// End finishes a turn for the thread's open work: it fingerprints the workspace
// (recording shell-made changes), settles staleness, and closes the work as
// completed only when the turn completed, was not paused, and no criterion is
// unresolved. root is the turn's project root ("" without a project).
func (s *Service) End(ctx context.Context, threadID, turnStatus string, paused bool, root string) error {
	return s.count("end", s.end(ctx, threadID, turnStatus, paused, root))
}

func (s *Service) end(ctx context.Context, threadID, turnStatus string, paused bool, root string) error {
	w, ok, err := s.Store.OpenWorkForThread(ctx, threadID)
	if err != nil || !ok {
		return err
	}
	d, err := s.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return err
	}
	if err := s.turnEndFingerprint(ctx, d, root); err != nil {
		s.count("fingerprint", err)
	}
	if d, err = s.Store.GetWorkDetail(ctx, w.ID); err != nil {
		return err
	}
	if err := s.settle(ctx, d, root); err != nil {
		s.count("staleness", err)
	}
	if turnStatus != protocol.TurnCompleted || paused {
		return nil
	}
	if d, err = s.Store.GetWorkDetail(ctx, w.ID); err != nil {
		return err
	}
	if len(Unresolved(d)) > 0 {
		return nil
	}
	return s.Store.CloseWork(ctx, w.ID, protocol.WorkCompleted, s.now())
}

// turnEndFingerprint stores the turn-end workspace fingerprint. When it differs
// from the previous one and the work has attempts, the changed paths (every path
// involved, or "." when only HEAD moved) become file_change evidence.
func (s *Service) turnEndFingerprint(ctx context.Context, d protocol.WorkDetail, root string) error {
	ws, ok := s.workspace(ctx, root)
	if !ok {
		return nil
	}
	now := s.now()
	var prev *protocol.Fingerprint
	for i := range d.Fingerprints {
		if prev == nil || !d.Fingerprints[i].TakenAt.Before(prev.TakenAt) {
			prev = &d.Fingerprints[i]
		}
	}
	if prev != nil && prev.Value != ws.Value && len(d.Attempts) > 0 {
		seen := map[string]bool{}
		var paths []string
		for _, p := range append(append([]string{}, prev.Paths...), ws.Paths...) {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			paths = []string{"."}
		}
		for _, p := range paths {
			if _, err := s.Store.AddEvidence(ctx, protocol.Evidence{WorkID: d.Work.ID, Kind: protocol.EvidenceFileChange,
				SourceURI: p, Summary: "workspace", ObservedAt: now}); err != nil {
				return err
			}
		}
	}
	_, err := s.Store.AddFingerprint(ctx, protocol.Fingerprint{WorkID: d.Work.ID, Kind: protocol.FingerprintTurnEnd,
		Value: ws.Value, Paths: ws.Paths, TakenAt: now})
	return err
}

// settle writes staleness back: stale criteria get status stale, and evidence of
// superseded attempts, stale criteria and superseded criteria gets stale_at.
func (s *Service) settle(ctx context.Context, d protocol.WorkDetail, root string) error {
	now := s.now()
	stale := Staleness(d, fingerprint.Environment(ctx, root, s.ToolVersion))
	superseded := map[string]bool{}
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeCriterion {
			continue
		}
		if n.Status == StatusSuperseded {
			superseded[n.ID] = true
		}
		if stale[n.ID] && n.Status != protocol.StatusStale {
			if err := s.Store.UpdateWorkNode(ctx, n.ID, protocol.StatusStale, n.Revision, now); err != nil {
				return err
			}
		}
	}
	latest := latestAttempts(d)
	var ids []string
	for _, a := range d.Attempts {
		if a.EvidenceID != "" && a.CriterionNodeID != "" && (latest[a.CriterionNodeID].ID != a.ID || stale[a.CriterionNodeID]) {
			ids = append(ids, a.EvidenceID)
		}
	}
	for _, e := range d.Evidence {
		if superseded[e.NodeID] {
			ids = append(ids, e.ID)
		}
	}
	return s.Store.MarkEvidenceStale(ctx, ids, now)
}
