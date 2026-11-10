package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func classifyActionFailure(operationID string, err error) protocol.ActionFailure {
	var typed *taskActionError
	if errors.As(err, &typed) {
		f := typed.Failure
		f.OperationID = operationID
		return f
	}
	f := protocol.ActionFailure{OperationID: operationID, Category: "execution_failed", Message: err.Error(), Retryable: true, NextStep: "Inspect the failure and current resource state before proposing a focused correction."}
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, ErrApprovalExpired) || strings.Contains(text, "approval not granted"):
		f.Category = "approval_required"
		f.Retryable = false
		f.NextStep = "Wait for an actual engine approval before proceeding."
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.HasSuffix(text, "eof") || strings.Contains(text, "timeout") || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(text, "timed out") || strings.Contains(text, "unexpected eof") || strings.Contains(text, "connection reset") || strings.Contains(text, "disconnected"):
		f.Category = "outcome_unknown"
		f.Retryable = false
		f.NextStep = "Inspect the action's actual effects before any further mutation; do not replay it."
	case errors.Is(err, store.ErrTaskConflict) || strings.Contains(text, "stale") || strings.Contains(text, "revision conflict"):
		f.Category = "stale_state"
		f.NextStep = "Refresh the current task/work and node revisions and states before correcting the update."
	case strings.Contains(text, "old_string") && (strings.Contains(text, "not found") || strings.Contains(text, "matches")):
		f.Category = "edit_conflict"
		f.NextStep = "Read the current file and copy a unique matching section before retrying the edit."
	case errors.Is(err, os.ErrNotExist) || strings.Contains(text, "does not exist") || strings.Contains(text, "no such file"):
		f.Category = "missing_resource"
		f.NextStep = "Check the resource location and list its parent before retrying."
	case strings.Contains(text, "denied this action") || strings.Contains(text, "rejected") && !strings.Contains(text, "work.update rejected"):
		f.Category = "approval_rejected"
		f.Retryable = false
		f.NextStep = "Stop this action; obtain a new explicit user decision before changing its approval scope."
	case strings.Contains(text, "blocked by policy") || strings.Contains(text, "read-only") || strings.Contains(text, "permission denied") || strings.Contains(text, "outside the project") || strings.Contains(text, "chat has no project") || strings.Contains(text, "not permitted") || strings.Contains(text, "policy narrowed") || strings.Contains(text, "blocked by plugin") || strings.HasPrefix(text, "blocked:"):
		f.Category = "policy_denied"
		f.Retryable = false
		f.NextStep = "Respect the current permission boundary; clarify the constraint rather than changing tools to bypass it."
	case errors.Is(err, ErrApprovalExpired) || strings.Contains(text, "approval not granted"):
		f.Category = "approval_required"
		f.Retryable = false
		f.NextStep = "Wait for an actual engine approval before proceeding."
	}
	return f
}
func (e *Engine) taskFailureState(ctx context.Context, taskID string, f protocol.ActionFailure) protocol.ActionFailure {
	if f.Category != "stale_state" {
		return f
	}
	task, err := e.Store.GetExecutionTask(ctx, taskID)
	if err != nil {
		return f
	}
	state := map[string]any{"task_id": task.ID, "expected_task_revision": task.Revision}
	if task.WorkID != "" {
		if d, err := e.Store.GetWorkDetail(ctx, task.WorkID); err == nil {
			nodes := make([]map[string]any, 0, len(d.Nodes))
			for _, n := range d.Nodes {
				if n.ValidUntil == nil && n.Status != "superseded" {
					nodes = append(nodes, map[string]any{"id": n.ID, "expected_revision": n.Revision, "from_status": n.Status})
				}
			}
			state["work_id"] = d.Work.ID
			state["expected_revision"] = d.Work.Revision
			state["nodes"] = nodes
		}
	}
	raw, err := json.Marshal(state)
	if err == nil && len(raw) <= 65536 {
		f.State = raw
	}
	return f
}
func (e *Engine) taskRecoveryContext(ctx context.Context, snapshot protocol.TaskSnapshot) (string, error) {
	var pending []protocol.ActionFailure
	for _, op := range snapshot.Operations {
		if op.Failure != nil && op.ResolvedByID == "" && !e.taskWorkflowConditionCleared(ctx, snapshot.Task.ThreadID, op) {
			f := e.taskFailureState(ctx, snapshot.Task.ID, *op.Failure)
			pending = append(pending, compactTaskFailure(f))
		}
	}
	if len(pending) == 0 {
		return "", nil
	}
	if len(pending) > 8 {
		pending = pending[len(pending)-8:]
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return "", err
	}
	for len(raw) > 12*1024 && len(pending) > 1 {
		pending = pending[1:]
		raw, err = json.Marshal(pending)
		if err != nil {
			return "", err
		}
	}
	return "Unresolved operation failures and recovery steps:\n" + string(raw) + "\nDo not mark graph nodes completed or manufacture approval to clear a failure. Reads may help recover; a different tool cannot override permission denial.", nil
}
func (e *Engine) recoverInterruptedTask(ctx context.Context, taskID string) error {
	task, err := e.Store.GetExecutionTask(ctx, taskID)
	if err != nil {
		return err
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return err
	}
	if snap.Task.ID != taskID {
		return store.ErrTaskConflict
	}
	for _, op := range snap.Operations {
		if op.Status != "running" {
			continue
		}
		status := "outcome_unknown"
		failure := &protocol.ActionFailure{Category: "outcome_unknown", OperationID: op.ID, Message: "The engine stopped while this action was running; its effects are uncertain.", NextStep: "Inspect its effects before retrying. Do not replay the saved action."}
		if op.ItemID != "" {
			if item, err := e.Store.GetItem(ctx, op.ItemID); err == nil {
				switch item.Status {
				case protocol.ItemCompleted:
					status = "succeeded"
					failure = nil
				case protocol.ItemDenied:
					status = "blocked"
					if item.Tool != nil {
						f := classifyActionFailure(op.ID, errors.New(item.Tool.Error))
						failure = &f
					}
				case protocol.ItemFailed:
					status = "failed"
					if item.Tool != nil {
						f := classifyActionFailure(op.ID, errors.New(item.Tool.Error))
						failure = &f
					}
				}
			}
		}
		if _, err := e.Store.TransitionTaskOperation(ctx, op.ID, protocol.OperationTransition{ExpectedRevision: op.Revision, Status: status, Failure: failure}); err != nil {
			return err
		}
	}
	// Orphaned interpretation/workflow requests convey no automatic authority.
	approvals, err := e.Store.ListApprovals(ctx, "pending")
	if err != nil {
		return err
	}
	for _, a := range approvals {
		if a.ThreadID != task.ThreadID || (a.Kind != "task_requirement" && a.Kind != "task_interpretation") {
			continue
		}
		e.mu.Lock()
		live := e.approvals[a.ID] != nil
		e.mu.Unlock()
		if !live {
			if err := e.expireApproval(ctx, a, "recovered interrupted task"); err != nil {
				return err
			}
		}
	}
	return nil
}
func ambiguousTaskRetry(snapshot protocol.TaskSnapshot) bool {
	paths := map[string]bool{}
	for _, op := range snapshot.Operations {
		if op.ResolvedByID != "" || (op.Status != "failed" && op.Status != "outcome_unknown") {
			continue
		}
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(op.Args, &args)
		if args.Path == "" {
			_ = json.Unmarshal(op.Resources, &args)
		}
		if args.Path != "" {
			paths["path:"+args.Path] = true
		} else {
			raw := op.Args
			if len(raw) == 0 {
				raw = op.Resources
			}
			paths[fmt.Sprintf("opaque:%s:%x", op.Tool, sha256.Sum256(raw))] = true
		}
	}
	return len(paths) > 1
}

