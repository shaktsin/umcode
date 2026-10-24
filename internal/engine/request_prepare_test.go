package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestEveryProviderDispatchIsBudgeted(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	for _, phase := range []string{"chat", "child", "compaction", "title"} {
		res := resolved{sel: protocol.ModelSelection{Provider: "fixture", Model: "small"}, meta: models.Meta{ContextWindow: 4096}}
		req := llm.Request{System: "system", Messages: []llm.Message{llm.Text(llm.RoleUser, "hello")}, MaxTokens: 2048}
		got, r, err := e.prepareModelRequest(context.Background(), t.Context(), protocol.Turn{}, &res, req, phase, newTurnBudget(protocol.ExecutionLimits{}))
		if err != nil || got.MaxTokens != 1024 || r.EstimatedInput+r.Margin+r.ReservedOutput > 4096 {
			t.Fatalf("%s: %+v %v", phase, r, err)
		}
	}
}
func TestFallbackRepreparesForSmallerWindow(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	req := llm.Request{Messages: []llm.Message{llm.Text(llm.RoleUser, strings.Repeat("a", 10000))}, MaxTokens: 4096}
	for _, n := range []int{32768, 4096} {
		res := resolved{sel: protocol.ModelSelection{Provider: "fixture", Model: string(rune(n))}, meta: models.Meta{ContextWindow: n}}
		_, _, err := e.prepareModelRequest(t.Context(), t.Context(), protocol.Turn{}, &res, req, "compaction", newTurnBudget(protocol.ExecutionLimits{}))
		if (n == 4096) != errors.Is(err, errRequestOverCapacity) {
			t.Fatalf("window %d: %v", n, err)
		}
	}
}
func TestUnknownCapacityAdmitsRequestWithDefaultWindow(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	req := llm.Request{Messages: []llm.Message{llm.Text(llm.RoleUser, strings.Repeat("preserve me", 1000))}}
	original := req.Messages[0].Parts[0].Text
	res := resolved{sel: protocol.ModelSelection{Provider: "openai_compatible", Model: "unknown"}}
	prepared, report, err := e.prepareModelRequest(t.Context(), t.Context(), protocol.Turn{}, &res, req, "chat", newTurnBudget(protocol.ExecutionLimits{}))
	if err != nil || report.CapacitySource != "default" || report.Window != DefaultContextWindow {
		t.Fatalf("unknown capacity blocked or mislabeled the request: %+v %v", report, err)
	}
	if req.Messages[0].Parts[0].Text != original || prepared.Messages[0].Parts[0].Text != original {
		t.Fatal("request text changed during admission")
	}
}
func TestBudgetingRetainsSharedLimits(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	b := newTurnBudget(protocol.ExecutionLimits{MaxTokens: 42})
	res := resolved{sel: protocol.ModelSelection{Provider: "fixture", Model: "known"}, meta: models.Meta{ContextWindow: 32768}}
	_, _, err := e.prepareModelRequest(t.Context(), t.Context(), protocol.Turn{}, &res, llm.Request{MaxTokens: 1024}, "chat", b)
	if err != nil || b.limits.MaxTokens != 42 {
		t.Fatal("mutated execution allowance", err)
	}
}
