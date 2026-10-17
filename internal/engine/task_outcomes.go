package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/work"
)

type taskFileProof struct {
	Path      string   `json:"path"`
	SHA256    string   `json:"sha256"`
	Bytes     int      `json:"bytes"`
	Satisfies []string `json:"satisfies"`
}
type taskOpaqueResolution struct {
	OperationID      string   `json:"operationId"`
	ExpectedRevision int      `json:"expectedRevision"`
	ApprovalID       string   `json:"approvalId"`
	EvidenceIDs      []string `json:"evidenceIds"`
}
type taskItemData struct {
	TaskResolution *taskOpaqueResolution   `json:"taskResolution,omitempty"`
	EngineSource   string                  `json:"engineToolSource,omitempty"`
	FileProofs     []taskFileProof         `json:"fileProofs,omitempty"`
	ActionFailure  *protocol.ActionFailure `json:"actionFailure,omitempty"`
}

func taskItemMetadata(item protocol.Item) taskItemData {
	var d taskItemData
	_ = json.Unmarshal(item.Data, &d)
	return d
}
func mergeTaskItemData(item *protocol.Item, key string, value any) {
	data := map[string]json.RawMessage{}
	_ = json.Unmarshal(item.Data, &data)
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	data[key] = raw
	item.Data, _ = json.Marshal(data)
}
func (e *Engine) operationArgs(ctx context.Context, op protocol.TaskOperation) (json.RawMessage, error) {
	if op.ArgsRef == "" {
		return op.Args, nil
	}
	if op.ArgsRef != op.ItemID {
		return nil, errors.New("operation argument reference mismatch")
	}
	item, err := e.Store.GetItem(ctx, op.ArgsRef)
	if err != nil {
		return nil, err
	}
	if item.Tool == nil || item.Tool.Name != op.Tool {
		return nil, errors.New("original tool arguments unavailable")
	}
	var raw bytes.Buffer
	if err := json.Compact(&raw, item.Tool.Args); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw.Bytes())
	if hex.EncodeToString(hash[:]) != op.ArgsSHA256 {
		return nil, errors.New("original tool arguments changed")
	}
	return item.Tool.Args, nil
}
func taskFileTarget(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct{ Path, Workspace string }
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Path == "" || a.Workspace != "" && a.Workspace != "project" {
		return "", errors.New("project file target required")
	}
	scope := tools.ScopeFrom(ctx)
	if scope == nil {
		return "", errors.New("project scope required for file evidence")
	}
	abs, err := scope.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	return pathutil.Resolved(abs), nil
}
func fileOutcomeSatisfied(op protocol.TaskOperation, body string, hash string) bool {
	switch op.Tool {
	case "file.write":
		var args struct{ Content string }
		if json.Unmarshal(op.Args, &args) != nil {
			return false
		}
		expected := sha256.Sum256([]byte(args.Content))
		return hex.EncodeToString(expected[:]) == hash
	case "file.edit":
		var args struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		if json.Unmarshal(op.Args, &args) != nil {
			return false
		}
		if args.Old == "" {
			return body == args.New
		}
		if args.New == "" {
			return !strings.Contains(body, args.Old)
		}
		if !strings.Contains(body, args.New) {
			return false
		}
		if strings.Contains(args.New, args.Old) {
			return !strings.Contains(strings.ReplaceAll(body, args.New, ""), args.Old)
		}
		return !strings.Contains(body, args.Old)
	}
	return false
}
func (e *Engine) attachTaskFileProof(ctx context.Context, taskID string, observation tools.FileReadEvidence, item *protocol.Item) error {
	task, err := e.Store.GetExecutionTask(ctx, taskID)
	if err != nil {
		return err
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return err
	}
	if snap.Task.ID != taskID {
		return errors.New("task changed before file verification")
	}
	proof := taskFileProof{Path: pathutil.Resolved(observation.Path), SHA256: observation.SHA256, Bytes: observation.Bytes}
	for _, op := range snap.Operations {
		if op.Tool != "file.write" && op.Tool != "file.edit" {
			continue
		}
		prior, err := e.Store.GetItem(ctx, op.ItemID)
		if err != nil || prior.Seq >= item.Seq {
			continue
		}
		if taskItemMetadata(prior).EngineSource != "builtin_file_mutation" && op.ImplementationID != "legacy_unverified" {
			continue
		}
		args, argsErr := e.operationArgs(ctx, op)
		if argsErr != nil {
			return argsErr
		}
		op.Args = args
		target, err := taskFileTarget(ctx, op.Args)
		if err != nil || target != proof.Path {
			continue
		}
		if fileOutcomeSatisfied(op, observation.Body, observation.SHA256) {
			proof.Satisfies = append(proof.Satisfies, op.ID)
		}
	}
	d := taskItemMetadata(*item)
	d.FileProofs = append(d.FileProofs, proof)
	mergeTaskItemData(item, "fileProofs", d.FileProofs)
	return nil
}
func (e *Engine) recordTaskOutcome(ctx context.Context, id string, item protocol.Item) error {
	op, err := e.Store.GetTaskOperation(ctx, id)
	if err != nil {
		return err
	}
	status := "outcome_unknown"
	verification := "pending"
	var failure *protocol.ActionFailure
	switch item.Status {
	case protocol.ItemCompleted:
		status = "succeeded"
	case protocol.ItemDenied:
		status = "blocked"
	case protocol.ItemFailed:
		status = "failed"
	}
	d := taskItemMetadata(item)
	if item.Tool != nil && item.Tool.Error != "" {
		f := classifyActionFailure(op.ID, errors.New(item.Tool.Error))
		if d.ActionFailure != nil {
			f = *d.ActionFailure
		}
		f.OperationID = op.ID
		f.Advisory = f.Advisory || workflowDiscoveryTool(op.Tool) && f.Retryable
		f = e.taskReadbackFailure(ctx, op, f)
		f = e.taskFailureState(ctx, op.TaskID, f)
		failure = &f
	}
	if failure != nil && failure.Category == "outcome_unknown" {
		status = "outcome_unknown"
	}
	if status == "succeeded" {
		if workflowDiscoveryTool(op.Tool) || d.EngineSource == "engine_control" {
			verification = "passed"
		}
		if d.EngineSource == "builtin_verification" {
			var report struct {
				Status string `json:"status"`
			}
			if item.Tool == nil || json.Unmarshal([]byte(item.Tool.Output), &report) != nil {
				return errors.New("verification result contract unavailable")
			}
			if report.Status == "passed" {
				verification = "passed"
			} else {
				verification = "failed"
				failure = &protocol.ActionFailure{Category: "verification_failed", OperationID: op.ID, Message: "Verification did not pass: " + report.Status, Retryable: true, NextStep: "Inspect the recorded check results and rerun the failed and affected checks."}
			}
		}
	}
	op, err = e.Store.TransitionTaskOperation(ctx, id, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: status, VerificationStatus: verification, ResultItemID: item.ID, Failure: failure})
	if err != nil {
		return err
	}
	if status != "succeeded" || verification != "passed" {
		return nil
	}
	if d.TaskResolution != nil {
		if err := e.acknowledgeTaskOutcome(ctx, op, *d.TaskResolution); err != nil {
			return err
		}
	}
	for _, proof := range d.FileProofs {
		for _, target := range proof.Satisfies {
			if err := e.verifyTaskOperation(ctx, target, []string{item.ID}, ""); err != nil {
				return err
			}
		}
	}
	// An exact successful retry of a validated control operation resolves its
	// earlier failure without claiming the failed invocation itself executed.
	task, err := e.Store.GetExecutionTask(ctx, op.TaskID)
	if err != nil {
		return err
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return err
	}
	for _, past := range snap.Operations {
		if past.ID == op.ID || past.ResolvedByID != "" || past.Failure == nil || past.Tool != op.Tool {
			continue
		}
		currentArgs, argsErr := e.operationArgs(ctx, op)
		if argsErr != nil {
			return argsErr
		}
		pastArgs, argsErr := e.operationArgs(ctx, past)
		if argsErr != nil {
			return argsErr
		}
		same := workflowActionKey(ctx, op.Tool, currentArgs) == workflowActionKey(ctx, past.Tool, pastArgs)
		if !same && d.EngineSource == "builtin_verification" && op.Tool == "verification.run" {
			prior, lookupErr := e.Store.GetItem(ctx, past.ItemID)
			same = lookupErr == nil && taskItemMetadata(prior).EngineSource == "builtin_verification" && verificationCovers(currentArgs, pastArgs)
		}
		if !same {
			continue
		}
		if _, err := e.Store.TransitionTaskOperation(ctx, past.ID, protocol.OperationTransition{ExpectedRevision: past.Revision, Status: past.Status, VerificationStatus: "passed", EvidenceIDs: []string{item.ID}, ResolvedByID: op.ID, Resolution: "satisfied", Failure: past.Failure}); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) verifyTaskOperation(ctx context.Context, id string, evidenceIDs []string, supersedesID string) error {
	op, err := e.Store.GetTaskOperation(ctx, id)
	if err != nil {
		return err
	}
	if len(evidenceIDs) == 0 {
		return errors.New("actual verification evidence is required")
	}
	for _, evidenceID := range evidenceIDs {
		item, err := e.Store.GetItem(ctx, evidenceID)
		if err != nil {
			return err
		}
		if item.Kind != protocol.ItemToolCall || item.Status != protocol.ItemCompleted || item.Tool == nil {
			return errors.New("verification must reference a completed tool result")
		}
		task, err := e.Store.GetExecutionTask(ctx, op.TaskID)
		if err != nil {
			return err
		}
		if task.ThreadID != item.ThreadID {
			return errors.New("verification is outside this task")
		}
		d := taskItemMetadata(item)
		if d.EngineSource != "builtin_file_read" && d.EngineSource != "builtin_verification" {
			return errors.New("tool result has no trusted verification contract")
		}
		matches := false
		for _, proof := range d.FileProofs {
			for _, target := range proof.Satisfies {
				if target == op.ID {
					matches = true
				}
			}
		}
		if !matches {
			return errors.New("evidence does not verify this operation's requested outcome")
		}
		snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
		if err != nil {
			return err
		}
		var observation protocol.TaskOperation
		for _, candidate := range snap.Operations {
			if candidate.ItemID == item.ID {
				observation = candidate
				break
			}
		}
		if observation.Status != "succeeded" || observation.VerificationStatus != "passed" {
			return errors.New("verification observation is not finalized")
		}
		change := protocol.OperationTransition{ExpectedRevision: op.Revision, Status: op.Status, VerificationStatus: "passed", EvidenceIDs: evidenceIDs, Failure: op.Failure}
		if op.Status != "succeeded" {
			change.ResolvedByID = observation.ID
			change.Resolution = "satisfied"
		}
		op, err = e.Store.TransitionTaskOperation(ctx, op.ID, change)
		if err != nil {
			return err
		}
		if op.Status == "succeeded" && (op.Tool == "file.write" || op.Tool == "file.edit") {
			if err := e.supersedeIntermediateFileEffects(ctx, op, evidenceIDs); err != nil {
				return err
			}
		}
		if supersedesID != "" {
			old, err := e.Store.GetTaskOperation(ctx, supersedesID)
			if err != nil {
				return err
			}
			if old.TaskID != op.TaskID {
				return errors.New("supersession crosses task scope")
			}
			// The same evidence must independently satisfy the old desired outcome.
			return e.verifyTaskOperation(ctx, old.ID, evidenceIDs, "")
		}
	}
	return nil
}
func (e *Engine) reconcileTaskOutcome(ctx context.Context, taskID string) (string, error) {
	task, err := e.Store.GetExecutionTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return "", err
	}
	if snap.Task.ID != taskID {
		return "", errors.New("task changed during reconciliation")
	}
	outcome := "completed"
	status := "completed"
	if len(snap.Operations) == 0 {
		outcome = "no_execution"
		status = "active"
	}
	succeeded := false
	substantive := false
	attemptedEffect := false
	for _, op := range snap.Operations {
		if !workflowDiscoveryTool(op.Tool) {
			attemptedEffect = true
		}
		if op.Status == "succeeded" {
			if !workflowDiscoveryTool(op.Tool) {
				substantive = true
			}
			succeeded = true
		}
		if op.ResolvedByID != "" || e.taskWorkflowConditionCleared(ctx, task.ThreadID, op) {
			continue
		}
		if op.Status == "failed" || op.Status == "blocked" || op.Status == "outcome_unknown" || op.VerificationStatus == "failed" {
			if op.Failure != nil && op.Failure.Advisory {
				continue
			}
			outcome = "blocked"
			status = "blocked"
			break
		}
		if op.Status != "succeeded" || op.VerificationStatus != "passed" {
			outcome = "unverified"
			status = "active"
		}
	}
	if outcome == "completed" && !substantive && succeeded {
		outcome, status = "no_execution", "active"
		if attemptedEffect {
			outcome = "not_executed"
		}
	}
	if outcome == "completed" && !succeeded {
		outcome, status = "not_executed", "active"
	}
	// Successful operations do not close requested work that still lacks evidence.
	if outcome == "completed" {
		for _, expectation := range snap.Expectations {
			if expectation.Kind != "explanation" && expectation.Status == "pending" {
				outcome, status = "unverified", "active"
				break
			}
		}
	}
	if _, childErr := e.Store.ActiveAgentTask(ctx, task.ThreadID, task.ProjectID); childErr == nil && outcome == "completed" {
		outcome, status = "unverified", "active"
	} else if childErr != nil && !errors.Is(childErr, store.ErrNotFound) {
		return "", childErr
	}
	_, err = e.Store.TransitionExecutionTask(ctx, task.ID, protocol.TaskTransition{ExpectedRevision: snap.Task.Revision, Status: status})
	return outcome, err
}
func (e *Engine) refreshTaskRecovery(ctx context.Context, threadID string, recovery *taskRecoveryState) error {
	snap, err := e.Store.ActiveExecutionTask(ctx, threadID)
	if err != nil {
		return err
	}
	for key, failure := range recovery.pending {
		for _, op := range snap.Operations {
			if op.ID == failure.OperationID && op.ResolvedByID != "" {
				delete(recovery.pending, key)
				break
			}
		}
	}
	return nil
}
func (e *Engine) captureVerificationFiles(ctx context.Context, taskID string, item *protocol.Item) error {
	task, err := e.Store.GetExecutionTask(ctx, taskID)
	if err != nil {
		return err
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, op := range snap.Operations {
		if op.Tool != "file.write" && op.Tool != "file.edit" {
			continue
		}
		args, argsErr := e.operationArgs(ctx, op)
		if argsErr != nil {
			return argsErr
		}
		op.Args = args
		path, err := taskFileTarget(ctx, op.Args)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16*1024*1024 {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		hash := sha256.Sum256(body)
		if err := e.attachTaskFileProof(ctx, taskID, tools.FileReadEvidence{Path: filepath.Clean(path), SHA256: hex.EncodeToString(hash[:]), Bytes: len(body), Body: string(body)}, item); err != nil {
			return err
		}
	}
	return nil
}
func taskOutcomeText(outcome string) string {
	if outcome == "not_executed" {
		return "The requested action did not run. The task remains open; inspect the tool error before retrying."
	}
	if outcome == "unverified" {
		return "The actions ran, but verification is incomplete. The task remains open."
	}
	return fmt.Sprintf("Task outcome: %s", outcome)
}

func (e *Engine) taskReadbackFailure(ctx context.Context, operation protocol.TaskOperation, f protocol.ActionFailure) protocol.ActionFailure {
	if operation.Tool != "file.read" || f.Category != "missing_resource" && f.Category != "execution_failed" {
		return f
	}
	target, err := taskFileTarget(ctx, operation.Args)
	if err != nil {
		return f
	}
	task, err := e.Store.GetExecutionTask(ctx, operation.TaskID)
	if err != nil {
		return f
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return f
	}
	for _, op := range snap.Operations {
		if op.Status != "succeeded" || op.Tool != "file.write" && op.Tool != "file.edit" {
			continue
		}
		args, err := e.operationArgs(ctx, op)
		if err != nil {
			continue
		}
		prior, err := taskFileTarget(ctx, args)
		if err == nil && target == prior {
			f.Category = "verification_failed"
			f.Advisory = false
			f.Message = "The file change ran, but read-back verification failed: " + f.Message
			f.NextStep = "Inspect the current file and the recorded change, then retry verification before declaring completion."
			return f
		}
	}
	return f
}

func (e *Engine) acknowledgeTaskOutcome(ctx context.Context, observer protocol.TaskOperation, resolution taskOpaqueResolution) error {
	target, err := e.Store.GetTaskOperation(ctx, resolution.OperationID)
	if err != nil {
		return err
	}
	if target.TaskID != observer.TaskID || target.Revision != resolution.ExpectedRevision || target.Status != "outcome_unknown" || target.ResolvedByID != "" {
		return store.ErrTaskConflict
	}
	task, err := e.Store.GetExecutionTask(ctx, target.TaskID)
	if err != nil {
		return err
	}
	approval, err := e.Store.GetApproval(ctx, resolution.ApprovalID)
	if err != nil {
		return err
	}
	var payload struct {
		Operation protocol.TaskOperation `json:"operation"`
	}
	if approval.Status != "approved" || approval.Kind != "task_interpretation" || approval.ThreadID != task.ThreadID || approval.NodeID != task.ID || approval.ItemID != observer.ItemID || json.Unmarshal(approval.Args, &payload) != nil || payload.Operation.ID != target.ID || payload.Operation.Revision != target.Revision {
		return errors.New("uncertain outcome acknowledgement lacks its bound user approval")
	}
	_, err = e.Store.TransitionTaskOperation(ctx, target.ID, protocol.OperationTransition{ExpectedRevision: target.Revision, Status: target.Status, VerificationStatus: "pending", EvidenceIDs: resolution.EvidenceIDs, ResolvedByID: observer.ID, Resolution: "acknowledged", Failure: target.Failure})
	if err != nil {
		return err
	}
	snapshot, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return err
	}
	for _, past := range snapshot.Operations {
		if past.ID == target.ID || past.ResolvedByID != "" || past.Failure == nil || past.Failure.Category != "outcome_unknown" || past.Failure.BlockerID != target.ID {
			continue
		}
		if _, err := e.Store.TransitionTaskOperation(ctx, past.ID, protocol.OperationTransition{ExpectedRevision: past.Revision, Status: past.Status, VerificationStatus: "pending", ResolvedByID: observer.ID, Resolution: "acknowledged", Failure: past.Failure}); err != nil {
			return err
		}
	}
	return nil
}

// Check descriptions do not change the commands that were actually rerun.
// Every earlier requested check must be covered; a successful subset is insufficient.
func verificationCovers(current, earlier json.RawMessage) bool {
	type check struct {
		Command   string `json:"command"`
		Directory string `json:"directory"`
	}
	var now, old struct {
		Checks []check `json:"checks"`
	}
	if json.Unmarshal(current, &now) != nil || json.Unmarshal(earlier, &old) != nil || len(old.Checks) == 0 {
		return false
	}
	for _, required := range old.Checks {
		found := false
		for _, actual := range now.Checks {
			if strings.TrimSpace(actual.Command) == strings.TrimSpace(required.Command) && filepath.Clean(actual.Directory) == filepath.Clean(required.Directory) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// A later authorized, verified mutation establishes the current state of the
// same file. Earlier successful intermediate effects remain recorded as superseded;
// a failed invocation is never cleared merely because the path is the same.
func (e *Engine) supersedeIntermediateFileEffects(ctx context.Context, current protocol.TaskOperation, evidenceIDs []string) error {
	args, err := e.operationArgs(ctx, current)
	if err != nil {
		return err
	}
	target, err := taskFileTarget(ctx, args)
	if err != nil {
		return err
	}
	item, err := e.Store.GetItem(ctx, current.ItemID)
	if err != nil {
		return err
	}
	task, err := e.Store.GetExecutionTask(ctx, current.TaskID)
	if err != nil {
		return err
	}
	snapshot, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return err
	}
	for _, past := range snapshot.Operations {
		if past.ID == current.ID || past.Status != "succeeded" || past.ResolvedByID != "" || past.Tool != "file.write" && past.Tool != "file.edit" {
			continue
		}
		prior, err := e.Store.GetItem(ctx, past.ItemID)
		if err != nil || prior.Seq >= item.Seq || taskItemMetadata(prior).EngineSource != "builtin_file_mutation" {
			continue
		}
		args, err := e.operationArgs(ctx, past)
		if err != nil {
			return err
		}
		previousTarget, err := taskFileTarget(ctx, args)
		if err != nil || previousTarget != target {
			continue
		}
		if _, err := e.Store.TransitionTaskOperation(ctx, past.ID, protocol.OperationTransition{ExpectedRevision: past.Revision, Status: past.Status, VerificationStatus: past.VerificationStatus, EvidenceIDs: evidenceIDs, ResolvedByID: current.ID, Resolution: "superseded", Failure: past.Failure}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) taskWorkflowConditionCleared(ctx context.Context, threadID string, op protocol.TaskOperation) bool {
	if op.Status != "blocked" || op.Failure == nil {
		return false
	}
	blockerID := op.Failure.BlockerID
	if op.Failure.AuthoritySource != "work_graph" {
		// Older engine-denied items predate typed authority provenance. Recover
		// it from the original denied item, never from a tool's error text alone.
		// This also handles tasks already imported by an earlier local build.
		if op.ImplementationID != "legacy_unverified" {
			return false
		}
		item, err := e.Store.GetItem(ctx, op.ItemID)
		if err != nil || item.ThreadID != threadID || item.TurnID != op.TurnID || item.Kind != protocol.ItemToolCall || item.Status != protocol.ItemDenied || item.Tool == nil || item.Tool.Name != op.Tool {
			return false
		}
		message := item.Tool.Error
		if message != "workflow not ready" {
			const prefix = "workflow not ready: "
			var b workflowBlocker
			if !strings.HasPrefix(message, prefix) || json.Unmarshal([]byte(strings.TrimPrefix(message, prefix)), &b) != nil || b.Code != "workflow_not_ready" || b.WorkID == "" {
				return false
			}
			blockerID = b.WorkID
		}
	}
	tool := op.Tool
	if tool == "work.update" {
		tool = "file.write"
	}
	if e.checkWorkflowGate(ctx, threadID, tool) == nil {
		return true
	}
	w, ok, err := e.Store.OpenWorkForThread(ctx, threadID)
	if err != nil || !ok || w.ID != blockerID {
		return false
	}
	detail, err := e.Store.GetWorkDetail(ctx, w.ID)
	return err == nil && len(work.CompletionBlockers(detail)) == 0
}
