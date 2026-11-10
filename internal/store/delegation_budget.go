package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/shaktsin/umcode/internal/protocol"
)

// AgentTaskBudget initializes once, then restores the task's original limits and
// cumulative actual usage. Resuming a turn cannot increase the task allowance.
func (s *Store) AgentTaskBudget(ctx context.Context, taskID string, initial protocol.ExecutionLimits) (protocol.ExecutionLimits, protocol.UsageTotals, error) {
	var limits protocol.ExecutionLimits
	var usage protocol.UsageTotals
	data, err := json.Marshal(initial)
	if err != nil {
		return limits, usage, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO agent_task_budgets(task_id,limits_json,usage_json) SELECT id,?,'{}' FROM agent_tasks WHERE id=? ON CONFLICT(task_id) DO NOTHING`, string(data), taskID)
	if err != nil {
		return limits, usage, err
	}
	return s.ReadAgentTaskBudget(ctx, taskID)
}

// ReadAgentTaskBudget never initializes a ledger or changes its limits.
func (s *Store) ReadAgentTaskBudget(ctx context.Context, taskID string) (protocol.ExecutionLimits, protocol.UsageTotals, error) {
	var limits protocol.ExecutionLimits
	var usage protocol.UsageTotals
	var l, u string
	err := s.DB.QueryRowContext(ctx, `SELECT limits_json,usage_json FROM agent_task_budgets WHERE task_id=?`, taskID).Scan(&l, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return limits, usage, ErrNotFound
	}
	if err != nil {
		return limits, usage, err
	}
	if err = json.Unmarshal([]byte(l), &limits); err != nil {
		return limits, usage, err
	}
	err = json.Unmarshal([]byte(u), &usage)
	return limits, usage, err
}

// UpdateAgentTaskBudget is called by the task's single synchronized budget
// owner. It persists cumulative actuals even after task cancellation.
func (s *Store) UpdateAgentTaskBudget(ctx context.Context, taskID string, usage protocol.UsageTotals) error {
	data, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE agent_task_budgets SET usage_json=? WHERE task_id=?`, string(data), taskID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrNotFound
	}
	return err
}
