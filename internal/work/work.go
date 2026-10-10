// Package work records what the engine can observe about a unit of user work:
// its goal, verification criteria, changed artifacts, verification attempts and
// tool failures. Recording is best-effort and never changes engine behavior.
package work

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	DesignedWorkflow bool // deterministic graph semantics; false preserves legacy behavior
	Store            *store.Store
	Log              *slog.Logger
	Now              func() time.Time // defaults to time.Now().UTC()
	Failures         atomic.Int64     // best-effort recording errors

	Vault       *vault.Vault                 // full outputs; nil disables the vault
	VaultDir    string                       // excluded from workspace fingerprints
	ToolVersion func(context.Context) string // identity of the toolchain, optional
	// Workspace takes a workspace fingerprint; nil uses fingerprint.TakeWorkspace.
	Workspace func(ctx context.Context, root string) (fingerprint.Workspace, bool)
}

// Update prepares and applies a semantic batch for the calling thread's current
// open work. Rationale is transient; diagnostics contain compact metadata only.
func (s *Service) Update(ctx context.Context, threadID string, req protocol.WorkUpdateRequest) (result protocol.WorkUpdateResult, gates []protocol.WorkflowGate, err error) {
	defer func() {
		if err == nil {
			if s.Log != nil {
				s.Log.Info("work graph updated", "work_id", req.WorkID, "revision", result.Revision, "created", result.Created, "transitioned", result.Transitioned, "linked", result.Linked)
			}
			return
		}
		s.Failures.Add(1)
		if s.Log != nil {
			class := "storage"
			var validation *ValidationError
			if errors.As(err, &validation) {
				class = validation.Code
			} else if errors.Is(err, store.ErrWorkUpdateConflict) {
				class = "stale"
			}
			// Malformed request IDs may themselves contain secrets.
			args := []any{"revision", req.ExpectedRevision, "rejection", class}
			if validID(req.WorkID) && !containsSecret(req.WorkID) {
				args = append(args, "work_id", req.WorkID)
			}
			s.Log.Warn("work graph update rejected", args...)
		}
	}()
	w, ok, err := s.Store.OpenWorkForThread(ctx, threadID)
	if err != nil {
		return result, nil, err
	}
	if !ok || w.ID != req.WorkID {
		return result, nil, invalid("work_id", "inactive")
	}
	detail, err := s.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return result, nil, err
	}
	// Enforce bounds on the original bytes as well as the sanitized projection.
	// A long secret must not become a valid oversized request by being masked.
	if err := updateInputBounds(req); err != nil {
		return result, nil, err
	}
	// Copy slices and raw content so redaction never modifies caller-owned data.
	clean := req
	clean.Nodes = append([]protocol.WorkNodeChange(nil), req.Nodes...)
	clean.Edges = append([]protocol.WorkEdgeChange(nil), req.Edges...)
	for i, node := range req.Nodes {
		clean.Nodes[i].Content = append(json.RawMessage(nil), node.Content...)
		clean.Nodes[i].EvidenceIDs = append([]string(nil), node.EvidenceIDs...)
		// Reject secret candidates before redaction can mask the signal used by
		// candidate validation. Other semantic prose is safely redacted.
		if node.Kind == protocol.NodeMemoryCandidate {
			if containsSecret(node.Title) || containsSecret(node.Content) {
				return result, nil, invalid("nodes.candidate.text", "invalid")
			}
		}
		clean.Nodes[i].Title = redactText(node.Title)
		if len(node.Content) != 0 {
			clean.Nodes[i].Content, err = redactNodeContent(node.Content)
			if err != nil {
				return result, nil, invalid("nodes.content", "invalid")
			}
		}
	}
	prepared, err := PrepareUpdate(detail, clean, s.now())
	if err != nil {
		return result, nil, err
	}
	result, err = s.Store.ApplyWorkUpdate(ctx, prepared)
	if err != nil {
		return result, nil, err
	}
	return result, prepared.Gates, nil
}

func updateInputBounds(req protocol.WorkUpdateRequest) error {
	if len(req.Nodes) > MaxNodeChanges || len(req.Edges) > MaxEdgeChanges || len(req.Rationale) > MaxRationaleBytes {
		return invalid("request", "limit")
	}
	for _, node := range req.Nodes {
		if len(node.Ref) > MaxClientRefBytes || len(node.Title) > MaxNodeTitleBytes || len(node.Content) > MaxNodeContentBytes || len(node.EvidenceIDs) > MaxNodeEvidenceIDs {
			return invalid("nodes", "limit")
		}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return invalid("request", "invalid")
	}
	if len(raw) > MaxWorkUpdateBytes {
		return invalid("request", "limit")
	}
	return nil
}

