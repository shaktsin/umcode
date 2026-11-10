package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// AgentPlanSource fixes the selected checkout and its visible state at card
// creation. Approval must compare both fields before admitting any task.
type AgentPlanSource struct {
	PlanID     string          `json:"planId"`
	SourceRoot string          `json:"sourceRoot"`
	Manifest   json.RawMessage `json:"manifest"`
}

func (s *Store) AgentPlanSource(ctx context.Context, planID string) (AgentPlanSource, error) {
	var p AgentPlanSource
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT plan_id,source_root,manifest_json FROM agent_plan_sources WHERE plan_id=?`, planID).
		Scan(&p.PlanID, &p.SourceRoot, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Manifest = json.RawMessage(raw)
	return p, nil
}

// SaveAgentPlanSource is idempotent only for byte-identical source authority.
// A later proposal must use a new plan ID rather than altering what the user
// saw on the card.
func (s *Store) SaveAgentPlanSource(ctx context.Context, p AgentPlanSource) error {
	if p.PlanID == "" || !filepath.IsAbs(p.SourceRoot) || !json.Valid(p.Manifest) {
		return fmt.Errorf("%w: invalid agent plan source", ErrAgentTransition)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var root, raw string
	err = tx.QueryRowContext(ctx, `SELECT source_root,manifest_json FROM agent_plan_sources WHERE plan_id=?`, p.PlanID).Scan(&root, &raw)
	if err == nil {
		if root == p.SourceRoot && bytes.Equal([]byte(raw), p.Manifest) {
			return nil
		}
		return fmt.Errorf("%w: agent plan source changed", ErrAgentConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_plan_sources(plan_id,source_root,manifest_json,created_at) VALUES(?,?,?,?)`, p.PlanID, p.SourceRoot, string(p.Manifest), Now()); err != nil {
		return err
	}
	return tx.Commit()
}
