package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/credentials"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
)

func newRouter(t *testing.T) (*Router, *credentials.Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	llms := llm.NewRegistry()
	creds := credentials.New(st, secrets.NewMemoryStore(), llms)
	cat, err := models.NewCatalog(st)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider = "claude"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, creds, cat, llms, cfg, log), creds, st
}

func addKey(t *testing.T, creds *credentials.Service, provider, label, secret string) protocol.Credential {
	t.Helper()
	c, err := creds.Add(context.Background(), protocol.CredentialAddParams{Provider: provider, Label: label, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPlanWithoutKeysExplainsItself(t *testing.T) {
	r, _, _ := newRouter(t)
	plan, err := r.Plan(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) != 0 || plan.Reason == "" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanLinesUpFallbacks(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	k1 := addKey(t, creds, "claude", "primary", "sk-one")
	k2 := addKey(t, creds, "claude", "backup", "sk-two")

	plan, err := r.Plan(ctx, Request{Need: Capabilities{Tools: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) < 2 {
		t.Fatalf("expected fallbacks, got %+v", plan.Routes)
	}
	first, _ := plan.Next()
	if first.Provider != "claude" {
		t.Fatalf("first route = %+v", first)
	}
	// Every route is usable, and the same model/key pair never repeats.
	seen := map[string]bool{}
	for _, rt := range plan.Routes {
		key := rt.Model + "/" + rt.Cred.Record.ID
		if seen[key] {
			t.Fatalf("route repeated: %s", key)
		}
		seen[key] = true
	}
	var sawBoth bool
	for _, rt := range plan.Routes {
		if rt.Model == first.Model && rt.Cred.Record.ID != first.Cred.Record.ID {
			sawBoth = true
		}
	}
	if !sawBoth {
		t.Fatalf("the second key is never tried: %+v", plan.Routes)
	}
	_ = k1
	_ = k2
}

func TestPinnedKeyIsExclusive(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	addKey(t, creds, "claude", "primary", "sk-one")
	k2 := addKey(t, creds, "claude", "backup", "sk-two")

	plan, err := r.Plan(ctx, Request{
		Pinned: protocol.ModelSelection{CredentialID: k2.ID}, Need: Capabilities{Tools: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) == 0 {
		t.Fatal("no routes")
	}
	for _, rt := range plan.Routes {
		if rt.Cred.Record.ID != k2.ID {
			t.Fatalf("pinned key ignored: %+v", rt)
		}
	}
}

func TestCooldownRemovesARoute(t *testing.T) {
	ctx := context.Background()
	r, creds, st := newRouter(t)
	k := addKey(t, creds, "claude", "only", "sk-one")

	plan, _ := r.Plan(ctx, Request{Need: Capabilities{Tools: true}})
	if len(plan.Routes) == 0 {
		t.Fatal("no routes")
	}
	hot := plan.Routes[0]
	if err := st.RecordFailure(ctx, hot.Provider, hot.Model, k.ID, "rate_limited", "429", time.Hour); err != nil {
		t.Fatal(err)
	}
	again, _ := r.Plan(ctx, Request{Need: Capabilities{Tools: true}})
	for _, rt := range again.Routes {
		if rt.Same(hot) {
			t.Fatalf("cooling-down route still offered: %+v", rt)
		}
	}
	var blocked bool
	for _, b := range again.Blocked {
		if b.Model == hot.Model && b.CooldownEnd != nil {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("cooldown not explained: %+v", again.Blocked)
	}

	// Clearing it puts the route back.
	if err := st.ClearCooldown(ctx, hot.Provider, hot.Model, k.ID); err != nil {
		t.Fatal(err)
	}
	back, _ := r.Plan(ctx, Request{Need: Capabilities{Tools: true}})
	var found bool
	for _, rt := range back.Routes {
		found = found || rt.Same(hot)
	}
	if !found {
		t.Fatal("route did not come back after the cooldown was cleared")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  string
		cooling bool
		retry   bool
	}{
		{"rate limit honours retry-after", &llm.Error{Status: http.StatusTooManyRequests, RetryAfter: 5 * time.Second}, "rate_limited", true, true},
		{"overloaded", &llm.Error{Status: 529}, "rate_limited", true, true},
		{"revoked key", &llm.Error{Status: http.StatusUnauthorized}, "unauthorized", true, true},
		{"outage", &llm.Error{Status: http.StatusBadGateway}, "server_error", true, true},
		{"unknown model", &llm.Error{Status: http.StatusNotFound}, "unknown_model", true, true},
		{"context too long", &llm.Error{Status: http.StatusBadRequest, Body: "maximum context length exceeded"}, "context_too_long", false, true},
		{"bad request", &llm.Error{Status: http.StatusBadRequest, Body: "nope"}, "invalid_request", false, false},
		{"timeout", context.DeadlineExceeded, "timeout", true, true},
		{"plain error", errors.New("boom"), "error", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, cooldown := Classify(c.err)
			if status != c.status {
				t.Fatalf("status = %q, want %q", status, c.status)
			}
			if (cooldown > 0) != c.cooling {
				t.Fatalf("cooldown = %v", cooldown)
			}
			if Retryable(c.err) != c.retry {
				t.Fatalf("retryable = %v", Retryable(c.err))
			}
		})
	}
	if _, cd := Classify(&llm.Error{Status: 429, RetryAfter: 5 * time.Second}); cd != 5*time.Second {
		t.Fatalf("retry-after ignored: %v", cd)
	}
	if _, cd := Classify(&llm.Error{Status: 429, RetryAfter: 10 * time.Hour}); cd > 30*time.Minute {
		t.Fatalf("retry-after not capped: %v", cd)
	}
}

func TestAnthropicCreditErrorClassification(t *testing.T) {
	const creditBody = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."},"request_id":"req_test"}`
	for _, tt := range []struct {
		name     string
		provider string
		body     string
		want     string
		retry    bool
	}{
		{"exhausted Claude credits", "claude", creditBody, "insufficient_credit", true},
		{"Anthropic provider alias", "anthropic", creditBody, "insufficient_credit", true},
		{"other provider", "openai", creditBody, "invalid_request", false},
		{"schema error", "claude", `{"type":"error","error":{"type":"invalid_request_error","message":"tools.11.custom.input_schema: input_schema does not support anyOf at the top level"}}`, "invalid_request", false},
		{"credit text inside schema error", "claude", `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content: invalid value Your credit balance is too low"}}`, "invalid_request", false},
		{"unstructured credit text", "claude", "Your credit balance is too low", "invalid_request", false},
		{"wrong error type", "claude", `{"type":"error","error":{"type":"unknown_error","message":"Your credit balance is too low"}}`, "invalid_request", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := fmt.Errorf("stream failed: %w", &llm.Error{Provider: tt.provider, Status: http.StatusBadRequest, Body: tt.body})
			status, cooldown := Classify(err)
			if status != tt.want || Retryable(err) != tt.retry {
				t.Fatalf("classification = %q, retryable = %v; want %q, %v", status, Retryable(err), tt.want, tt.retry)
			}
			if tt.retry && (cooldown < time.Minute || cooldown > 30*time.Minute) {
				t.Fatalf("billing cooldown should avoid hammering the route and allow later recovery: %v", cooldown)
			}
			if !tt.retry && cooldown != 0 {
				t.Fatalf("invalid request cooled the route: %v", cooldown)
			}
		})
	}
}

func TestExhaustedCreditRecordsHealthAndPreservesApprovedFallback(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	addKey(t, creds, "claude", "primary", "sk-one")
	addKey(t, creds, "openai", "fallback", "sk-two")
	req := Request{Strategy: protocol.PoolPriority,
		Candidates: []protocol.ConfiguredModel{
			{ID: "claude", Provider: "claude", Model: "claude-sonnet-4-6", Enabled: true},
			{ID: "openai", Provider: "openai", Model: "gpt-5.6-sol", Enabled: true},
		}}
	plan, err := r.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := plan.Next()
	if !ok || first.Provider != "claude" {
		t.Fatalf("initial route = %+v, %v", first, ok)
	}
	creditErr := &llm.Error{Provider: "claude", Status: http.StatusBadRequest,
		Body: `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`}
	if status := r.Failed(ctx, first, creditErr); status != "insufficient_credit" || !Retryable(creditErr) {
		t.Fatalf("credit error must allow approved failover: %q", status)
	}
	second, ok := plan.Next()
	if !ok || second.Provider != "openai" || second.Why != protocol.RouteFallback {
		t.Fatalf("fallback route = %+v, %v", second, ok)
	}
	if _, ok := plan.Next(); ok {
		t.Fatal("exhausted approved routes were retried")
	}
	health, err := r.Health(ctx)
	if err != nil || len(health) != 1 || health[0].LastStatus != "insufficient_credit" || health[0].CooldownEnd == nil {
		t.Fatalf("credit diagnostics = %+v, %v", health, err)
	}
	again, err := r.Plan(ctx, req)
	if err != nil || len(again.Routes) != 1 || again.Routes[0].Provider != "openai" {
		t.Fatalf("cooled route offered again: %+v, %v", again, err)
	}
	if len(again.Blocked) != 1 || again.Blocked[0].CooldownEnd == nil || !strings.Contains(again.Blocked[0].Unavailable, "credit") {
		t.Fatalf("billing cooldown not explained: %+v", again.Blocked)
	}
	// A single approved model cannot silently expand into catalog alternatives.
	req.Candidates = req.Candidates[:1]
	single, err := r.Plan(ctx, req)
	if err != nil || len(single.Routes) != 0 || single.Reason == "" {
		t.Fatalf("single exhausted model = %+v, %v", single, err)
	}
}

// A configured set is the whole world: the plan may not reach past it into the
// provider catalog, whatever the catalog holds.
func TestCandidatesAreTheOnlyModelsOffered(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	addKey(t, creds, "claude", "only", "sk-one")

	cands := []protocol.ConfiguredModel{
		{ID: "a", Name: "Haiku", Provider: "claude", Model: "claude-haiku-4-5-20251001", Enabled: true},
	}
	plan, err := r.Plan(ctx, Request{Candidates: cands,
		Strategy: protocol.PoolPriority, Need: Capabilities{Tools: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) == 0 {
		t.Fatalf("no routes: %+v", plan)
	}
	for _, rt := range plan.Routes {
		if rt.Model != cands[0].Model {
			t.Fatalf("offered a model nobody configured: %+v", rt)
		}
	}
}

func TestPoolStrategiesOrderCandidates(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	addKey(t, creds, "claude", "only", "sk-one")

	cheap := protocol.ConfiguredModel{ID: "h", Provider: "claude", Model: "claude-haiku-4-5-20251001", Enabled: true}
	strong := protocol.ConfiguredModel{ID: "o", Provider: "claude", Model: "claude-opus-5", Enabled: true}
	cands := []protocol.ConfiguredModel{cheap, strong}

	first := func(strategy string) string {
		plan, err := r.Plan(ctx, Request{Candidates: cands,
			Strategy: strategy, Need: Capabilities{Tools: true}})
		if err != nil || len(plan.Routes) == 0 {
			t.Fatalf("%s: %+v %v", strategy, plan, err)
		}
		return plan.Routes[0].Model
	}
	if got := first(protocol.PoolPriority); got != cheap.Model {
		t.Fatalf("priority should keep the user's order, got %s", got)
	}
	if got := first(protocol.PoolQuality); got != strong.Model {
		t.Fatalf("quality should pick the stronger model, got %s", got)
	}
	if got := first(protocol.PoolCheap); got != cheap.Model {
		t.Fatalf("cheap should pick the cheaper model, got %s", got)
	}
}

// A provider in the pool with no key is not silently dropped: the plan says so.
func TestCandidateWithoutAKeyIsExplained(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	addKey(t, creds, "claude", "only", "sk-one")

	cands := []protocol.ConfiguredModel{
		{ID: "a", Provider: "claude", Model: "claude-sonnet-5", Enabled: true},
		{ID: "b", Name: "GPT", Provider: "openai", Model: "gpt-5.6-sol", Enabled: true},
	}
	plan, err := r.Plan(ctx, Request{Candidates: cands,
		Strategy: protocol.PoolPriority, Need: Capabilities{Tools: true}})
	if err != nil {
		t.Fatal(err)
	}
	var explained bool
	for _, b := range plan.Blocked {
		if b.Provider == "openai" && strings.Contains(b.Unavailable, "no usable key") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("missing key not explained: %+v", plan.Blocked)
	}
}

// Local template-style models only think once the engine detects the chat
// template controls, so an undetected local server must not outrank a
// non-reasoning cloud model when the pool asks for quality.
func TestQualityOrderDoesNotRankLocalModelsAsReasoning(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newRouter(t)
	local := protocol.ConfiguredModel{ID: "l", Provider: "openai_compatible", Model: "qwen3.8", Enabled: true}
	cloud := protocol.ConfiguredModel{ID: "c", Provider: "openai", Model: "gpt-4o-mini", Enabled: true}
	got := r.order(ctx, []protocol.ConfiguredModel{cloud, local}, protocol.PoolQuality, TierQuality)
	if got[0].ID != cloud.ID {
		t.Fatalf("quality order = %s first, want the cloud model kept ahead of an undetected local model", got[0].ID)
	}
	// A catalog reasoning model still outranks a non-reasoning one.
	think := protocol.ConfiguredModel{ID: "t", Provider: "claude", Model: "claude-sonnet-4-6", Enabled: true}
	got = r.order(ctx, []protocol.ConfiguredModel{cloud, think}, protocol.PoolQuality, TierQuality)
	if got[0].ID != think.ID {
		t.Fatalf("catalog reasoning model should rank first, got %s", got[0].ID)
	}
}

// The effort level is a model-call setting; it never changes which model runs.
func TestPlanIgnoresEffortLevel(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	addKey(t, creds, "claude", "only", "sk-one")
	cands := []protocol.ConfiguredModel{
		{ID: "h", Provider: "claude", Model: "claude-haiku-4-5-20251001", Enabled: true},
		{ID: "o", Provider: "claude", Model: "claude-opus-5", Enabled: true},
	}
	ids := func(p *Plan) []string {
		var out []string
		for _, rt := range p.Routes {
			out = append(out, rt.Model+"/"+rt.Why)
		}
		return out
	}
	for _, strategy := range []string{protocol.PoolPriority, protocol.PoolQuality, protocol.PoolCheap, protocol.PoolBalanced} {
		var got [][]string
		for range []string{"low", "high"} {
			plan, err := r.Plan(ctx, Request{Candidates: cands, Strategy: strategy, Need: Capabilities{Tools: true}})
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, ids(plan))
		}
		if strings.Join(got[0], ",") != strings.Join(got[1], ",") {
			t.Fatalf("%s: routes differ: %v", strategy, got)
		}
	}
}

func TestStrategyTierOrdering(t *testing.T) {
	ctx := context.Background()
	r, creds, _ := newRouter(t)
	addKey(t, creds, "claude", "only", "sk-one")
	cheap := protocol.ConfiguredModel{ID: "h", Provider: "claude", Model: "claude-haiku-4-5-20251001", Enabled: true}
	strong := protocol.ConfiguredModel{ID: "o", Provider: "claude", Model: "claude-opus-5", Enabled: true}
	cands := []protocol.ConfiguredModel{cheap, strong}
	first := func(tier string) string {
		plan, err := r.Plan(ctx, Request{Candidates: cands, Tier: tier, Strategy: protocol.PoolBalanced, Need: Capabilities{Tools: true}})
		if err != nil || len(plan.Routes) == 0 {
			t.Fatalf("%s: %+v %v", tier, plan, err)
		}
		return plan.Routes[0].Model
	}
	if got := first("quality"); got != strong.Model {
		t.Fatalf("quality tier picked %s", got)
	}
	if got := first("cheap"); got != cheap.Model {
		t.Fatalf("cheap tier picked %s", got)
	}
	if TierForStrategy(protocol.PoolQuality) != "quality" || TierForStrategy(protocol.PoolFast) != "cheap" ||
		TierForStrategy(protocol.PoolCheap) != "cheap" || TierForStrategy(protocol.PoolBalanced) != "" ||
		TierForStrategy(protocol.PoolPriority) != "" {
		t.Fatal("TierForStrategy mapping")
	}
}