// redactNodeContent walks JSON tokens with credential-field context, preserving
// duplicate object keys for semantic rejection and exact noncredential numbers.
func redactNodeContent(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out bytes.Buffer
	var value func(bool) error
	value = func(credential bool) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return errors.New("invalid JSON value")
			}
			out.WriteByte(byte(delim))
			first := true
			for decoder.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				childCredential := credential
				if delim == '{' {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					text, ok := key.(string)
					if !ok {
						return errors.New("invalid JSON key")
					}
					childCredential = credential || credentialField(text) || containsSecret(text)
					encoded, _ := json.Marshal(redactText(text))
					out.Write(encoded)
					out.WriteByte(':')
				}
				if err := value(childCredential); err != nil {
					return err
				}
			}
			close, err := decoder.Token()
			if err != nil {
				return err
			}
			want := json.Delim(']')
			if delim == '{' {
				want = '}'
			}
			if close != want {
				return errors.New("invalid JSON delimiter")
			}
			out.WriteByte(byte(want))
			return nil
		}
		if credential && token != nil {
			token = "[REDACTED]"
		} else if text, ok := token.(string); ok {
			token = redactText(text)
		}
		encoded, err := json.Marshal(token)
		if err != nil {
			return err
		}
		out.Write(encoded)
		return nil
	}
	if err := value(false); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON content")
	}
	return out.Bytes(), nil
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

	fp *obsFingerprints // shared by the results of one observation
}

// redactText masks secrets in text that is persisted outside the vault.
func redactText(s string) string {
	b, _ := vault.Redact([]byte(s))
	return string(b)
}

// maxWorkspacePaths bounds the file_change rows one turn-end change may add;
// beyond it the change is recorded workspace-wide ("." ).
const maxWorkspacePaths = 50

// obsFingerprints caches the workspace and environment fingerprints so one
// observation (which may hold several results) takes them once.
type obsFingerprints struct {
	done bool
	ws   fingerprint.Workspace
	ok   bool
	env  string
}

func (s *Service) snapshot(ctx context.Context, o Observation) (fingerprint.Workspace, bool, string) {
	f := o.fp
	if f == nil {
		f = &obsFingerprints{}
	}
	if !f.done {
		f.ws, f.ok = s.workspace(ctx, o.Root)
		f.env = fingerprint.Environment(ctx, o.Root, s.ToolVersion)
		f.done = true
	}
	return f.ws, f.ok, f.env
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
	if w, ok, err := s.Store.OpenWorkForThread(ctx, th.ID); err != nil || ok {
		if err == nil && ok && s.DesignedWorkflow {
			depth := MaxDepth(w.WorkflowDepth, InitialDepth(text))
			if depth != w.WorkflowDepth {
				return s.Store.SetWorkDepth(ctx, w.ID, depth)
			}
		}
		return err
	}
	goal := capText(redactText(text), goalLimit)
	now := s.now()
	depth := ""
	if s.DesignedWorkflow {
		depth = InitialDepth(text)
	}
	w, err := s.Store.CreateWork(ctx, protocol.Work{ThreadID: th.ID, ProjectID: th.ProjectID, Goal: goal, CreatedAt: now, WorkflowDepth: depth})
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
	o.fp = &obsFingerprints{}
	w, ok, err := s.Store.OpenWorkForThread(ctx, threadID)
	if err != nil || !ok {
		return err
	}
	if s.DesignedWorkflow {
		d, err := s.Store.GetWorkDetail(ctx, w.ID)
		if err != nil {
			return err
		}
		depth := ObservedDepth(w.WorkflowDepth, d, o)
		if depth != w.WorkflowDepth {
			if err := s.Store.SetWorkDepth(ctx, w.ID, depth); err != nil {
				return err
			}
		}
	} else if (o.Err == "" || isVerificationTool(o.Tool)) && EscalatesToGuided(o.Tool, o.Risk) && w.WorkflowDepth != protocol.DepthGuided {
		if err := s.Store.SetWorkDepth(ctx, w.ID, protocol.DepthGuided); err != nil {
			return err
		}
	}
	if o.Err != "" {
		return s.recordFailure(ctx, w.ID, o)
	}
	if s.DesignedWorkflow && discoveryTool(o.Tool) {
		// Only engine-observed successful calls can create discovery provenance.
		// No semantic-client assertions are accepted as observations.
		body := redactText(o.structured())
		hash := sha256.Sum256([]byte(body))
		argsHash := sha256.Sum256(o.Args)
		if _, err := s.Store.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, Kind: protocol.EvidenceDiscovery, SourceURI: o.Tool, SourceRevision: hex.EncodeToString(argsHash[:]), ContentHash: hex.EncodeToString(hash[:]), Summary: capText(body, summaryLimit), ObservedAt: s.now()}); err != nil {
			return err
		}
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

