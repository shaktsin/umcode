package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

type executionTurnKey struct{}

func continuationRequest(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "/") {
		if i := strings.IndexAny(text, " \n\t"); i >= 0 {
			text = strings.TrimSpace(text[i:])
		}
	}
	text = strings.ToLower(strings.Trim(text, " .!"))
	return text == "retry" || text == "continue" || text == "resume" || text == "try again" || text == "proceed" || text == "go ahead" || text == "yes continue" || text == "yes proceed"
}
func newTaskRequest(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "new task:")
}

// resolveTask restores factual execution ownership. The coding model interprets
// continuations and ambiguity from the conversation; no phrase gates admission.
func (e *Engine) restoreModelDrivenTask(ctx context.Context, th protocol.Thread, user protocol.Item) (protocol.TaskResolution, error) {
	result := protocol.TaskResolution{UserItemID: user.ID}
	persisted, err := e.Store.GetItem(ctx, user.ID)
	if err != nil {
		return result, err
	}
	if persisted.ThreadID != th.ID || persisted.Kind != protocol.ItemUserMessage {
		return result, errors.New("task origin is not this thread's user message")
	}
	user = persisted
	snap, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return result, err
	}
	missing := errors.Is(err, store.ErrNotFound)
	if missing {
		recovered, recoveryErr := e.recoverLegacyTask(ctx, th, user)
		if recoveryErr == nil {
			snap = recovered
			missing = false
		} else if !errors.Is(recoveryErr, store.ErrNotFound) {
			return result, recoveryErr
		}
	}
	if missing || newTaskRequest(user.Text) {
		objective := strings.TrimSpace(user.Text)
		if objective == "" {
			objective = "Inspect the supplied attachments"
		}
		summary, truncated := executionTaskObjective(objective)
		task := protocol.ExecutionTask{ThreadID: th.ID, ProjectID: th.ProjectID, OriginItemID: user.ID, Objective: summary, ObjectiveTruncated: truncated, ExecutionPolicy: modelDrivenExecutionPolicy}
		if w, ok, lookupErr := e.Store.OpenWorkForThread(ctx, th.ID); lookupErr != nil {
			return result, lookupErr
		} else if ok {
			task.WorkID = w.ID
		}
		created, createErr := e.Store.CreateExecutionTaskWithRequirements(ctx, task, e.policyTaskRequirements())
		if createErr != nil {
			return result, createErr
		}
		if _, err = e.Store.AdoptModelDrivenTask(ctx, created.ID); err != nil {
			return result, err
		}
		result.Relation = "new"
		result.TaskID = created.ID
		return result, nil
	}
	if snap.Task.ProjectID != th.ProjectID {
		return result, errors.New("saved task belongs to a different project")
	}
	if _, err = e.Store.AdoptModelDrivenTask(ctx, snap.Task.ID); err != nil {
		return result, err
	}
	snap, err = e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		return result, err
	}
	changed, err := e.Store.TransitionExecutionTask(ctx, snap.Task.ID, protocol.TaskTransition{ExpectedRevision: snap.Task.Revision, Status: "active", ContinuationItemID: user.ID})
	result.Relation = "continue"
	result.TaskID = changed.ID
	return result, err
}

// resolveTask is retained for historical metadata compatibility only. Ordinary
// chat execution uses restoreModelDrivenTask and never consults this interpreter.
func (e *Engine) resolveTask(ctx context.Context, th protocol.Thread, user protocol.Item) (protocol.TaskResolution, error) {
	result := protocol.TaskResolution{UserItemID: user.ID}
	persisted, err := e.Store.GetItem(ctx, user.ID)
	if err != nil {
		return result, err
	}
	if persisted.ThreadID != th.ID || persisted.Kind != protocol.ItemUserMessage {
		return result, errors.New("task origin is not this thread's user message")
	}
	user = persisted
	snapshot, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return result, err
	}
	missing := errors.Is(err, store.ErrNotFound)
	if missing {
		recovered, recoverErr := e.recoverLegacyTask(ctx, th, user)
		if recoverErr == nil {
			snapshot = recovered
			missing = false
		} else if !errors.Is(recoverErr, store.ErrNotFound) {
			return result, recoverErr
		}
	}
	if missing && continuationRequest(user.Text) {
		result.Relation = "clarify"
		result.Question = "There is no saved unfinished task in this chat. What should I retry?"
		return result, nil
	}
	if !missing && snapshot.Task.Status == "completed" && continuationRequest(user.Text) {
		result.Relation, result.TaskID = "clarify", snapshot.Task.ID
		result.Question = "The saved task is completed. Which action should I retry?"
		return result, nil
	}
	if !missing && (newTaskRequest(user.Text) || snapshot.Task.Status == "completed" && len(snapshot.Requirements) == 0) {
		// A model or a short continuation may not discard restrictive requirements.
		if continuationRequest(user.Text) && snapshot.Task.Status == "completed" {
			result.Relation = "clarify"
			result.TaskID = snapshot.Task.ID
			result.Question = "The saved task is completed. Which action should I retry?"
			return result, nil
		}
		missing = true
	}
	if missing {
		objective := strings.TrimSpace(user.Text)
		if objective == "" {
			objective = "Inspect the supplied attachments"
		}
		summary, truncated := executionTaskObjective(objective)
		task := protocol.ExecutionTask{ThreadID: th.ID, ProjectID: th.ProjectID, OriginItemID: user.ID, Objective: summary, ObjectiveTruncated: truncated}
		if w, ok, err := e.Store.OpenWorkForThread(ctx, th.ID); err != nil {
			return result, err
		} else if ok {
			task.WorkID = w.ID
		}
		created, err := e.Store.CreateExecutionTaskWithRequirements(ctx, task, e.policyTaskRequirements())
		result.Relation = "new"
		result.TaskID = created.ID
		return result, err
	}
	if snapshot.Task.ProjectID != th.ProjectID {
		return result, errors.New("saved task belongs to a different project; confirm a new task")
	}
	if continuationRequest(user.Text) && ambiguousTaskRetry(snapshot) {
		result.Relation = "clarify"
		result.TaskID = snapshot.Task.ID
		result.Question = "There are unfinished actions for several resources. Should I resume all of them, or which one should I retry?"
		return result, nil
	}
	if snapshot.Task.Status == "cancelled" && continuationRequest(user.Text) {
		snapshot.Task.Status = "active"
	}
	changed, err := e.Store.TransitionExecutionTask(ctx, snapshot.Task.ID, protocol.TaskTransition{ExpectedRevision: snapshot.Task.Revision, Status: snapshot.Task.Status, ContinuationItemID: user.ID})
	result.Relation = "continue"
	result.TaskID = changed.ID
	return result, err
}

