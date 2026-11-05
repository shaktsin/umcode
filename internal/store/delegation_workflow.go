package store

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/protocol"
)

type AgentWorkflowCheck = protocol.AgentWorkflowCheck
type AgentWorkflow = protocol.AgentWorkflow

func (s *Store) AgentWorkflow(ctx context.Context, taskID string) (AgentWorkflow, error) {
	var raw string
	var w AgentWorkflow
	err := s.DB.QueryRowContext(ctx, `SELECT data_json FROM agent_workflow WHERE task_id=?`, taskID).Scan(&raw)
	if err != nil {
		return w, normalizeNotFound(err)
	}
	err = json.Unmarshal([]byte(raw), &w)
	return w, err
}
