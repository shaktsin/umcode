package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// runtimeMirrorCallID is the unique non-empty call ID of a runtime agent's
// mirror row in agent_children.
func runtimeMirrorCallID(agentID string) string { return "rt:" + agentID }

// EnsureRuntimeAgentExecution gives a runtime agent an execution identity by
// reusing the legacy execution tables. It inserts an inert mirror row into
// agent_children (id = agent ID, role implementer, state following the agent;
// UpdateRuntimeAgent keeps it in step) plus the internal thread and
// agent_executions row, exactly as EnsureAgentExecution does. Legacy delegation
// reads (AgentChild, AgentChildren) hide mirror rows, so no legacy path treats
// them as work. It is idempotent: repeating it returns the stored execution.
func (s *Store) EnsureRuntimeAgentExecution(ctx context.Context, a protocol.RuntimeAgent) (AgentExecution, error) {
	if x, err := s.AgentExecution(ctx, a.TaskID, a.ID); err == nil {
		return x, nil
	} else if !errors.Is(err, ErrNotFound) {
		return x, err
	}
	task, err := s.AgentTask(ctx, a.TaskID)
	if err != nil {
		return AgentExecution{}, err
	}
	parent, err := s.GetThread(ctx, task.ThreadID)
	if err != nil {
		return AgentExecution{}, err
	}
	msgs := []llm.Message{}
	data, err := json.Marshal(msgs)
	if err != nil {
		return AgentExecution{}, err
	}
	assignment, err := json.Marshal(a.Assignment)
	if err != nil {
		return AgentExecution{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AgentExecution{}, err
	}
	defer tx.Rollback()
	// The agent must exist in this task; its state is the mirror's state.
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM agents WHERE task_id=? AND id=?`, a.TaskID, a.ID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AgentExecution{}, ErrNotFound
		}
		return AgentExecution{}, err
	}
	now := Now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_children(id,task_id,call_id,role,assignment_json,state,result_json,usage_json,model_json,version,created_at,updated_at) VALUES(?,?,?,'implementer',?,?,'{}','{}','{}',1,?,?) ON CONFLICT(id) DO NOTHING`,
		a.ID, a.TaskID, runtimeMirrorCallID(a.ID), string(assignment), state, now, now); err != nil {
		return AgentExecution{}, err
	}
	id, err := insertAgentExecution(ctx, tx, a.TaskID, a.ID, parent, string(data))
	if err != nil {
		if x, rerr := s.AgentExecution(ctx, a.TaskID, a.ID); rerr == nil {
			return x, nil // lost a concurrent race
		}
		return AgentExecution{}, err
	}
	if err = tx.Commit(); err != nil {
		return AgentExecution{}, err
	}
	return AgentExecution{ThreadID: id, Context: msgs}, nil
}

// RuntimeAgentIDForThread resolves an internal execution thread to the runtime
// agent that owns it; any other thread is ErrNotFound.
func (s *Store) RuntimeAgentIDForThread(ctx context.Context, threadID string) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT a.id FROM threads th JOIN agents a ON a.id=th.internal_child_id WHERE th.id=?`, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
