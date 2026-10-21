package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"strings"
)

func defaultCapacityEndpoint(provider string) string {
	switch provider {
	case "claude":
		return "https://api.anthropic.com"
	case "openai":
		return "https://api.openai.com/v1"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta"
	case "openai_compatible":
		return "http://localhost:11434/v1"
	default:
		return ""
	}
}
func (e *Engine) capacityProfile(ctx context.Context, p protocol.ModelCapacityParams) (protocol.ModelCapacityProfile, error) {
	if p.CredentialID == "" || strings.TrimSpace(p.Model) == "" {
		return protocol.ModelCapacityProfile{}, protocol.Errorf(protocol.CodeInvalidParams, "credentialId and model are required")
	}
	cred, err := e.Store.GetCredential(ctx, p.CredentialID)
	if err != nil {
		return protocol.ModelCapacityProfile{}, err
	}
	if !cred.Enabled {
		return protocol.ModelCapacityProfile{}, protocol.Errorf(protocol.CodeInvalidParams, "credential is disabled")
	}
	endpoint := cred.BaseURL
	if endpoint == "" {
		endpoint = defaultCapacityEndpoint(cred.Provider)
	}
	identity, err := models.EndpointIdentity(cred.Provider, endpoint)
	if err != nil {
		return protocol.ModelCapacityProfile{}, err
	}
	profile, err := e.Store.GetModelCapacityProfile(ctx, cred.Provider, identity, strings.TrimSpace(p.Model))
	if errors.Is(err, store.ErrNotFound) {
		return protocol.ModelCapacityProfile{Provider: cred.Provider, EndpointID: identity, Model: strings.TrimSpace(p.Model), Backend: "generic"}, nil
	}
	return profile, err
}
func (e *Engine) ModelCapacity(ctx context.Context, p protocol.ModelCapacityParams) (protocol.ModelCapacityResult, error) {
	profile, err := e.capacityProfile(ctx, p)
	if err != nil {
		return protocol.ModelCapacityResult{}, err
	}
	result := protocol.ModelCapacityResult{Profile: profile, Source: "unavailable"}
	if profile.ContextWindow != nil {
		result.EffectiveContextWindow = profile.ContextWindow
		result.Source = "override"
	}
	return result, nil
}
func (e *Engine) SetModelCapacity(ctx context.Context, p protocol.ModelCapacityParams) (protocol.ModelCapacityResult, error) {
	current, err := e.capacityProfile(ctx, p)
	if err != nil {
		return protocol.ModelCapacityResult{}, err
	}
	profile := current
	if p.Profile != nil {
		profile = *p.Profile
		if profile.Provider != current.Provider || profile.EndpointID != current.EndpointID || profile.Model != current.Model {
			return protocol.ModelCapacityResult{}, protocol.Errorf(protocol.CodeInvalidParams, "capacity profile does not match the selected endpoint and model")
		}
	}
	if err = profile.Validate(); err != nil {
		return protocol.ModelCapacityResult{}, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
	}
	if err = e.Store.SetModelCapacityProfile(ctx, profile); err != nil {
		return protocol.ModelCapacityResult{}, err
	}
	return e.ModelCapacity(ctx, p)
}
