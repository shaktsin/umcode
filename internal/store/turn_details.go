package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/protocol"
)

// SaveTurnDiagnostics stores a bounded independent copy of a versioned record.
func (s *Store) SaveTurnDiagnostics(ctx context.Context, turnID string, d protocol.TurnDiagnostics) error {
	if d.Version != 1 {
		return fmt.Errorf("unsupported turn diagnostic version %d", d.Version)
	}
	// JSON copy prevents normalization from changing caller-owned slices/maps.
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	var cloned protocol.TurnDiagnostics
	if err = json.Unmarshal(raw, &cloned); err != nil {
		return err
	}
	d = cloned
	if len(d.Requests) > 128 {
		d.OmittedRequests += len(d.Requests) - 128
		d.Requests = d.Requests[:128]
		d.Complete = false
	}
	for i := range d.Requests {
		r := &d.Requests[i]
		if len(r.SelectedSources) > 128 {
			r.OmittedSelectedSources += len(r.SelectedSources) - 128
			r.SelectedSources = r.SelectedSources[:128]
			d.Complete = false
		}
		if len(r.SubmittedSources) > 128 {
			r.OmittedSubmittedSources += len(r.SubmittedSources) - 128
			r.SubmittedSources = r.SubmittedSources[:128]
			d.Complete = false
		}
		if len(r.ToolNames) > 128 {
			r.OmittedTools += len(r.ToolNames) - 128
			r.ToolNames = r.ToolNames[:128]
			d.Complete = false
		}
	}
	raw, err = json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO turn_diagnostics(turn_id,data_json) VALUES(?,?) ON CONFLICT(turn_id) DO UPDATE SET data_json=excluded.data_json`, turnID, string(raw))
	return err
}

func (s *Store) ReadTurnDetails(ctx context.Context, p protocol.TurnDetailsParams) (protocol.TurnDetailsResult, error) {
	var out protocol.TurnDetailsResult
	// Existing turn loader includes recorded usage and the route trail.
	turns, err := s.ListTurns(ctx, p.ThreadID)
	if err != nil {
		return out, err
	}
	found := false
	for _, turn := range turns {
		if turn.ID == p.TurnID {
			out.Turn = turn
			found = true
			break
		}
	}
	if !found {
		return out, ErrNotFound
	}
	out.Availability = "not_recorded"
	var raw string
	err = s.DB.QueryRowContext(ctx, `SELECT data_json FROM turn_diagnostics WHERE turn_id=?`, p.TurnID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var d protocol.TurnDiagnostics
	if err = json.Unmarshal([]byte(raw), &d); err != nil {
		return out, err
	}
	if d.Version != 1 {
		return out, fmt.Errorf("unsupported turn diagnostic version %d", d.Version)
	}
	out.Diagnostics = &d
	out.Availability = "recorded"
	if !d.Complete {
		out.Availability = "partial"
	}
	return out, nil
}