type taskRecoveryState struct {
	pending  map[string]protocol.ActionFailure
	attempts map[string]int
}

func (r *taskRecoveryState) observe(f protocol.ActionFailure, action string) bool {
	if r.pending == nil {
		r.pending = map[string]protocol.ActionFailure{}
		r.attempts = map[string]int{}
	}
	if !f.Advisory {
		r.pending[action] = f
	}
	key := f.Category + ":" + f.BlockerID
	if f.BlockerID == "" {
		key = f.Category
	}
	if len(f.State) > 0 {
		key += fmt.Sprintf(":%x", sha256.Sum256(f.State))
	}
	r.attempts[key]++
	return !f.Retryable || r.attempts[key] >= 3
}
func (r *taskRecoveryState) resolved(action string) { delete(r.pending, action) }
func (r *taskRecoveryState) failure() *protocol.ActionFailure {
	for _, f := range r.pending {
		copy := f
		return &copy
	}
	return nil
}
func (e *Engine) blockTaskSummary(ctx context.Context, id string, f protocol.ActionFailure) error {
	var item protocol.Item
	var err error
	if id != "" {
		item, err = e.Store.GetItem(ctx, id)
	} else {
		op, lookupErr := e.Store.GetTaskOperation(ctx, f.OperationID)
		if lookupErr != nil {
			return lookupErr
		}
		task, lookupErr := e.Store.GetExecutionTask(ctx, op.TaskID)
		if lookupErr != nil {
			return lookupErr
		}
		item, err = e.newItem(ctx, protocol.Turn{ID: op.TurnID, ThreadID: task.ThreadID}, protocol.ItemAgentMessage)
	}
	if err != nil {
		return err
	}
	item.Status = protocol.ItemFailed
	item.Text = fmt.Sprintf("Execution remains blocked: %s %s", f.Message, f.NextStep)
	if snap, lookupErr := e.Store.ActiveExecutionTask(ctx, item.ThreadID); lookupErr == nil {
		var changed []string
		for _, op := range snap.Operations {
			if op.Status == "succeeded" && (op.Tool == "file.write" || op.Tool == "file.edit") {
				var a struct{ Path string }
				_ = json.Unmarshal(op.Args, &a)
				if a.Path == "" {
					var r struct{ Path string }
					_ = json.Unmarshal(op.Resources, &r)
					a.Path = r.Path
				}
				changed = append(changed, op.Tool+" "+a.Path)
			}
		}
		if len(changed) > 0 {
			item.Text += " Changes already ran: " + strings.Join(changed, ", ") + ". Final verification is incomplete."
		}
	}
	mergeTaskItemData(&item, "actionFailure", f)
	return e.saveAndPublish(ctx, item, protocol.NotifyItemCompleted)
}

