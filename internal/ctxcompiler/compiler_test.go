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

func TestDesignedBudgetDropsP1BeforeAtomicP0(t *testing.T) {
	d := designedDetail()
	for i := range d.Nodes {
		if d.Nodes[i].ID == "f-option" {
			d.Nodes[i].Title = "OPTION_DETAIL " + strings.Repeat("support ", 2000)
		}
	}
	in := designedInput(d)
	in.Window = 8000
	res, ok := Compile(in)
	if !ok {
		t.Fatalf("P1 should drop to fit: %+v", res.Report)
	}
	text := res.Messages[0].JoinedText()
	if strings.Contains(text, "OPTION_DETAIL") || strings.Contains(text, "vault://a") {
		t.Fatal("P1 survived pressure")
	}
	for _, want := range []string{"c-gate", "public_contract", "Completion:", "z-task", "d-unknown"} {
		if !strings.Contains(text, want) {
			t.Errorf("P0 removed %q", want)
		}
	}
	if len(res.Report.Drops) == 0 {
		t.Fatal("P1 drop not reported")
	}
	in.Window = 100
	res, ok = Compile(in)
	if ok || len(res.Messages) != 0 || res.Report.Declined != "P0 over budget" {
		t.Fatalf("P0 must decline atomically: %+v", res)
	}
}

func TestDesignedInvalidGraphDeclinesWithoutMutation(t *testing.T) {
	d := designedDetail()
	d.Nodes[0].Content = []byte(`{"required":`)
	res, ok := Compile(designedInput(d))
	if ok || res.Report.Declined == "" || len(res.Messages) != 0 {
		t.Fatalf("invalid projection must decline: %+v", res)
	}
	if string(d.Nodes[0].Content) != `{"required":` || d.Work.Revision != 9 {
		t.Fatal("projection mutated graph")
	}
}

func TestDesignedTinyPositiveWindowDeclines(t *testing.T) {
	in := designedInput(designedDetail())
	in.Window = 1
	res, ok := Compile(in)
	if ok || res.Report.Declined != "P0 over budget" {
		t.Fatalf("positive window rounded into unbounded budget: %+v", res.Report)
	}
}

func TestDesignedPriorityAccounting(t *testing.T) {
	res, ok := Compile(designedInput(designedDetail()))
	if !ok || res.Report.P0Tokens <= 0 || res.Report.P1Tokens <= 0 || res.Report.P0Tokens+res.Report.P1Tokens != res.Report.WorkPacketTokens+res.Report.EvidencePacketTokens {
		t.Fatalf("priority accounting = %+v", res.Report)
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
