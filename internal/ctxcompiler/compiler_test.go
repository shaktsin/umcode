package ctxcompiler

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
)

func TestBudgetsFromWindow(t *testing.T) {
	if got := packetBudget(200000); got != 30000 {
		t.Fatalf("packetBudget = %d, want 30000", got)
	}
	if got := tailBudget(200000); got != 50000 {
		t.Fatalf("tailBudget = %d, want 50000", got)
	}
	if packetBudget(0) != 0 || tailBudget(-1) != 0 {
		t.Fatal("a non-positive window must yield 0, meaning unbounded")
	}
}

func TestEstimateCountsTextAndImages(t *testing.T) {
	msgs := []llm.Message{
		llm.Text(llm.RoleUser, strings.Repeat("a", 400)),
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: "image", MimeType: "image/png", DataB64: "x"}}},
	}
	if got, want := estimate(msgs), 100+1500+2*messageOverhead; got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func TestCompileDeclinesWithoutAGoal(t *testing.T) {
	res, ok := Compile(Input{Window: 200000})
	if ok || res.Report.Declined == "" {
		t.Fatalf("ok = %v, declined = %q", ok, res.Report.Declined)
	}
}
