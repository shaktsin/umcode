package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/credentials"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/router"
	"github.com/shaktsin/umcode/internal/secrets"
)

const anthropicCreditResponse = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`

// Exercise the real adapters, candidate resolution, routing, stream accounting,
// and persisted user-visible switch note against deterministic HTTP responses.
func TestBillingFailoverAtProviderBoundary(t *testing.T) {
	for _, tt := range []struct {
		name            string
		body            string
		pool            bool
		poolAlternative bool
		missingKey      bool
		fallbackFails   bool
		budgetSpent     bool
		wantAttempts    int
		wantStatus      string
		wantError       string
	}{
		{name: "eligible pool alternative", body: anthropicCreditResponse, pool: true, poolAlternative: true, wantAttempts: 2, wantStatus: "insufficient_credit"},
		{name: "schema error stays terminal", body: `{"type":"error","error":{"type":"invalid_request_error","message":"tools.11.custom.input_schema does not support anyOf"}}`, pool: true, poolAlternative: true, wantAttempts: 1, wantStatus: "invalid_request", wantError: "HTTP 400"},
		{name: "pool cannot escape to globally configured alternative", body: anthropicCreditResponse, pool: true, wantAttempts: 1, wantStatus: "insufficient_credit", wantError: "no other model was available"},
		{name: "pool alternative without a usable key", body: anthropicCreditResponse, pool: true, poolAlternative: true, missingKey: true, wantAttempts: 1, wantStatus: "insufficient_credit", wantError: "no other model was available"},
		{name: "explicit single configured model", body: anthropicCreditResponse, wantAttempts: 1, wantStatus: "insufficient_credit", wantError: "no other model was available"},
		{name: "all approved routes fail once", body: anthropicCreditResponse, pool: true, poolAlternative: true, fallbackFails: true, wantAttempts: 2, wantStatus: "insufficient_credit", wantError: "no other model was available"},
		{name: "spent whole-turn budget blocks all routes", body: anthropicCreditResponse, pool: true, poolAlternative: true, budgetSpent: true, wantAttempts: 0, wantError: "turn execution budget reached"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempts := make(chan string, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Errorf("decode provider request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				attempts <- payload.Model
				switch req.URL.Path {
				case "/v1/messages":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, tt.body)
				case "/chat/completions":
					if tt.fallbackFails {
						w.WriteHeader(http.StatusServiceUnavailable)
						fmt.Fprint(w, `{"error":{"message":"unavailable"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Fallback answer.\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
				default:
					t.Errorf("unexpected provider endpoint %s", req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			e, th, turn, st := pluginHookEngine(t)
			e.Cfg = config.Default(t.TempDir())
			e.LLMs = llm.NewRegistry()
			e.Creds = credentials.New(st, secrets.NewMemoryStore(), e.LLMs)
			cat, err := models.NewCatalog(st)
			if err != nil {
				t.Fatal(err)
			}
			e.Router = router.New(st, e.Creds, cat, e.LLMs, e.Cfg, e.Log)
			e.presets = models.DefaultPresets()
			for _, provider := range []string{"claude", "openai"} {
				if tt.missingKey && provider == "openai" {
					continue
				}
				record, err := e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: provider, Label: "test", Secret: "test-secret", BaseURL: server.URL})
				if err != nil {
					t.Fatal(err)
				}
				endpoint, _ := models.EndpointIdentity(provider, record.BaseURL)
				window := 200000
				model := "claude-sonnet-4-6"
				if provider == "openai" {
					model = "gpt-5.6-sol"
				}
				if err = e.Store.SetModelCapacityProfile(t.Context(), protocol.ModelCapacityProfile{Provider: provider, EndpointID: endpoint, Model: model, Backend: "generic", ContextWindow: &window}); err != nil {
					t.Fatal(err)
				}
			}
			e.routing.Models = []protocol.ConfiguredModel{{ID: "primary", Provider: "claude", Model: "claude-sonnet-4-6", Enabled: true}}
			selection := protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-4-6", Complexity: protocol.ComplexityStandard}
			if tt.pool {
				e.routing.Models = append(e.routing.Models, protocol.ConfiguredModel{ID: "fallback", Provider: "openai", Model: "gpt-5.6-sol", Enabled: true})
				members := []string{"primary"}
				if tt.poolAlternative {
					members = append(members, "fallback")
				}
				e.routing.Pools = []protocol.ModelPool{{ID: "coding", Name: "Coding", Strategy: protocol.PoolPriority, Models: members, Enabled: true}}
				selection.Provider, selection.Model = protocol.PoolProvider, "coding"
			}
			res, err := e.resolveSelection(t.Context(), th, selection, "Hello", 0)
			if err != nil {
				t.Fatal(err)
			}
			budget := newTurnBudget(protocol.ExecutionLimits{MaxDurationMinutes: 2, MaxTokens: 1_000_000, MaxCostUSD: 100})
			if tt.budgetSpent {
				budget.tokens = 1_000_000
			}
			out, err := e.callModel(t.Context(), t.Context(), turn, &res, llm.Request{MaxTokens: 100, Messages: []llm.Message{{Role: llm.RoleUser, Parts: textParts("Hello")}}}, "chat", budget)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
			} else if err != nil || out.text != "Fallback answer." {
				t.Fatalf("answer = %q, error = %v", out.text, err)
			}
			if len(attempts) != tt.wantAttempts || len(res.trail) != tt.wantAttempts {
				t.Fatalf("HTTP attempts = %d, trail = %+v", len(attempts), res.trail)
			}
			for i := 0; i < tt.wantAttempts; i++ {
				wantModel := "claude-sonnet-4-6"
				if i == 1 {
					wantModel = "gpt-5.6-sol"
				}
				if model := <-attempts; model != wantModel {
					t.Fatalf("HTTP attempt %d used %q, want %q", i+1, model, wantModel)
				}
			}
			if tt.wantAttempts > 0 && (res.trail[0].Status != tt.wantStatus || res.trail[0].Provider != "claude") {
				t.Fatalf("initial diagnostics = %+v", res.trail[0])
			}
			if tt.wantAttempts == 2 && res.trail[1].Provider != "openai" {
				t.Fatalf("fallback diagnostics = %+v", res.trail[1])
			}
			items, err := st.ListItems(t.Context(), th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			switches := 0
			for _, item := range items {
				if item.Kind == protocol.ItemInboundEvent && strings.Contains(item.Text, "Switched to") {
					switches++
					if !strings.Contains(item.Text, "insufficient provider credit") {
						t.Errorf("switch explanation = %q", item.Text)
					}
				}
			}
			wantSwitches := 0
			if tt.wantAttempts == 2 {
				wantSwitches = 1
			}
			if switches != wantSwitches {
				t.Errorf("visible switches = %d, want %d", switches, wantSwitches)
			}
		})
	}
}