func (e *Engine) unchangedTaskBlocker(ctx context.Context, th protocol.Thread, snapshot protocol.TaskSnapshot) (*protocol.ActionFailure, error) {
	for _, op := range snapshot.Operations {
		if op.ResolvedByID != "" || op.Failure == nil || op.Failure.Retryable || (op.Status != "failed" && op.Status != "blocked") {
			continue
		}
		switch op.Failure.Category {
		case "policy_denied", "approval_rejected", "approval_required", "workflow_requirement":
		default:
			continue
		}
		decision, err := e.checkTaskAuthority(ctx, th, snapshot, op)
		if err != nil {
			return nil, err
		}
		if (decision.Outcome == "deny" || decision.Outcome == "blocked") && decision.Failure != nil {
			return decision.Failure, nil
		}
	}
	return nil, nil
}

// Prompt summaries are bounded; original diagnostic records remain in task.context.
func compactTaskFailure(f protocol.ActionFailure) protocol.ActionFailure {
	if len(f.State) > 2048 {
		f.State = nil
	}
	if len([]rune(f.Message)) > 512 {
		f.Message = string([]rune(f.Message)[:512]) + "… [see task.context view=state]"
	}
	if len([]rune(f.NextStep)) > 512 {
		f.NextStep = string([]rune(f.NextStep)[:512]) + "…"
	}
	return f
}
