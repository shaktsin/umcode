package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type TokenOptimizationParams struct {
	Enabled bool `json:"enabled"`
}
type TokenOptimizationResult struct {
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
	LegacyMixed bool   `json:"legacyMixed"`
}

// Require a real boolean, so null/missing fields cannot silently turn the
// bundle off and component overrides are unavailable through product RPC.
func (p *TokenOptimizationParams) UnmarshalJSON(raw []byte) error {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	if input.Enabled == nil {
		return fmt.Errorf("enabled must be a boolean")
	}
	p.Enabled = *input.Enabled
	return nil
}
