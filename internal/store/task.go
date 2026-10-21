package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

var ErrTaskConflict = errors.New("task revision conflict")

const taskRecordLimit = 65536

type taskReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func taskJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > taskRecordLimit {
		return nil, errors.New("task record exceeds 65536 bytes")
	}
	return b, nil
}
func taskStatus(status string) bool {
	switch status {
	case "active", "blocked", "awaiting_approval", "completed", "cancelled", "superseded":
		return true
	}
	return false
}
func operationStatus(status string) bool {
	switch status {
	case "proposed", "blocked", "awaiting_approval", "running", "succeeded", "failed", "cancelled", "outcome_unknown":
		return true
	}
	return false
}
func verificationStatus(status string) bool {
	return status == "pending" || status == "passed" || status == "failed" || status == "blocked"
}
func readExecutionTask(ctx context.Context, q taskReader, id string) (protocol.ExecutionTask, error) {
	var task protocol.ExecutionTask
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT data_json FROM engine_tasks WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return task, ErrNotFound
	}
	if err != nil {
		return task, err
	}
	err = json.Unmarshal(raw, &task)
	return task, err
}
func (s *Store) GetExecutionTask(ctx context.Context, id string) (protocol.ExecutionTask, error) {
	return readExecutionTask(ctx, s.DB, id)
}

// CreateExecutionTask switches the current objective atomically without modifying
// historical work graphs, tool results, or approvals.
func (s *Store) CreateExecutionTask(ctx context.Context, task protocol.ExecutionTask) (protocol.ExecutionTask, error) {
	return s.CreateExecutionTaskWithRequirements(ctx, task, nil)
}
func (s *Store) CreateExecutionTaskWithRequirements(ctx context.Context, task protocol.ExecutionTask, requirements []protocol.RequirementProposal) (protocol.ExecutionTask, error) {
	return s.CreateExecutionTaskWithHistory(ctx, task, requirements, nil)
}

