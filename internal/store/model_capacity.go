package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
)

func (s *Store) GetModelCapacityProfile(ctx context.Context, provider, endpointID, model string) (protocol.ModelCapacityProfile, error) {
	var p protocol.ModelCapacityProfile
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT data_json FROM model_capacity_profiles WHERE provider=? AND endpoint_id=? AND model=?`, provider, endpointID, model).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}
func (s *Store) SetModelCapacityProfile(ctx context.Context, p protocol.ModelCapacityProfile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO model_capacity_profiles(provider,endpoint_id,model,data_json,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(provider,endpoint_id,model) DO UPDATE SET data_json=excluded.data_json,updated_at=excluded.updated_at`, p.Provider, p.EndpointID, p.Model, raw, Now())
	return err
}
