package protocol

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const MethodModelCapacityGet = "model/capacity/get"
const MethodModelCapacitySet = "model/capacity/set"

type ModelCapacityProfile struct {
	Provider              string `json:"provider"`
	EndpointID            string `json:"endpointId"`
	Model                 string `json:"model"`
	Backend               string `json:"backend"`
	ContextWindow         *int   `json:"contextWindow,omitempty"`
	MaxOutputTokens       *int   `json:"maxOutputTokens,omitempty"`
	ResponseReserveTokens *int   `json:"responseReserveTokens,omitempty"`
}

func (p ModelCapacityProfile) Validate() error {
	if strings.TrimSpace(p.Provider) == "" || strings.TrimSpace(p.Model) == "" {
		return errors.New("provider and model are required")
	}
	decoded, err := hex.DecodeString(p.EndpointID)
	if err != nil || len(decoded) != 32 {
		return errors.New("invalid endpoint identity")
	}
	if p.Backend != "generic" && p.Backend != "ollama" {
		return errors.New("unsupported capacity backend")
	}
	if p.Backend == "ollama" && p.Provider != "openai_compatible" {
		return errors.New("Ollama discovery requires an OpenAI-compatible endpoint")
	}
	for _, n := range []*int{p.ContextWindow, p.MaxOutputTokens, p.ResponseReserveTokens} {
		if n != nil && (*n <= 0 || int64(*n) > 2147483647) {
			return errors.New("capacity values must be positive integers no larger than 2147483647")
		}
	}
	if p.ContextWindow != nil {
		for _, n := range []*int{p.MaxOutputTokens, p.ResponseReserveTokens} {
			if n != nil && *n > *p.ContextWindow {
				return errors.New("response allowance cannot exceed context window")
			}
		}
	}
	return nil
}

type ModelCapacityParams struct {
	CredentialID string                `json:"credentialId"`
	Model        string                `json:"model"`
	Profile      *ModelCapacityProfile `json:"profile,omitempty"`
	Refresh      bool                  `json:"refresh,omitempty"`
}
type ModelCapacityResult struct {
	Profile                ModelCapacityProfile `json:"profile"`
	EffectiveContextWindow *int                 `json:"effectiveContextWindow,omitempty"`
	Source                 string               `json:"source"`
	ObservedAt             *time.Time           `json:"observedAt,omitempty"`
}