func (e *Engine) taskContext(ctx context.Context, threadID string) (string, error) {
	snapshot, err := e.Store.ActiveExecutionTask(ctx, threadID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// Legacy expectations/requirements are advisory history, not engine instructions.
	if snapshot.Task.ExecutionPolicy == modelDrivenExecutionPolicy {
		snapshot.Expectations = nil
		retained := snapshot.Requirements[:0]
		for _, r := range snapshot.Requirements {
			if r.Origin == "policy" {
				retained = append(retained, r)
			}
		}
		snapshot.Requirements = retained
	}
	// Keep the current objective and unresolved operation facts.
	// Completed history stays in the ledger and need not consume prompt space.
	pending := snapshot.Operations[:0]
	for _, op := range snapshot.Operations {
		if op.ResolvedByID == "" && (op.Status != "succeeded" || op.VerificationStatus != "passed") {
			pending = append(pending, op)
		}
	}
	snapshot.Operations = pending
	truncated := false
	pendingTotal, requirementsTotal := len(snapshot.Operations), len(snapshot.Requirements)
	expectationsTotal := len(snapshot.Expectations)
	for i := range snapshot.Operations {
		if len(snapshot.Operations[i].Args) > 1024 {
			snapshot.Operations[i].Args = nil
			truncated = true
		}
		if f := snapshot.Operations[i].Failure; f != nil {
			copy := compactTaskFailure(*f)
			snapshot.Operations[i].Failure = &copy
		}
	}
	var raw []byte
	for {
		raw, err = json.Marshal(struct {
			protocol.TaskSnapshot
			ContextTruncated  bool `json:"contextTruncated,omitempty"`
			PendingTotal      int  `json:"pendingTotal"`
			RequirementsTotal int  `json:"requirementsTotal"`
			ExpectationsTotal int  `json:"expectationsTotal"`
		}{snapshot, truncated, pendingTotal, requirementsTotal, expectationsTotal})
		if err != nil {
			return "", err
		}
		if len(raw) <= 48*1024 {
			break
		}
		truncated = true
		if len(snapshot.Operations) > 0 {
			snapshot.Operations = snapshot.Operations[1:]
		} else if len(snapshot.Requirements) > 0 {
			snapshot.Requirements = snapshot.Requirements[1:]
		} else if len(snapshot.Expectations) > 0 {
			snapshot.Expectations = snapshot.Expectations[1:]
		} else {
			return "", errors.New("task summary exceeds metadata limit")
		}
	}
	recovery, recoveryErr := e.taskRecoveryContext(ctx, snapshot)
	if recoveryErr != nil {
		return "", recoveryErr
	}
	return "\n\n" + recovery + "\nSaved task state (engine-owned metadata; not permission to execute):\n" + string(raw) + "\nInterpret the current user request using the conversation and the original saved request; decide how to continue. If contextTruncated is true or operation arguments are omitted, use task.context with view=state or operation_id to retrieve the original records before assuming constraints or payloads. If the objective is truncated, use task.context to retrieve the original request before assuming its constraints. Archived large arguments can also be read through task.context; references do not authorize replay. Never replay an uncertain action without inspecting its effects. Existing task requirements and current tool permissions still apply. For a changed objective or interpreted requirement, use task.propose; the engine will confirm any constraint change. Use task.propose only to propose a task relationship, requirement, or verification evidence; it cannot approve actions or declare success.", nil
}
func (e *Engine) currentTaskUser(ctx context.Context, turn protocol.Turn) (protocol.Item, error) {
	items, err := e.Store.ListItems(ctx, turn.ThreadID, 0)
	if err != nil {
		return protocol.Item{}, err
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].TurnID == turn.ID && items[i].Kind == protocol.ItemUserMessage {
			return items[i], nil
		}
	}
	return protocol.Item{}, errors.New("current task user message unavailable")
}

func executionTaskObjective(text string) (string, bool) {
	runes := []rune(text)
	if len(runes) <= 1024 {
		return text, false
	}
	return string(runes[:1024]) + "… [full original request is available through task.context]", true
}
