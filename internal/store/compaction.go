package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"time"
)

var ErrStaleCompaction = errors.New("stale compaction lifecycle update")

func (s *Store) SaveCompactionState(ctx context.Context, state protocol.CompactionState) error {
	if state.ID == "" || state.ThreadID == "" || state.TurnID == "" || state.Sequence < 1 || state.StartedAt.IsZero() || state.UpdatedAt.Before(state.StartedAt) {
		return errors.New("invalid compaction state")
	}
	switch state.Status {
	case "preparing", "summarizing", "restoring", "completed", "failed", "cancelled", "interrupted":
	default:
		return errors.New("invalid compaction status")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO compaction_runs(thread_id,id,sequence,status,started_order,data_json) VALUES(?,?,?,?,?,?) ON CONFLICT(thread_id) DO UPDATE SET id=excluded.id,sequence=excluded.sequence,status=excluded.status,started_order=excluded.started_order,data_json=excluded.data_json WHERE (compaction_runs.id=excluded.id AND excluded.sequence>compaction_runs.sequence AND compaction_runs.status IN ('preparing','summarizing','restoring')) OR (compaction_runs.id<>excluded.id AND excluded.started_order>compaction_runs.started_order AND excluded.sequence=1)`, state.ThreadID, state.ID, state.Sequence, state.Status, state.StartedAt.UnixNano(), raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStaleCompaction
	}
	return nil
}
func (s *Store) LatestCompactionState(ctx context.Context, threadID string) (*protocol.CompactionState, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT data_json FROM compaction_runs WHERE thread_id=?`, threadID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state protocol.CompactionState
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	return &state, nil
}
func (s *Store) ReconcileCompactions(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT data_json FROM compaction_runs WHERE status IN ('preparing','summarizing','restoring')`)
	if err != nil {
		return err
	}
	var states []protocol.CompactionState
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var state protocol.CompactionState
		if err = json.Unmarshal(raw, &state); err != nil {
			rows.Close()
			return err
		}
		states = append(states, state)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, state := range states {
		state.Sequence++
		state.Status = "interrupted"
		state.Error = "Automatic compaction was interrupted by shutdown."
		state.UpdatedAt = time.Now().UTC()
		if err = s.SaveCompactionState(ctx, state); err != nil {
			return err
		}
	}
	return nil
}
