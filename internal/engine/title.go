package engine

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// generateTitle names a thread after its first completed turn. It asks the
// provider's cheapest model (usage recorded under role "title") and falls back
// to the first words of the user's message.
func (e *Engine) generateTitle(ctx context.Context, threadID string, turn protocol.Turn) {
	items, err := e.Store.ListItems(ctx, threadID, 0)
	if err != nil {
		return
	}
	var first, reply string
	for _, it := range items {
		if it.Kind == protocol.ItemUserMessage && first == "" {
			first = it.Text
		}
		if it.Kind == protocol.ItemAgentMessage && reply == "" && it.Status == protocol.ItemCompleted {
			reply = it.Text
		}
	}
	title := fallbackTitle(first)
	if t := e.llmTitle(ctx, turn, first, reply); t != "" {
		title = t
	}
	if title == "" {
		return
	}
	_ = e.Store.UpdateThread(ctx, threadID, map[string]any{"title": title})
}

func (e *Engine) llmTitle(ctx context.Context, turn protocol.Turn, first, reply string) string {
	provider := turn.Resolved.Provider
	model := e.Catalog.CheapModel(provider)
	if rc, ok := e.Cfg.Models.Roles["title"]; ok && rc.Model != "" && (rc.Provider == "" || rc.Provider == provider) {
		model = rc.Model
	}
	if model == "" {
		model = turn.Resolved.Model
	}
	p, ok := e.LLMs.Get(provider)
	if !ok {
		return ""
	}
	cred, err := e.Creds.Resolve(ctx, provider, turn.Resolved.CredentialID)
	if err != nil {
		return ""
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prompt := "Write a 3-6 word title for this chat. Reply with the title only, no quotes or punctuation at the end.\n\nUser: " +
		clipRunes(first, 1500) + "\n\nAssistant: " + clipRunes(reply, 1500)
	if shared := delegation.BudgetFromContext(ctx); shared != nil {
		meta := e.Catalog.Lookup(ctx, provider, model)
		budget := newTurnBudget(protocol.ExecutionLimits{})
		budget.shared = shared
		out, err := e.streamOnce(tctx, context.WithoutCancel(ctx), turn, resolved{sel: protocol.ModelSelection{Provider: provider, Model: model}, meta: meta}, cred, p, llm.Request{Model: model, Messages: []llm.Message{llm.Text(llm.RoleUser, prompt)}, MaxTokens: 60}, "title", budget)
		if err != nil {
			return ""
		}
		t := strings.TrimSpace(strings.Trim(strings.TrimSpace(out.text), `"'.`))
		if i := strings.IndexByte(t, '\n'); i >= 0 {
			t = t[:i]
		}
		return clipRunes(t, 80)
	}
	start := time.Now()
	res := resolved{sel: protocol.ModelSelection{Provider: provider, Model: model}, cred: cred, meta: e.Catalog.Lookup(ctx, provider, model)}
	request, report, err := e.prepareModelRequest(tctx, ctx, turn, &res, llm.Request{Model: model, Messages: []llm.Message{llm.Text(llm.RoleUser, prompt)}, MaxTokens: 60}, "title", newTurnBudget(protocol.ExecutionLimits{}))
	if err != nil {
		return ""
	}
	ch, err := p.Stream(tctx, cred.Material, request)
	if err != nil {
		return ""
	}
	var b strings.Builder
	var usage llm.Usage
	ok = false
	for ev := range ch {
		switch ev.Type {
		case llm.EventTextDelta:
			b.WriteString(ev.Text)
		case llm.EventDone:
			usage, ok = ev.Usage, true
		}
	}
	e.observeRequestUsage(res, report, usage.InputTokens, usage.Reported)
	meta := e.Catalog.Lookup(ctx, provider, model)
	totals := protocol.UsageTotals{InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens,
		OutputTokens: usage.OutputTokens, ReasoningTokens: usage.ReasoningTokens, Requests: 1,
		CostUSD: models.Cost(meta, usage), Estimated: !usage.Reported}
	status := "ok"
	if !ok {
		status = "error"
	}
	diagnostic := protocol.RequestDiagnostics{Phase: "title", Provider: provider, Model: model, InputTokens: &totals.InputTokens, CachedInputTokens: &totals.CachedInputTokens, OutputTokens: &totals.OutputTokens, ReasoningTokens: &totals.ReasoningTokens, MeasurementOrigin: "provider", CostState: "unavailable"}
	if totals.Estimated {
		diagnostic.MeasurementOrigin = "estimated"
	}
	if !meta.PriceUnknown && cred.Material.Kind != llm.KindChatGPT {
		diagnostic.CostState = "known"
		diagnostic.CostUSD = &totals.CostUSD
	}
	diagnostic = budgetDiagnostics(diagnostic, report)
	recorderFrom(ctx).recordRequest(diagnostic)
	_ = e.Store.InsertUsage(ctx, store.UsageRecord{CredentialID: cred.Record.ID, Provider: provider, Model: model,
		ThreadID: turn.ThreadID, TurnID: turn.ID, Role: "title", Usage: totals,
		LatencyMs: time.Since(start).Milliseconds(), Status: status})
	if !ok {
		return ""
	}
	t := strings.TrimSpace(strings.Trim(strings.TrimSpace(b.String()), `"'.`))
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	return clipRunes(t, 80)
}

func fallbackTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return clipRunes(s, 60)
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}
