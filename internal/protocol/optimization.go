package protocol

type TokenOptimizationParams struct {
	Enabled bool `json:"enabled"`
}
type TokenOptimizationResult struct {
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
	LegacyMixed bool   `json:"legacyMixed"`
}
