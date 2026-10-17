package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

type expectationItemData struct {
	FileListing   *tools.FileListingEvidence   `json:"fileListing"`
	FileExistence *tools.FileExistenceEvidence `json:"fileExistence"`
}

func (e *Engine) expectationEvidence(ctx context.Context, ex protocol.TaskExpectation, snap protocol.TaskSnapshot, request protocol.Turn) (ids []string, effect bool, err error) {
	var resource protocol.TaskResource
	if err = json.Unmarshal(ex.Resource, &resource); err != nil {
		return
	}
	if ex.Kind == "explanation" {
		return nil, false, nil
	}
	origin, originErr := e.Store.GetItem(ctx, ex.OriginItemID)
	if originErr != nil {
		return nil, false, originErr
	}
	if origin.ThreadID != snap.Task.ThreadID || origin.Kind != protocol.ItemUserMessage {
		return nil, false, errors.New("expectation user origin is invalid")
	}
	var path string
	if resource.Type == "file" {
		path, err = taskFileTarget(ctx, ex.Resource)
		if err != nil {
			return
		}
		// The canonical binding fixed during intent validation must still refer to
		// the same target. Retargeting a symlink cannot move the verified resource.
		if resource.Path != path || resource.RequestedPath != "" && pathutil.Resolved(resource.RequestedPath) != path {
			return nil, false, nil
		}
	}
	// Fresh inquiry results must be from this turn. Original mutation verification
	// may be retained on explicit retry, but is rechecked against actual bytes.
	for _, op := range snap.Operations {
		item, lookup := e.Store.GetItem(ctx, op.ResultItemID)
		if lookup != nil {
			continue
		}
		if item.Tool == nil || item.ThreadID != snap.Task.ThreadID || item.Seq <= origin.Seq {
			continue
		}
		metadata := taskItemMetadata(item)
		if resource.Type == "file" && ex.Kind == "mutation" && metadata.EngineSource == "builtin_file_mutation" && op.Status == "succeeded" {
			args, argsErr := e.operationArgs(ctx, op)
			if argsErr != nil {
				return nil, false, argsErr
			}
			target, targetErr := taskFileTarget(ctx, args)
			if targetErr == nil && target == path {
				effect = true
			}
		}
		if ex.Predicate == "operation_verified" {
			// Opaque targets require a registered verifier contract. A matching tool
			// name or a generic successful exit alone cannot certify external effects.
			continue
		}
		if metadata.EngineSource == "builtin_file_list" && op.Tool == "file.list" && ex.Kind == "inspection" && (ex.Predicate == "list" || ex.Predicate == "read" || ex.Predicate == "exists") && item.TurnID == request.ID && item.Status == protocol.ItemCompleted && op.Status == "succeeded" {
			args, argsErr := e.operationArgs(ctx, op)
			if argsErr != nil {
				return nil, false, argsErr
			}
			var listingArgs struct {
				Path      string `json:"path"`
				Workspace string `json:"workspace"`
			}
			if json.Unmarshal(args, &listingArgs) != nil {
				continue
			}
			if listingArgs.Path == "" {
				listingArgs.Path = "."
			}
			args, _ = json.Marshal(listingArgs)
			listedPath, pathErr := taskFileTarget(ctx, args)
			var observed expectationItemData
			_ = json.Unmarshal(item.Data, &observed)
			if pathErr == nil && listedPath == path && observed.FileListing != nil && pathutil.Resolved(observed.FileListing.Path) == path && observed.FileListing.Complete {
				current, _, inspectErr := tools.ObserveDirectory(path)
				if inspectErr == nil && current.Complete && current.SHA256 == observed.FileListing.SHA256 && current.Entries == observed.FileListing.Entries {
					return []string{item.ID}, false, nil
				}
			}
		}
		if metadata.EngineSource != "builtin_file_read" || op.Tool != "file.read" {
			continue
		}
		if ex.Kind == "inspection" && item.TurnID != request.ID {
			continue
		}
		args, argsErr := e.operationArgs(ctx, op)
		if argsErr != nil {
			return nil, false, argsErr
		}
		readPath, pathErr := taskFileTarget(ctx, args)
		if pathErr != nil || readPath != path {
			continue
		}
		var observed expectationItemData
		_ = json.Unmarshal(item.Data, &observed)
		if ex.Predicate == "exists" && observed.FileExistence != nil && pathutil.Resolved(observed.FileExistence.Path) == path {
			// Recheck existence only; no missing-resource inference from error prose.
			_, statErr := os.Stat(path)
			exists := statErr == nil
			if (statErr == nil || errors.Is(statErr, os.ErrNotExist)) && exists == observed.FileExistence.Exists {
				return []string{item.ID}, effect, nil
			}
		}
		if item.Status != protocol.ItemCompleted || op.Status != "succeeded" {
			continue
		}
		for _, proof := range metadata.FileProofs {
			if proof.Path != path {
				continue
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			hash := sha256.Sum256(body)
			actual := hex.EncodeToString(hash[:])
			if actual != proof.SHA256 || len(body) != proof.Bytes {
				continue
			}
			if ex.Predicate == "read" {
				return []string{item.ID}, effect, nil
			}
			if ex.Predicate != "file_content" {
				continue
			}
			if resource.ExpectedSHA256 != "" && resource.ExpectedSHA256 != actual {
				continue
			}
			if ex.AllowExisting && resource.ExpectedSHA256 != "" {
				return []string{item.ID}, effect, nil
			}
			for _, satisfied := range proof.Satisfies {
				for _, mutation := range snap.Operations {
					if mutation.ID != satisfied || mutation.Status != "succeeded" || mutation.VerificationStatus != "passed" {
						continue
					}
					mi, miErr := e.Store.GetItem(ctx, mutation.ItemID)
					if miErr != nil || mi.Seq <= origin.Seq || taskItemMetadata(mi).EngineSource != "builtin_file_mutation" {
						continue
					}
					args, argsErr := e.operationArgs(ctx, mutation)
					if argsErr != nil {
						return nil, false, argsErr
					}
					target, targetErr := taskFileTarget(ctx, args)
					if targetErr == nil && target == path && item.Seq > mi.Seq {
						return []string{mi.ID, item.ID}, true, nil
					}
				}
			}
		}
	}
	return nil, effect, nil
}
func (e *Engine) evaluateTaskExpectations(ctx context.Context, taskID, turnID string) (protocol.TaskExpectationEvaluation, error) {
	result := protocol.TaskExpectationEvaluation{Status: "satisfied"}
	task, err := e.Store.GetExecutionTask(ctx, taskID)
	if err != nil {
		return result, err
	}
	snap, err := e.Store.ActiveExecutionTask(ctx, task.ThreadID)
	if err != nil {
		return result, err
	}
	if snap.Task.ID != taskID {
		return result, store.ErrTaskConflict
	}
	if task.ExpectationsVersion == 0 {
		result.Status = "clarify"
		return result, nil
	}
	current := false
	for _, ex := range snap.Expectations {
		if ex.RequestTurnID == turnID {
			current = true
		}
	}
	evaluated := 0
	for _, ex := range snap.Expectations {
		if ex.Status == "superseded" || current && ex.RequestTurnID != turnID {
			continue
		}
		evaluated++
		if ex.Kind == "explanation" {
			continue
		}
		evidence, effect, proofErr := e.expectationEvidence(ctx, ex, snap, protocol.Turn{ID: turnID, ThreadID: task.ThreadID})
		if proofErr != nil {
			return result, proofErr
		}
		if len(evidence) > 0 {
			result.EvidenceIDs = append(result.EvidenceIDs, evidence...)
			if ex.Status == "pending" {
				if _, err := e.Store.ResolveTaskExpectation(ctx, ex.ID, ex.Revision, evidence); err != nil {
					return result, err
				}
			}
			continue
		}
		result.Missing = append(result.Missing, ex.ID)
		if result.Status == "satisfied" {
			result.Status = "not_executed"
		}
		if effect {
			result.Status = "unverified"
		}
	}
	if evaluated == 0 {
		result.Status = "clarify"
	}
	// Real denials and unknown effects stop corrective execution. Satisfied
	// read-only inquiries remain answers; unfinished parent actions remain saved.
	if len(result.Missing) > 0 {
		for _, op := range snap.Operations {
			if op.ResolvedByID != "" || op.Failure == nil || op.Failure.Advisory || e.taskWorkflowConditionCleared(ctx, task.ThreadID, op) {
				continue
			}
			if !op.Failure.Retryable || op.Status == "blocked" || op.Status == "outcome_unknown" {
				result.Status = "blocked"
				result.Failure = op.Failure
				break
			}
		}
	}
	return result, nil
}
