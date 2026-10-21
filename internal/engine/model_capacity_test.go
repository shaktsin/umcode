package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/credentials"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCapacitySettingsRejectInvalidWindow(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	cred, err := e.Creds.Resolve(t.Context(), "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	params := protocol.ModelCapacityParams{CredentialID: cred.Record.ID, Model: "deterministic"}
	initial, err := e.ModelCapacity(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	p := initial.Profile
	zero := 0
	p.ContextWindow = &zero
	params.Profile = &p
	if _, err = e.SetModelCapacity(t.Context(), params); err == nil {
		t.Fatal("zero context accepted")
	}
	positive := 32768
	p.ContextWindow = &positive
	p.EndpointID = "forged"
	if _, err = e.SetModelCapacity(t.Context(), params); err == nil {
		t.Fatal("foreign endpoint profile accepted")
	}
	p = initial.Profile
	p.ContextWindow = &positive
	params.Profile = &p
	saved, err := e.SetModelCapacity(t.Context(), params)
	if err != nil || saved.Profile.ContextWindow == nil || *saved.Profile.ContextWindow != 32768 {
		t.Fatalf("valid override rejected: %+v %v", saved, err)
	}
}

type capacityFixtureProvider struct{ taskScriptProvider }

func (*capacityFixtureProvider) ID() string { return "openai_compatible" }
func runtimeCapacityFixture(t *testing.T, backend string, window *int, handler http.HandlerFunc) (*Engine, resolved, *httptest.Server) {
	t.Helper()
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	e.LLMs.Register(&capacityFixtureProvider{})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	record, err := e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "openai_compatible", Secret: "local-token", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := e.Creds.Resolve(t.Context(), "openai_compatible", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := models.EndpointIdentity("openai_compatible", record.BaseURL)
	profile := protocol.ModelCapacityProfile{Provider: "openai_compatible", EndpointID: id, Model: "qwen3-coder", Backend: backend, ContextWindow: window}
	if err = e.Store.SetModelCapacityProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	res := resolved{sel: protocol.ModelSelection{Provider: "openai_compatible", Model: "qwen3-coder", CredentialID: record.ID}, cred: credentials.Resolved{Record: record, Material: cred.Material}, meta: models.Meta{Provider: "openai_compatible", ID: "qwen3-coder", ContextWindow: 131072}}
	return e, res, srv
}
func TestCapacityUsesLowerRuntimeAndOverride(t *testing.T) {
	override := 16384
	e, res, _ := runtimeCapacityFixture(t, "ollama", &override, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3-coder","context_length":32768}]}`))
	})
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != 16384 {
		t.Fatalf("configured lower capacity lost: %+v %v", got, err)
	}
	override = 65536
	e.invalidateCapacity(res)
	got, err = e.resolveCapacity(t.Context(), res)
	// Profiles store values, not pointers into a caller's mutable memory.
	if err != nil || got.Window != 16384 {
		t.Fatalf("persisted override changed by caller: %+v %v", got, err)
	}
	profile, err := e.Store.GetModelCapacityProfile(t.Context(), res.sel.Provider, got.EndpointID, res.sel.Model)
	if err != nil {
		t.Fatal(err)
	}
	profile.ContextWindow = &override
	if err = e.Store.SetModelCapacityProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	e.invalidateCapacity(res)
	got, err = e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != 32768 {
		t.Fatalf("override exceeded loaded runtime allocation: %+v %v", got, err)
	}
}
func TestCapacityDiscoveryFailurePreservesOverride(t *testing.T) {
	override := 16384
	e, res, _ := runtimeCapacityFixture(t, "ollama", &override, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline", http.StatusServiceUnavailable) })
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != 16384 || got.Source != "override" {
		t.Fatalf("discovery failure discarded valid override: %+v %v", got, err)
	}
}
func TestUnknownLocalCapacityIsActionable(t *testing.T) {
	e, res, _ := runtimeCapacityFixture(t, "generic", nil, func(w http.ResponseWriter, r *http.Request) { t.Error("generic metadata must not make a network call") })
	_, err := e.resolveCapacity(t.Context(), res)
	if !errors.Is(err, ErrCapacityUnknown) {
		t.Fatalf("advertised maximum substituted for actual allocation: %v", err)
	}
}
func TestCapacityRefreshInvalidatesPriorAllocation(t *testing.T) {
	var window atomic.Int64
	window.Store(32768)
	var calls atomic.Int32
	e, res, _ := runtimeCapacityFixture(t, "ollama", nil, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `{"models":[{"name":"qwen3-coder","context_length":%d}]}`, window.Load())
	})
	a, err := e.resolveCapacity(t.Context(), res)
	if err != nil || a.Window != 32768 {
		t.Fatal(a, err)
	}
	_, err = e.resolveCapacity(t.Context(), res)
	if err != nil || calls.Load() != 1 {
		t.Fatal("within-turn metadata was not cached", err)
	}
	window.Store(8192)
	refreshed, err := e.ModelCapacity(t.Context(), protocol.ModelCapacityParams{CredentialID: res.cred.Record.ID, Model: res.sel.Model, Refresh: true})
	if err != nil || refreshed.EffectiveContextWindow == nil || *refreshed.EffectiveContextWindow != 8192 || calls.Load() != 2 {
		t.Fatalf("refresh kept stale runtime allocation: %+v calls=%d error=%v", refreshed, calls.Load(), err)
	}
}

func (*capacityFixtureProvider) RuntimeCapacity(context.Context, llm.Credential, string) (llm.RuntimeCapacity, error) {
	return llm.RuntimeCapacity{}, llm.ErrRuntimeCapacityUnavailable
}
