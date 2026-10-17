package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

type internalThreadsKey struct{}

// WithInternalThreads is for engine-owned child execution only, never an RPC argument.
func WithInternalThreads(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalThreadsKey{}, true)
}
func internalThreads(ctx context.Context) bool {
	v, _ := ctx.Value(internalThreadsKey{}).(bool)
	return v
}

// AgentExecution is accessible only through a task/child-scoped lookup. Context
// is the exact bounded initial input, and Model is the last actual route used.
type AgentExecution struct {
	ThreadID string
	Context  []llm.Message
	Model    protocol.ModelSelection
}

// AgentModelScope freezes the root request's approved routes without secrets.
type AgentModelScope struct {
	Provider, Model, PoolID, Strategy, CredentialID string
	Explicit                                        bool
	Candidates                                      []protocol.ConfiguredModel
	CredentialIDs                                   []string
}

func (s *Store) CreateAgentModelScope(ctx context.Context, taskID string, scope AgentModelScope) error {
	data, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO agent_model_scopes(task_id,scope_json) VALUES(?,?)`, taskID, string(data))
	return err
}
func (s *Store) AgentModelScope(ctx context.Context, taskID string) (AgentModelScope, error) {
	var scope AgentModelScope
	var data string
	err := s.DB.QueryRowContext(ctx, `SELECT scope_json FROM agent_model_scopes WHERE task_id=?`, taskID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return scope, ErrNotFound
	}
	if err != nil {
		return scope, err
	}
	err = json.Unmarshal([]byte(data), &scope)
	return scope, err
}

func (s *Store) AgentExecution(ctx context.Context, taskID, childID string) (AgentExecution, error) {
	var x AgentExecution
	var supplied, model string
	err := s.DB.QueryRowContext(ctx, `SELECT thread_id,context_json,model_json FROM agent_executions WHERE task_id=? AND child_id=?`, taskID, childID).Scan(&x.ThreadID, &supplied, &model)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	if err = json.Unmarshal([]byte(supplied), &x.Context); err != nil {
		return x, err
	}
	err = json.Unmarshal([]byte(model), &x.Model)
	return x, err
}

func (s *Store) EnsureAgentExecution(ctx context.Context, task protocol.AgentTask, child protocol.AgentChild, parent protocol.Thread, msgs []llm.Message) (AgentExecution, error) {
	if x, err := s.AgentExecution(ctx, task.ID, child.ID); err == nil {
		return x, nil
	} else if !errors.Is(err, ErrNotFound) {
		return x, err
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		return AgentExecution{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AgentExecution{}, err
	}
	defer tx.Rollback()
	id := NewID("thr")
	now := Now()
	result, err := tx.ExecContext(ctx, `INSERT INTO threads (`+threadCols+`,internal_child_id) SELECT ?,?,th.project_id,'local',th.channel,0,0,?,?,?,?,th.approval_mode,'',?,?,c.id FROM threads th JOIN agent_tasks t ON t.thread_id=th.id JOIN agent_children c ON c.task_id=t.id WHERE t.id=? AND c.id=? AND th.id=? AND th.internal_child_id=''`, id, "Agent execution", parent.Settings.Provider, parent.Settings.Model, string(parent.Settings.Complexity), parent.Settings.CredentialID, now, now, task.ID, child.ID, parent.ID)
	if err != nil {
		return AgentExecution{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return AgentExecution{}, ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_executions(child_id,task_id,thread_id,context_json) VALUES(?,?,?,?)`, child.ID, task.ID, id, string(data))
	if err != nil {
		return AgentExecution{}, err
	}
	if err = tx.Commit(); err != nil {
		return AgentExecution{}, err
	}
	return AgentExecution{ThreadID: id, Context: msgs}, nil
}

func (s *Store) UpdateAgentExecutionModel(ctx context.Context, taskID, childID string, model protocol.ModelSelection) error {
	data, err := json.Marshal(model)
	if err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE agent_executions SET model_json=? WHERE task_id=? AND child_id=?`, string(data), taskID, childID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) IsInternalThread(ctx context.Context, id string) (bool, error) {
	var child string
	err := s.DB.QueryRowContext(ctx, `SELECT internal_child_id FROM threads WHERE id=?`, id).Scan(&child)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return child != "", err
}