// CreateExecutionTaskWithHistory imports existing engine records atomically with
// constraints; a failed import cannot leave a retry without its uncertain effects.
func (s *Store) CreateExecutionTaskWithHistory(ctx context.Context, task protocol.ExecutionTask, requirements []protocol.RequirementProposal, history []protocol.TaskOperation) (protocol.ExecutionTask, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	task, err = createExecutionTaskTx(ctx, tx, task)
	if err != nil {
		return task, err
	}
	for _, proposal := range requirements {
		proposal.TaskID = task.ID
		proposal.ExpectedRevision = task.Revision
		if _, err := putTaskRequirementTx(ctx, tx, proposal); err != nil {
			return task, err
		}
		task, err = readExecutionTask(ctx, tx, task.ID)
		if err != nil {
			return task, err
		}
	}
	for _, past := range history {
		past.TaskID = task.ID
		created, err := createTaskOperationTx(ctx, tx, past)
		if err != nil {
			return task, err
		}
		switch past.Status {
		case "succeeded", "failed", "blocked", "outcome_unknown":
			created.Status = past.Status
		default:
			return task, errors.New("invalid historical operation status")
		}
		if past.VerificationStatus == "passed" && past.Status == "succeeded" {
			created.VerificationStatus = "passed"
		}
		created.ResultItemID = created.ItemID
		created.Failure = past.Failure
		if created.Failure != nil {
			created.Failure.OperationID = created.ID
		}
		raw, err := taskJSON(created)
		if err != nil {
			return task, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_operations SET status=?,data_json=? WHERE id=?`, created.Status, raw, created.ID); err != nil {
			return task, err
		}
	}
	return task, tx.Commit()
}
func createExecutionTaskTx(ctx context.Context, tx *sql.Tx, task protocol.ExecutionTask) (protocol.ExecutionTask, error) {
	var project, kind, owner string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM threads WHERE id=?`, task.ThreadID).Scan(&project); err != nil {
		return task, err
	}
	if task.ProjectID != project {
		return task, errors.New("task project does not match thread")
	}
	if err := tx.QueryRowContext(ctx, `SELECT thread_id,kind FROM items WHERE id=?`, task.OriginItemID).Scan(&owner, &kind); err != nil {
		return task, err
	}
	if owner != task.ThreadID || kind != "userMessage" {
		return task, errors.New("task origin must be a user message in its thread")
	}
	if task.WorkID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM works WHERE id=?`, task.WorkID).Scan(&owner); err != nil {
			return task, err
		}
		if owner != task.ThreadID {
			return task, errors.New("task work belongs to another thread")
		}
	}
	if strings.TrimSpace(task.Objective) == "" {
		return task, errors.New("task objective required")
	}
	task.ID = NewID("etk")
	task.Revision = 1
	task.Status = "active"
	task.ContinuationItemID = ""
	task.CreatedAt = time.Now().UTC()
	task.UpdatedAt = task.CreatedAt
	raw, err := taskJSON(task)
	if err != nil {
		return task, err
	}
	var oldID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM engine_tasks WHERE thread_id=? AND current=1`, task.ThreadID).Scan(&oldID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return task, err
	}
	if err == nil {
		old, err := readExecutionTask(ctx, tx, oldID)
		if err != nil {
			return task, err
		}
		old.Status = "superseded"
		old.Revision++
		old.UpdatedAt = task.CreatedAt
		prior, err := taskJSON(old)
		if err != nil {
			return task, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE engine_tasks SET current=0,status=?,revision=?,data_json=? WHERE id=?`, old.Status, old.Revision, prior, oldID); err != nil {
			return task, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO engine_tasks(id,thread_id,work_id,origin_item_id,revision,status,data_json) VALUES(?,?,?,?,?,?,?)`, task.ID, task.ThreadID, nullableString(task.WorkID), task.OriginItemID, task.Revision, task.Status, raw)
	if err != nil {
		return task, err
	}
	return task, nil
}
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) ActiveExecutionTask(ctx context.Context, threadID string) (protocol.TaskSnapshot, error) {
	var result protocol.TaskSnapshot
	var id string
	// A read transaction prevents mixed task/operation revisions in prompt context.
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT id FROM engine_tasks WHERE thread_id=? AND current=1`, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Task, err = readExecutionTask(ctx, tx, id)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT data_json FROM task_operations WHERE task_id=? ORDER BY rowid`, id)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var raw []byte
		var op protocol.TaskOperation
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &op); err != nil {
			break
		}
		result.Operations = append(result.Operations, op)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if rowErr != nil {
		return result, rowErr
	}
	rows, err = tx.QueryContext(ctx, `SELECT data_json FROM task_requirements WHERE task_id=? ORDER BY rowid`, id)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var raw []byte
		var req protocol.TaskRequirement
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &req); err != nil {
			break
		}
		result.Requirements = append(result.Requirements, req)
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if rowErr != nil {
		return result, rowErr
	}
	result.Expectations, err = readTaskExpectations(ctx, tx, id)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) TransitionExecutionTask(ctx context.Context, id string, change protocol.TaskTransition) (protocol.ExecutionTask, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ExecutionTask{}, err
	}
	defer tx.Rollback()
	task, err := readExecutionTask(ctx, tx, id)
	if err != nil {
		return task, err
	}
	if task.Revision != change.ExpectedRevision {
		return task, ErrTaskConflict
	}
	if !taskStatus(change.Status) {
		return task, errors.New("invalid task status")
	}
	if change.ContinuationItemID != "" {
		var owner, kind string
		if err := tx.QueryRowContext(ctx, `SELECT thread_id,kind FROM items WHERE id=?`, change.ContinuationItemID).Scan(&owner, &kind); err != nil {
			return task, err
		}
		if owner != task.ThreadID || kind != "userMessage" {
			return task, errors.New("invalid continuation origin")
		}
		task.ContinuationItemID = change.ContinuationItemID
	}
	task.Status = change.Status
	if change.Phase != "" {
		task.Phase = change.Phase
	}
	task.Revision++
	task.UpdatedAt = time.Now().UTC()
	raw, err := taskJSON(task)
	if err != nil {
		return task, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE engine_tasks SET revision=?,status=?,data_json=? WHERE id=? AND revision=? AND current=1`, task.Revision, task.Status, raw, id, change.ExpectedRevision)
	if err != nil {
		return task, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return task, err
	}
	if n != 1 {
		return task, ErrTaskConflict
	}
	return task, tx.Commit()
}
func (s *Store) CreateTaskOperation(ctx context.Context, op protocol.TaskOperation) (protocol.TaskOperation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	op, err = createTaskOperationTx(ctx, tx, op)
	if err != nil {
		return op, err
	}
	return op, tx.Commit()
}
func createTaskOperationTx(ctx context.Context, tx *sql.Tx, op protocol.TaskOperation) (protocol.TaskOperation, error) {
	task, err := readExecutionTask(ctx, tx, op.TaskID)
	if err != nil {
		return op, err
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM turns WHERE id=?`, op.TurnID).Scan(&owner); err != nil {
		return op, err
	}
	if owner != task.ThreadID {
		return op, errors.New("operation turn belongs to another thread")
	}
	if op.ItemID != "" {
		if err := checkOperationItem(ctx, tx, task, op, op.ItemID); err != nil {
			return op, err
		}
	}
	if op.Tool == "" {
		return op, errors.New("operation tool required")
	}
	if len(op.Args) == 0 {
		op.Args = json.RawMessage(`{}`)
	}
	if !json.Valid(op.Args) {
		return op, errors.New("invalid operation arguments")
	}
	op.ArgsRef = ""
	op.ArgsSHA256 = ""
	op.ResolvedByID = ""
	op.Resolution = ""
	if len(op.Args) > 16384 {
		if op.ItemID == "" {
			return op, errors.New("large operation arguments require their original tool item")
		}
		var toolRaw []byte
		if err := tx.QueryRowContext(ctx, `SELECT tool_json FROM items WHERE id=?`, op.ItemID).Scan(&toolRaw); err != nil {
			return op, err
		}
		var stored protocol.ToolCallData
		if err := json.Unmarshal(toolRaw, &stored); err != nil {
			return op, err
		}
		// Match SaveItem's RawMessage encoding: compact whitespace and escape
		// HTML/Unicode separators without decoding or altering argument values.
		expected, err := json.Marshal(op.Args)
		if err != nil {
			return op, err
		}
		actual, err := json.Marshal(stored.Args)
		if err != nil {
			return op, err
		}
		if stored.Name != op.Tool || !bytes.Equal(expected, actual) {
			return op, errors.New("operation arguments do not match the original tool item")
		}
		hash := sha256.Sum256(actual)
		op.ArgsRef = op.ItemID
		op.ArgsSHA256 = hex.EncodeToString(hash[:])
		var args map[string]json.RawMessage
		_ = json.Unmarshal(op.Args, &args)
		resources := map[string]json.RawMessage{}
		if len(op.Resources) > 0 {
			if err := json.Unmarshal(op.Resources, &resources); err != nil {
				return op, err
			}
		}
		for _, key := range []string{"path", "workspace"} {
			if len(args[key]) > 0 && len(args[key]) <= 4096 {
				resources[key] = args[key]
			}
		}
		op.Resources, _ = json.Marshal(resources)
		op.Args = nil
	}
	op.ID = NewID("opr")
	op.Revision = 1
	op.Status = "proposed"
	op.VerificationStatus = "pending"
	op.Failure = nil
	op.EvidenceIDs = nil
	op.ResultItemID = ""
	op.SupersedesID = ""
	raw, err := taskJSON(op)
	if err != nil {
		return op, err
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT current FROM engine_tasks WHERE id=?`, task.ID).Scan(&current); err != nil {
		return op, err
	}
	if current != 1 || task.Status == "cancelled" || task.Status == "superseded" {
		return op, ErrTaskConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_operations(id,task_id,turn_id,item_id,revision,status,data_json) VALUES(?,?,?,?,?,?,?)`, op.ID, op.TaskID, op.TurnID, nullableString(op.ItemID), op.Revision, op.Status, raw)
	if err != nil {
		return op, err
	}
	return op, nil
}
func checkOperationItem(ctx context.Context, tx *sql.Tx, task protocol.ExecutionTask, op protocol.TaskOperation, id string) error {
	var thread, turn string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id,turn_id FROM items WHERE id=?`, id).Scan(&thread, &turn); err != nil {
		return err
	}
	if thread != task.ThreadID || turn != op.TurnID {
		return errors.New("operation item ownership mismatch")
	}
	return nil
}
func readOperation(ctx context.Context, q taskReader, id string) (protocol.TaskOperation, error) {
	var op protocol.TaskOperation
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT data_json FROM task_operations WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return op, ErrNotFound
	}
	if err != nil {
		return op, err
	}
	err = json.Unmarshal(raw, &op)
	return op, err
}
func (s *Store) GetTaskOperation(ctx context.Context, id string) (protocol.TaskOperation, error) {
	return readOperation(ctx, s.DB, id)
}
func (s *Store) TransitionTaskOperation(ctx context.Context, id string, change protocol.OperationTransition) (protocol.TaskOperation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.TaskOperation{}, err
	}
	defer tx.Rollback()
	op, err := readOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.Revision != change.ExpectedRevision {
		return op, ErrTaskConflict
	}
	if !operationStatus(change.Status) {
		return op, errors.New("invalid operation status")
	}
	if change.VerificationStatus != "" && !verificationStatus(change.VerificationStatus) {
		return op, errors.New("invalid verification status")
	}
	task, err := readExecutionTask(ctx, tx, op.TaskID)
	if err != nil {
		return op, err
	}
	if change.ResultItemID != "" {
		if err := checkOperationItem(ctx, tx, task, op, change.ResultItemID); err != nil {
			return op, err
		}
		op.ResultItemID = change.ResultItemID
	}
	for _, id := range change.EvidenceIDs {
		// Verification observations may be later operations of this same task.
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM items i WHERE i.id=? AND i.thread_id=? AND EXISTS(SELECT 1 FROM task_operations o WHERE o.task_id=? AND o.turn_id=i.turn_id)`, id, task.ThreadID, task.ID).Scan(&n); err != nil {
			return op, err
		}
		if n != 1 {
			return op, fmt.Errorf("evidence %s is outside task", id)
		}
	}
	if change.SupersedesID != "" {
		old, err := readOperation(ctx, tx, change.SupersedesID)
		if err != nil {
			return op, err
		}
		if old.TaskID != op.TaskID || old.ID == op.ID {
			return op, errors.New("invalid superseded operation")
		}
		op.SupersedesID = old.ID
	}
	if change.ResolvedByID != "" {
		target, err := readOperation(ctx, tx, change.ResolvedByID)
		if err != nil {
			return op, err
		}
		if target.TaskID != op.TaskID || target.ID == op.ID || target.Status != "succeeded" || target.VerificationStatus != "passed" {
			return op, errors.New("resolution requires verified evidence in the same task")
		}
		if change.Resolution != "satisfied" && change.Resolution != "superseded" && change.Resolution != "acknowledged" {
			return op, errors.New("invalid operation resolution")
		}
		op.ResolvedByID = target.ID
		op.Resolution = change.Resolution
	}
	op.Status = change.Status
	op.Revision++
	op.Failure = change.Failure
	if change.VerificationStatus != "" {
		op.VerificationStatus = change.VerificationStatus
	}
	if change.EvidenceIDs != nil {
		op.EvidenceIDs = change.EvidenceIDs
	}
	raw, err := taskJSON(op)
	if err != nil {
		return op, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE task_operations SET revision=?,status=?,data_json=? WHERE id=? AND revision=?`, op.Revision, op.Status, raw, id, change.ExpectedRevision)
	if err != nil {
		return op, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return op, err
	}
	if n != 1 {
		return op, ErrTaskConflict
	}
	return op, tx.Commit()
}

// AdoptModelDrivenTask changes only execution-policy metadata and obsolete
// interpretation prompts. Operation results and real tool permissions are untouched.
func (s *Store) AdoptModelDrivenTask(ctx context.Context, taskID string) (protocol.TaskSnapshot, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return protocol.TaskSnapshot{}, err
	}
	defer tx.Rollback()
	task, err := readExecutionTask(ctx, tx, taskID)
	if err != nil {
		return protocol.TaskSnapshot{}, err
	}
	var current bool
	if err = tx.QueryRowContext(ctx, `SELECT current FROM engine_tasks WHERE id=?`, taskID).Scan(&current); err != nil {
		return protocol.TaskSnapshot{}, err
	}
	if !current {
		return protocol.TaskSnapshot{}, ErrTaskConflict
	}
	if task.ExecutionPolicy != protocol.ModelDrivenExecutionPolicy {
		revision := task.Revision
		task.ExecutionPolicy = protocol.ModelDrivenExecutionPolicy
		task.Revision++
		task.UpdatedAt = time.Now().UTC()
		raw, encodeErr := taskJSON(task)
		if encodeErr != nil {
			return protocol.TaskSnapshot{}, encodeErr
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE engine_tasks SET revision=?,data_json=? WHERE id=? AND revision=? AND current=1`, task.Revision, raw, taskID, revision)
		if updateErr != nil {
			return protocol.TaskSnapshot{}, updateErr
		}
		n, countErr := result.RowsAffected()
		if countErr != nil {
			return protocol.TaskSnapshot{}, countErr
		}
		if n != 1 {
			return protocol.TaskSnapshot{}, ErrTaskConflict
		}
	}
	// Policy-origin requirements refer to explicit configured policy. Never expire
	// those or actual tool/workflow decisions because an interpretation was retired.
	_, err = tx.ExecContext(ctx, `UPDATE approvals SET status='expired',decided_by='model_driven_policy_migration',decided_at=? WHERE thread_id=? AND status='pending' AND (kind='task_interpretation' OR (kind='task_requirement' AND node_id IN (SELECT id FROM task_requirements WHERE task_id=? AND COALESCE(json_extract(data_json,'$.origin'),'')<>'policy')))`, Now(), task.ThreadID, taskID)
	if err != nil {
		return protocol.TaskSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return protocol.TaskSnapshot{}, err
	}
	return s.ActiveExecutionTask(ctx, task.ThreadID)
}
