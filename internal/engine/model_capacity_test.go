package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/credentials"
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
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
func TestUnknownLocalCapacityUsesDefaultWindow(t *testing.T) {
	e, res, _ := runtimeCapacityFixture(t, "generic", nil, func(w http.ResponseWriter, r *http.Request) { t.Error("generic metadata must not make a network call") })
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != DefaultContextWindow || got.Source != "default" {
		t.Fatalf("unknown capacity must fall back to the default window without blocking: %+v %v", got, err)
	}
}
func TestOverflowLearnsServerStatedLimit(t *testing.T) {
	e, res, _ := runtimeCapacityFixture(t, "generic", nil, func(w http.ResponseWriter, r *http.Request) {})
	failure := &llm.Error{Provider: "openai_compatible", Status: 400, Body: "This model's maximum context length is 32768 tokens"}
	e.learnContextOverflow(res, failure, requestBudgetReport{CapacitySource: "default", Window: DefaultContextWindow, EstimatedInput: 100000})
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != 32768 || got.Source != "observed" {
		t.Fatalf("server-stated limit not learned: %+v %v", got, err)
	}
	// A later guess must not replace a limit the server stated.
	e.learnContextOverflow(res, errors.New("context overflow"), requestBudgetReport{CapacitySource: "observed", Window: 32768, EstimatedInput: 20000, ReservedOutput: 8000})
	if got, _ = e.resolveCapacity(t.Context(), res); got.Window != 32768 {
		t.Fatalf("guess replaced authoritative limit: %+v", got)
	}
}
func TestOverflowWithoutStatedLimitHalvesAssumedWindow(t *testing.T) {
	e, res, _ := runtimeCapacityFixture(t, "generic", nil, func(w http.ResponseWriter, r *http.Request) {})
	e.learnContextOverflow(res, errors.New("context overflow"), requestBudgetReport{CapacitySource: "default", Window: DefaultContextWindow, EstimatedInput: 100000, ReservedOutput: 20000})
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != 60000 || got.Source != "observed" {
		t.Fatalf("assumed window not reduced: %+v %v", got, err)
	}
}
func TestOverflowNeverGuessesOverConfiguredWindow(t *testing.T) {
	override := 16384
	e, res, _ := runtimeCapacityFixture(t, "generic", &override, func(w http.ResponseWriter, r *http.Request) {})
	e.learnContextOverflow(res, errors.New("context overflow"), requestBudgetReport{CapacitySource: "override", Window: override, EstimatedInput: 10000})
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != override || got.Source != "override" {
		t.Fatalf("configured window altered by a guess: %+v %v", got, err)
	}
}
func TestSuccessfulUsageRaisesAssumedWindow(t *testing.T) {
	e, res, _ := runtimeCapacityFixture(t, "generic", nil, func(w http.ResponseWriter, r *http.Request) {})
	e.observeRequestUsage(res, requestBudgetReport{CapacitySource: "default", Window: DefaultContextWindow}, 100000, true)
	if got, _ := e.resolveCapacity(t.Context(), res); got.Window != DefaultContextWindow || got.Source != "default" {
		t.Fatalf("low usage changed the assumed window: %+v", got)
	}
	e.observeRequestUsage(res, requestBudgetReport{CapacitySource: "default", Window: DefaultContextWindow}, 240000, true)
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.Window != 300000 || got.Source != "observed" {
		t.Fatalf("usage near the assumed window did not raise it: %+v %v", got, err)
	}
}
func TestStatedLimitIsNotRaisedByUsage(t *testing.T) {
	e, res, _ := runtimeCapacityFixture(t, "generic", nil, func(w http.ResponseWriter, r *http.Request) {})
	e.learnContextOverflow(res, &llm.Error{Status: 400, Body: "maximum context length is 32768 tokens"}, requestBudgetReport{CapacitySource: "default", Window: DefaultContextWindow})
	e.observeRequestUsage(res, requestBudgetReport{CapacitySource: "observed", Window: 32768}, 32000, true)
	if got, _ := e.resolveCapacity(t.Context(), res); got.Window != 32768 {
		t.Fatalf("server-stated limit was raised: %+v", got)
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

func capacityWithWindow(t *testing.T, window int) (*Engine, resolved) {
	t.Helper()
	e, res, _ := runtimeCapacityFixture(t, "generic", &window, func(w http.ResponseWriter, r *http.Request) { t.Error("generic metadata must not make a network call") })
	return e, res
}
func TestUnknownMaxOutputUsesDefault(t *testing.T) {
	e, res := capacityWithWindow(t, 262144)
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.MaxOutput != 32768 {
		t.Fatalf("unknown max output must use the configured default: %+v %v", got, err)
	}
	prepared, _, err := budgetRequest(llm.Request{Messages: []llm.Message{llm.Text(llm.RoleUser, "hi")}}, got, got.ResponseReserve)
	if err != nil || prepared.MaxTokens != 32768 {
		t.Fatalf("prepared MaxTokens = %d, %v", prepared.MaxTokens, err)
	}
	e.Cfg.Models.DefaultMaxOutputTokens = 0
	e.invalidateCapacity(res)
	if got, err = e.resolveCapacity(t.Context(), res); err != nil || got.MaxOutput != 32768 {
		t.Fatalf("unset default must fall back to 32768: %+v %v", got, err)
	}
}
func TestKnownMaxOutputWins(t *testing.T) {
	e, res := capacityWithWindow(t, 262144)
	id, _ := models.EndpointIdentity("openai_compatible", res.cred.Record.BaseURL)
	window, output := 262144, 8192
	profile := protocol.ModelCapacityProfile{Provider: "openai_compatible", EndpointID: id, Model: res.sel.Model, Backend: "generic", ContextWindow: &window, MaxOutputTokens: &output}
	if err := e.Store.SetModelCapacityProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	e.invalidateCapacity(res)
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.MaxOutput != 8192 {
		t.Fatalf("known max output lost: %+v %v", got, err)
	}
	prepared, _, err := budgetRequest(llm.Request{Messages: []llm.Message{llm.Text(llm.RoleUser, "hi")}}, got, got.ResponseReserve)
	if err != nil || prepared.MaxTokens != 8192 {
		t.Fatalf("prepared MaxTokens = %d, %v", prepared.MaxTokens, err)
	}
}
func TestSmallWindowStaysBounded(t *testing.T) {
	e, res := capacityWithWindow(t, 4096)
	got, err := e.resolveCapacity(t.Context(), res)
	if err != nil || got.MaxOutput > got.Window {
		t.Fatalf("default output exceeded the window: %+v %v", got, err)
	}
	for _, text := range []string{"hi", strings.Repeat("word ", 1500)} {
		req := llm.Request{Messages: []llm.Message{llm.Text(llm.RoleUser, text)}}
		prepared, report, err := budgetRequest(req, got, got.ResponseReserve)
		if err != nil {
			var capacityErr *requestCapacityError
			if !errors.As(err, &capacityErr) {
				t.Fatalf("unexpected error: %v", err)
			}
			continue
		}
		if report.EstimatedInput+prepared.MaxTokens > got.Window {
			t.Fatalf("input %d + output %d exceeds window %d", report.EstimatedInput, prepared.MaxTokens, got.Window)
		}
	}
}

func TestEvaluationModelOutputLimitReachesCapacity(t *testing.T) {
	for _, tc := range []struct {
		name           string
		window, output int64
		want           int
	}{
		{"declared limit below the default", 200000, 9000, 9000},
		{"window caps the declared limit", 5000, 9000, 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
			e.LLMs.Register(&evaluationProvider{inner: &evalStreamFixture{}, synthetic: true, model: evaluation.Model{ID: "deterministic", Window: tc.window, MaxOutput: tc.output}})
			cred, err := e.Creds.Resolve(t.Context(), "claude", "")
			if err != nil {
				t.Fatal(err)
			}
			res := resolved{sel: protocol.ModelSelection{Provider: "claude", Model: "deterministic", CredentialID: cred.Record.ID}, cred: cred, meta: models.Meta{Provider: "claude", ID: "deterministic"}}
			got, err := e.resolveCapacity(t.Context(), res)
			if err != nil || got.MaxOutput != tc.want {
				t.Fatalf("capacity = %+v, %v; want max output %d", got, err, tc.want)
			}
		})
	}
}
