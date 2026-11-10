package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shaktsin/umcode/internal/protocol"
)

func (e *Engine) nextExecutionCorrection(ctx context.Context, taskID, turnID string) (string, bool, protocol.TaskExpectationEvaluation, error) {
	evaluation, err := e.evaluateTaskExpectations(ctx, taskID, turnID)
	if err != nil {
		return "", false, evaluation, err
	}
	if evaluation.Status != "not_executed" && evaluation.Status != "unverified" {
		return "", false, evaluation, nil
	}
	_, ok, err := e.Store.ReserveTaskCorrection(ctx, turnID)
	if err != nil || !ok {
		return "", false, evaluation, err
	}
	missing := evaluation.Missing
	if len(missing) > 16 {
		missing = missing[:16]
	}
	raw, _ := json.Marshal(missing)
	text := "Engine evidence check: the requested outcome is not yet established. Narration and bracketed receipts did not execute an action. Unfinished expectation IDs: " + string(raw) + ". Use the saved requested outcomes and real tool results, perform only authorized actions, and verify the actual result. Read task.context view=state if needed. Do not invent restrictions, claim success without evidence, downgrade the objective, or replay uncertain actions."
	if evaluation.Status == "unverified" {
		text += " Some effects already ran; inspect and verify them before considering another mutation."
	}
	return text, true, evaluation, nil
}
func (e *Engine) markExecutionProvisional(ctx context.Context, messageID string) error {
	if messageID == "" {
		return nil
	}
	item, err := e.Store.GetItem(ctx, messageID)
	if err != nil {
		return err
	}
	mergeTaskItemData(&item, "provisionalExecution", true)
	mergeTaskItemData(&item, "provisionalText", item.Text)
	item.Text = "The requested action is still pending; checking execution evidence."
	return e.saveAndPublish(ctx, item, protocol.NotifyItemCompleted)
}
func taskInquiryOnly(snapshot protocol.TaskSnapshot, turnID string) bool {
	current := false
	for _, ex := range snapshot.Expectations {
		if ex.RequestTurnID == turnID {
			current = true
		}
	}
	found := false
	for _, ex := range snapshot.Expectations {
		if ex.Status == "superseded" || current && ex.RequestTurnID != turnID {
			continue
		}
		found = true
		if ex.Kind == "mutation" {
			return false
		}
	}
	for _, op := range snapshot.Operations {
		if op.TurnID == turnID && op.ResolvedByID == "" && op.Failure != nil && !op.Failure.Advisory {
			return false
		}
	}
	return found
}
func (e *Engine) finalizeExecutionExpectation(ctx context.Context, turn protocol.Turn, messageID string, evaluation protocol.TaskExpectationEvaluation) error {
	if evaluation.Status == "blocked" {
		if evaluation.Failure != nil {
			return &taskActionError{Failure: *evaluation.Failure}
		}
		return errors.New("requested action remains blocked")
	}
	item, err := e.Store.GetItem(ctx, messageID)
	if err != nil {
		item, err = e.newItem(ctx, turn, protocol.ItemAgentMessage)
		if err != nil {
			return err
		}
	}
	metadata := map[string]any{"status": evaluation.Status, "expectationIds": evaluation.Missing, "evidenceIds": evaluation.EvidenceIDs}
	mergeTaskItemData(&item, "taskOutcome", metadata)
	if evaluation.Status == "satisfied" {
		return e.saveAndPublish(ctx, item, protocol.NotifyItemCompleted)
	}
	text := "The model did not execute the requested action."
	if evaluation.Status == "unverified" {
		text = "Changes ran, but the requested result has not been verified."
	}
	if evaluation.Status == "clarify" {
		text = "The requested result is unclear; clarify what should be produced or checked."
	}
	mergeTaskItemData(&item, "provisionalText", item.Text)
	mergeTaskItemData(&item, "provisionalExecution", false)
	f := protocol.ActionFailure{Category: evaluation.Status, BlockerID: "expectation:" + turn.ID, Message: text, NextStep: "The task remains unfinished. Retry or clarify the requested result.", Retryable: false}
	mergeTaskItemData(&item, "actionFailure", f)
	item.Text, item.Status = text, protocol.ItemFailed
	if err := e.saveAndPublish(ctx, item, protocol.NotifyItemCompleted); err != nil {
		return err
	}
	return &taskActionError{Failure: f}
}
func (e *Engine) prepareExecutionExpectations(ctx, sctx context.Context, th protocol.Thread, turn protocol.Turn, res *resolved, p protocol.TurnStartParams, budget *turnBudget) error {
	user, err := e.currentTaskUser(sctx, turn)
	if err != nil {
		return err
	}
	_, err = e.resolveTaskExpectations(ctx, sctx, th, turn, user, res, budget, p.TaskIntent)
	return err
}
func (e *Engine) finishIntentClarification(ctx context.Context, turn protocol.Turn, err error) error {
	var clarification *taskIntentClarification
	if !errors.As(err, &clarification) {
		return fmt.Errorf("resolve requested outcome: %w", err)
	}
	item, itemErr := e.newItem(ctx, turn, protocol.ItemAgentMessage)
	if itemErr != nil {
		return itemErr
	}
	item.Text, item.Status = clarification.Question, protocol.ItemCompleted
	mergeTaskItemData(&item, "taskOutcome", map[string]string{"status": "clarify"})
	return e.saveAndPublish(ctx, item, protocol.NotifyItemCompleted)
}