func discoveryTool(name string) bool {
	switch name {
	case "file.read", "file.list", "file.search", "web.search", "web.fetch", "verification.plan", "computer.list", "computer.inspect", "visual.inspect":
		return true
	}
	return false
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
	if obj, note := s.putVault(ctx, o.Err, now); obj != nil {
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
	for i := range checks {
		checks[i].Command = redactText(checks[i].Command)
		checks[i].Directory = redactText(checks[i].Directory)
		checks[i].Reason = redactText(checks[i].Reason)
		checks[i].Label = redactText(checks[i].Label)
	}
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
func (s *Service) putVault(ctx context.Context, text string, at time.Time) (*protocol.VaultObjectRow, string) {
	if s.Vault == nil || text == "" {
		return nil, ""
	}
	o, err := s.Vault.Put([]byte(text))
	if err != nil {
		s.count("vault put", err)
		return nil, notRetained
	}
	row := protocol.VaultObjectRow{Hash: o.Hash, Class: o.Class, Status: "available", Size: o.Size,
		OriginalSize: o.OriginalSize, Truncated: o.Truncated, CreatedAt: at, LastReferencedAt: at}
	// Refresh the index row now so a concurrent GC pass sees the object as in use
	// even when this put deduplicated onto an existing file.
	s.count("vault index", s.Store.UpsertVaultObject(ctx, row))
	return &row, ""
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
	command = redactText(command)
	crit := criterionFor(d, command)
	critID := ""
	if crit != nil {
		critID = crit.ID
	}
	obj, note := s.putVault(ctx, full, now)
	summary := tailText(full, summaryLimit)
	if note != "" {
		summary = tailText(full, summaryLimit-len(note)) + note
	}
	ev := protocol.Evidence{WorkID: d.Work.ID, NodeID: critID, Kind: protocol.EvidenceVerificationOutput, SourceURI: command,
		Summary: summary, ObservedAt: now}
	ws, wsOK, env := s.snapshot(ctx, o)
	ev.EnvFingerprint = env
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
	if wsOK {
		in.Fingerprint = &protocol.Fingerprint{WorkID: d.Work.ID, Kind: protocol.FingerprintVerification, Value: ws.Value, Paths: ws.Paths, TakenAt: now}
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
// unresolved. It returns the ID only when this call newly completes the Work.
// root is the turn's project root ("" without a project).
func (s *Service) End(ctx context.Context, threadID, turnStatus string, paused bool, root string) (completedWorkID string, err error) {
	id, err := s.end(ctx, threadID, turnStatus, paused, root)
	return id, s.count("end", err)
}

func (s *Service) end(ctx context.Context, threadID, turnStatus string, paused bool, root string) (string, error) {
	w, ok, err := s.Store.OpenWorkForThread(ctx, threadID)
	if err != nil || !ok {
		return "", err
	}
	d, err := s.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		return "", err
	}
	if err := s.turnEndFingerprint(ctx, d, root); err != nil {
		s.count("fingerprint", err)
	}
	if d, err = s.Store.GetWorkDetail(ctx, w.ID); err != nil {
		return "", err
	}
	if err := s.settle(ctx, d, root); err != nil {
		s.count("staleness", err)
	}
	if turnStatus != protocol.TurnCompleted || paused {
		return "", nil
	}
	if d, err = s.Store.GetWorkDetail(ctx, w.ID); err != nil {
		return "", err
	}
	blockers := Unresolved(d)
	if s.DesignedWorkflow && d.Work.WorkflowDepth != protocol.DepthDirect {
		blockers = CompletionBlockers(d)
	}
	if len(blockers) > 0 {
		return "", nil
	}
	if err := s.Store.CloseWork(ctx, w.ID, protocol.WorkCompleted, s.now()); err != nil {
		return "", err
	}
	return w.ID, nil
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
	// A verification whose own fingerprint was skipped is newer than prev and
	// cannot be judged against it, so nothing is invalidated.
	skipped := false
	for _, a := range d.Attempts {
		if a.FingerprintID == "" && prev != nil && a.FinishedAt.After(prev.TakenAt) {
			skipped = true
		}
	}
	if prev != nil && prev.Value != ws.Value && len(d.Attempts) > 0 && !skipped {
		seen := map[string]bool{}
		var paths []string
		for _, p := range append(append([]string{}, prev.Paths...), ws.Paths...) {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 || len(paths) > maxWorkspacePaths {
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
