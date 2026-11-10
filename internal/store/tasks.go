package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// PyTime formats t the way the Python app stores task times
// ("2026-09-18T07:00:00.000000Z"), so both engines compare them correctly.
func PyTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000") + "Z" }

func pyNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return PyTime(*t)
}

const taskCols = `id, name, prompt, task_type, schedule_json, timezone, status, next_run_at, last_run_at,
	COALESCE(last_result, ''), COALESCE(last_error, ''), created_by, created_at,
	provider, model, complexity, credential_id, thread_id`

func scanTask(sc interface{ Scan(...any) error }) (protocol.Task, error) {
	var t protocol.Task
	var sched, created, cplx string
	var next, last sql.NullString
	err := sc.Scan(&t.ID, &t.Name, &t.Prompt, &t.TaskType, &sched, &t.Timezone, &t.Status, &next, &last,
		&t.LastResult, &t.LastError, &t.CreatedBy, &created,
		&t.Settings.Provider, &t.Settings.Model, &cplx, &t.Settings.CredentialID, &t.ThreadID)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal([]byte(sched), &t.Schedule)
	t.Settings.Complexity = protocol.NormalizeComplexity(cplx)
	t.NextRunAt, t.LastRunAt = nullTime(next), nullTime(last)
	t.CreatedAt = ParseTime(created)
	return t, nil
}

// CreateTask inserts an active task.
func (s *Store) CreateTask(ctx context.Context, t protocol.Task) (protocol.Task, error) {
	sched, err := json.Marshal(t.Schedule)
	if err != nil {
		return t, err
	}
	now := time.Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO tasks (name, prompt, task_type, schedule_json, timezone, status,
		next_run_at, created_by, created_at, updated_at, provider, model, complexity, credential_id, thread_id)
		VALUES (?,?,?,?,?,'active',?,?,?,?,?,?,?,?,?)`,
		t.Name, t.Prompt, t.TaskType, string(sched), t.Timezone, pyNullTime(t.NextRunAt), t.CreatedBy,
		PyTime(now), PyTime(now), t.Settings.Provider, t.Settings.Model, string(t.Settings.Complexity),
		t.Settings.CredentialID, t.ThreadID)
	if err != nil {
		return t, err
	}
	id, _ := res.LastInsertId()
	return s.GetTask(ctx, id)
}

// GetTask loads one task.
func (s *Store) GetTask(ctx context.Context, id int64) (protocol.Task, error) {
	t, err := scanTask(s.DB.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// ListTasks returns tasks, newest first, optionally filtered by status.
func (s *Store) ListTasks(ctx context.Context, status string) ([]protocol.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CancelTask marks a task cancelled. It returns ErrNotFound if it does not exist
// or was already cancelled.
func (s *Store) CancelTask(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE tasks SET status = 'cancelled', lease_until = NULL, updated_at = ?
		WHERE id = ? AND status != 'cancelled'`, PyTime(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTaskThread records the thread a task reports into.
func (s *Store) SetTaskThread(ctx context.Context, id int64, threadID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE tasks SET thread_id = ?, updated_at = ? WHERE id = ?`, threadID, PyTime(time.Now()), id)
	return err
}

// LeaseDueTasks claims up to limit active tasks whose next run is due, the
// same way the Python scheduler does, so the two never run a task twice.
func (s *Store) LeaseDueTasks(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]protocol.Task, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowS := PyTime(now)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE status = 'active' AND next_run_at IS NOT NULL
		AND next_run_at <= ? AND (lease_until IS NULL OR lease_until < ?) ORDER BY next_run_at, id LIMIT ?`, nowS, nowS, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []protocol.Task
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET lease_until = ?, updated_at = ? WHERE id = ?`,
			PyTime(now.Add(lease)), nowS, id); err != nil {
			return nil, err
		}
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, tx.Commit()
}

// StartTaskRun records a running run.
func (s *Store) StartTaskRun(ctx context.Context, taskID int64, turnID string) (int64, error) {
	now := PyTime(time.Now())
	res, err := s.DB.ExecContext(ctx, `INSERT INTO task_runs (task_id, status, started_at, turn_id) VALUES (?, 'running', ?, ?)`,
		taskID, now, turnID)
	if err != nil {
		return 0, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE tasks SET last_run_at = ?, updated_at = ? WHERE id = ?`, now, now, taskID); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetTaskRunTurn links a run to its turn.
func (s *Store) SetTaskRunTurn(ctx context.Context, runID int64, turnID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE task_runs SET turn_id = ? WHERE id = ?`, turnID, runID)
	return err
}

// FinishTaskRun records a run's outcome and the task's next run. A nil next
// with terminal=true completes the task.
func (s *Store) FinishTaskRun(ctx context.Context, runID, taskID int64, ok bool, result, errText string, next *time.Time, terminal bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := PyTime(time.Now())
	runStatus := "success"
	if !ok {
		runStatus = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET status = ?, finished_at = ?, result = ?, error = ? WHERE id = ?`,
		runStatus, now, nullIfEmpty(result), nullIfEmpty(errText), runID); err != nil {
		return err
	}
	status := protocol.TaskActive
	if terminal {
		status = protocol.TaskCompleted
	}
	// A task cancelled while it was running stays cancelled.
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = CASE WHEN status = 'cancelled' THEN 'cancelled' ELSE ? END,
		next_run_at = ?, lease_until = NULL, last_result = COALESCE(?, last_result), last_error = ?, updated_at = ?
		WHERE id = ?`, status, pyNullTime(next), nullIfEmpty(result), nullIfEmpty(errText), now, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

// ReleaseTaskLease clears a lease without running.
func (s *Store) ReleaseTaskLease(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE tasks SET lease_until = NULL, updated_at = ? WHERE id = ?`, PyTime(time.Now()), id)
	return err
}

// SetTaskNextRun sets next_run_at (used by "run now").
func (s *Store) SetTaskNextRun(ctx context.Context, id int64, next time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE tasks SET next_run_at = ?, updated_at = ? WHERE id = ? AND status = 'active'`,
		PyTime(next), PyTime(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListTaskRuns returns a task's runs, newest first.
func (s *Store) ListTaskRuns(ctx context.Context, taskID int64, limit int) ([]protocol.TaskRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, task_id, status, turn_id, started_at, finished_at,
		COALESCE(result, ''), COALESCE(error, '') FROM task_runs WHERE task_id = ? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.TaskRun
	for rows.Next() {
		var r protocol.TaskRun
		var started string
		var fin sql.NullString
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Status, &r.TurnID, &started, &fin, &r.Result, &r.Error); err != nil {
			return nil, err
		}
		r.StartedAt, r.FinishedAt = ParseTime(started), nullTime(fin)
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
